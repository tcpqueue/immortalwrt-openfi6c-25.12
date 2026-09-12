import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

ROOT = Path.cwd()
PKG = ROOT / 'package/mtk/applications/luci-app-openfi'
FAN = PKG / 'root/usr/sbin/op_fan.sh'
HARDWARE = PKG / 'root/usr/share/openfi/hardware.sh'


class HardwareTests(unittest.TestCase):
    def test_pwm_cold_boot_kernel_period_constraint(self):
        # Model the kernel's EINVAL when duty is written before a valid period.
        shim = r'''
echo() {
    exec 3>&1
    destination=$(readlink /proc/self/fd/3)
    case "$destination" in
      */duty_cycle)
        period_file=${destination%/*}/period
        [ "$(cat "$period_file")" -gt 0 ] || return 1;;
    esac
    printf '%s\n' "$*"
}
init_pwm
'''
        for script, success in [(FAN, True)]:
            with self.subTest(script=script), tempfile.TemporaryDirectory() as tmp:
                for channel in (0, 1):
                    node = Path(tmp) / f'class/pwm/pwmchip0/pwm{channel}'
                    node.mkdir(parents=True)
                    for attr in ('period', 'duty_cycle', 'enable'):
                        (node / attr).write_text('0\n')
                result = subprocess.run(['sh', '-c', 'set -- --library\n. ' + shlex.quote(str(script)) + '\n' + shim],
                                        env=dict(os.environ, OPENFI_SYSFS=tmp), capture_output=True)
                self.assertEqual(result.returncode == 0, success, result.stderr)
                if success:
                    for channel in (0, 1):
                        node = Path(tmp) / f'class/pwm/pwmchip0/pwm{channel}'
                        self.assertEqual((node / 'period').read_text().strip(), '40000')
                        self.assertEqual((node / 'duty_cycle').read_text().strip(), '0')
                        self.assertEqual((node / 'enable').read_text().strip(), '1')

    def test_switch_power_led_and_no_network_mutation(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            gpio = base / 'class/gpio/lte_power'
            gpio.mkdir(parents=True)
            (gpio / 'value').write_text('0\n')
            run = base / 'run'
            run.mkdir()
            for name in ('system', 'wifi', 'internet', 'modem'):
                led = base / 'class/leds' / name
                led.mkdir(parents=True)
                (led / 'trigger').write_text('none [netdev] timer\n')
                (led / 'brightness').write_text('1\n')
            code = '. ' + shlex.quote(str(HARDWARE)) + r'''
uci() {
    case "$*" in
      '-q get openfi.switch.func') echo "$FUNC";;
      '-q get openfi.switch.power') echo 1;;
      '-q get openfi.switch.led') echo 1;;
      *) echo "Unexpected config mutation: $*" >&2; exit 99;;
    esac
}
apply_hardware
'''
            def apply(func, state):
                (run / 'openfi-switch.state').write_text(state)
                subprocess.run(['sh', '-c', code], check=True,
                               env=dict(os.environ, OPENFI_SYSFS=tmp, OPENFI_RUNDIR=str(run), FUNC=str(func)))
            apply(2, 'off')
            self.assertEqual((gpio / 'value').read_text().strip(), '1')
            apply(2, 'on')
            self.assertEqual((gpio / 'value').read_text().strip(), '0')
            apply(3, 'off')
            for led in (base / 'class/leds').iterdir():
                self.assertEqual((led / 'brightness').read_text().strip(), '0')
            apply(3, 'on')
            for led in (base / 'class/leds').iterdir():
                self.assertEqual((led / 'brightness').read_text().strip(), '1')
                self.assertEqual((led / 'trigger').read_text().strip(), 'netdev')
            apply(1, 'off')  # Retired WAN switch assignment has no network effect.
            self.assertEqual((gpio / 'value').read_text().strip(), '0')


if __name__ == '__main__':
    unittest.main(verbosity=2)
