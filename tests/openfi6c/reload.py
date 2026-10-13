import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import tempfile
import time
import unittest

ROOT = Path.cwd()
SCRIPT = ROOT / 'package/mtk/applications/luci-app-openfi/root/usr/sbin/op_fan.sh'
SERVICE = ROOT / 'package/mtk/applications/luci-app-openfi/root/etc/init.d/openfi'


class ReloadTests(unittest.TestCase):
    def test_live_mode_changes_preserve_pwm_and_curve(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            config, commands, run = (root / name for name in ('config', 'commands', 'run'))
            for directory in (config, commands, run):
                directory.mkdir()
            values = dict(mode='auto', manual_speed='20', min_speed='5', period='2',
                          emergency_temp='85', fan_stop='0', temp1='40', speed1='5',
                          temp2='50', speed2='25', temp3='60', speed3='50', temp4='75', speed4='80')
            for key, value in values.items():
                (config / ('openfi.fan.' + key)).write_text(value + '\n')
            zone = root / 'class/thermal/thermal_zone0'
            zone.mkdir(parents=True)
            (zone / 'temp').write_text('60000\n')
            for channel in (0, 1):
                pwm = root / f'class/pwm/pwmchip0/pwm{channel}'
                pwm.mkdir(parents=True)
                for name in ('period', 'duty_cycle', 'enable'):
                    (pwm / name).write_text('0\n')
            for name, body in {
                'uci': 'cat "$TEST_CONFIG/$3" 2>/dev/null',
                'sleep': '/bin/sleep 0.03',
                'logger': 'exit 0',
            }.items():
                path = commands / name
                path.write_text('#!/bin/sh\n' + body + '\n')
                path.chmod(0o755)
            env = dict(os.environ, OPENFI_SYSFS=tmp, OPENFI_RUNDIR=str(run),
                       TEST_CONFIG=str(config), PATH=str(commands) + ':' + os.environ['PATH'])
            trace_path = root / 'trace.log'
            with trace_path.open('w') as trace:
                process = subprocess.Popen(['sh', '-x', str(SCRIPT)], env=env, stdout=trace, stderr=trace)
                try:
                    def wait_for(mode, output):
                        deadline = time.monotonic() + 8
                        while time.monotonic() < deadline:
                            self.assertIsNone(process.poll(), 'Mode reload must keep the fan process alive')
                            try:
                                state = json.loads((run / 'openfi-fan.json').read_text())
                            except (FileNotFoundError, json.JSONDecodeError):
                                state = {}
                            if state.get('mode') == mode and state.get('output') == output:
                                return state
                            time.sleep(0.01)
                        self.fail(f'Fan did not reach {mode}/{output}%')

                    self.assertEqual(wait_for('auto', 50)['target'], 50)
                    for mode, output in (('manual', 20), ('auto', 50), ('manual', 100), ('auto', 50)):
                        if mode == 'manual':
                            (config / 'openfi.fan.manual_speed').write_text(str(output) + '\n')
                        (config / 'openfi.fan.mode').write_text(mode + '\n')
                        begin = trace_path.stat().st_size
                        process.send_signal(signal.SIGHUP)
                        self.assertEqual(wait_for(mode, output)['target'], output)
                        transition = trace_path.read_text()[begin:]
                        self.assertNotIn('+ init_pwm', transition, 'Reload must not initialize PWM again')
                        self.assertNotIn('+ shutdown', transition, 'Reload must not run the full-speed exit handler')
                        if output != 100:
                            self.assertNotIn('+ write_speed 100', transition, 'Running fan must not receive another startup boost')
                    for key, value in values.items():
                        if key.startswith(('temp', 'speed')):
                            self.assertEqual((config / ('openfi.fan.' + key)).read_text().strip(), value)
                    # A stopped fan still needs the normal startup boost.
                    (config / 'openfi.fan.manual_speed').write_text('0\n')
                    (config / 'openfi.fan.mode').write_text('manual\n')
                    process.send_signal(signal.SIGHUP)
                    wait_for('manual', 0)
                    begin = trace_path.stat().st_size
                    (config / 'openfi.fan.mode').write_text('auto\n')
                    process.send_signal(signal.SIGHUP)
                    wait_for('auto', 50)
                    self.assertIn('+ write_speed 100', trace_path.read_text()[begin:])
                    # Hot reload must not bypass CPU protection in manual mode.
                    (zone / 'temp').write_text('90000\n')
                    (config / 'openfi.fan.mode').write_text('manual\n')
                    process.send_signal(signal.SIGHUP)
                    self.assertEqual(wait_for('manual', 100)['reason'], 'overheat')
                    self.assertEqual(trace_path.read_text().count('+ init_pwm\n'), 1)
                finally:
                    process.terminate()
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()

    def test_term_during_long_period_requests_full_speed(self):
        # procd sends KILL 5 s after TERM; a 30 s sample must not delay the exit handler.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            config, commands, run = (root / name for name in ('config', 'commands', 'run'))
            for directory in (config, commands, run):
                directory.mkdir()
            values = dict(mode='auto', min_speed='5', period='30', emergency_temp='85', fan_stop='0',
                          temp1='40', speed1='5', temp2='50', speed2='25', temp3='60', speed3='50',
                          temp4='75', speed4='80')
            for key, value in values.items():
                (config / ('openfi.fan.' + key)).write_text(value + '\n')
            zone = root / 'class/thermal/thermal_zone0'
            zone.mkdir(parents=True)
            (zone / 'temp').write_text('60000\n')
            pwm = []
            for channel in (0, 1):
                node = root / f'class/pwm/pwmchip0/pwm{channel}'
                node.mkdir(parents=True)
                for name in ('period', 'duty_cycle', 'enable'):
                    (node / name).write_text('0\n')
                pwm.append(node / 'duty_cycle')
            for name, body in {'uci': 'cat "$TEST_CONFIG/$3" 2>/dev/null', 'logger': 'exit 0'}.items():
                path = commands / name
                path.write_text('#!/bin/sh\n' + body + '\n')
                path.chmod(0o755)
            env = dict(os.environ, OPENFI_SYSFS=tmp, OPENFI_RUNDIR=str(run), TEST_CONFIG=str(config),
                       PATH=str(commands) + ':' + os.environ['PATH'])
            process = subprocess.Popen([os.environ.get('OPENFI_TEST_SH', 'sh'), str(SCRIPT)], env=env)
            try:
                deadline = time.monotonic() + 5
                state = {}
                while time.monotonic() < deadline and state.get('reason') != 'auto':
                    try:
                        state = json.loads((run / 'openfi-fan.json').read_text())
                    except (FileNotFoundError, json.JSONDecodeError):
                        state = {}
                    time.sleep(0.02)
                # The startup boost leaves straight for the curve value, not 90%.
                self.assertEqual((state.get('output'), state.get('target')), (50, 50))
                self.assertEqual(pwm[0].read_text().strip(), '20000')
                process.send_signal(signal.SIGTERM)
                process.wait(timeout=3)
                for duty in pwm:
                    self.assertEqual(duty.read_text().strip(), '0')
                state = json.loads((run / 'openfi-fan.json').read_text())
                self.assertEqual((state['reason'], state['output']), ('stopped', 100))
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait()

    def test_service_reload_signals_without_stop_start(self):
        source = '. ' + shlex.quote(str(SERVICE)) + '\n'
        output = subprocess.check_output(['sh', '-c', source + '''
            procd_running() { return 0; }
            procd_send_signal() { echo "signal:$*"; }
            apply_modem_reset() { echo reset-check; }
            stop() { echo stopped; }
            start() { echo started; }
            reload_service
        '''], text=True)
        self.assertEqual(output.strip().splitlines(), ['signal:openfi fan HUP', 'reset-check'])
        output = subprocess.check_output(['sh', '-c', source + '''
            procd_running() { return 0; }
            procd_send_signal() { return 1; }
            start() { echo started; }
            reload_service
        '''], text=True)
        self.assertEqual(output.strip(), 'started')
        output = subprocess.check_output(['sh', '-c', source + '''
            procd_running() { return 1; }
            procd_send_signal() { echo unexpected-signal; }
            start() { echo started; }
            reload_service
        '''], text=True)
        self.assertEqual(output.strip(), 'started')


if __name__ == '__main__':
    unittest.main(verbosity=2)
