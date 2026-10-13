import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

ROOT = Path.cwd()
SCRIPT = ROOT / "package/mtk/applications/luci-app-openfi/root/usr/sbin/op_fan.sh"
BASE = """
mode=auto
min_speed=55
cpu_low=55
cpu_high=65
period=5
manual_speed=55
current=0
t1=55; t2=58; t3=61; t4=65
s1=55; s2=70; s3=85; s4=100
fan_stop=1; emergency_temp=85
"""

def shell(code, env=None):
    return subprocess.run(["sh", "-c", "set -- --library\n. " + shlex.quote(str(SCRIPT)) + "\n" + BASE + code],
                          text=True, capture_output=True, env=env, check=True).stdout.strip()

class FanTests(unittest.TestCase):
    def test_curve(self):
        for temp, expected in [(20,0),(54,0),(55,55),(56,60),(58,70),(60,80),(61,85),(64,96),(65,100),(105,100)]:
            with self.subTest(temp=temp):
                self.assertEqual(shell(f"cpu={temp}; choose_speed; echo $target"), str(expected))
    def test_fourth_point_is_adjustable(self):
        self.assertEqual(shell("s4=90; cpu=75; choose_speed; echo $target"),"90")
        self.assertEqual(shell("s4=90; cpu=85; choose_speed; echo $target"),"100")
    def test_no_fan_stop(self):
        self.assertEqual(shell("fan_stop=0; cpu=20; choose_speed; echo $target"),"55")
    def test_curve_validation(self):
        for bad in ["t2=56", "t3=54", "s2=54", "s4=80", "emergency_temp=66"]:
            self.assertEqual(shell(bad+"; if valid_curve; then echo bad; else echo rejected; fi"),"rejected")
    def test_every_interval_is_monotonic(self):
        values=[int(shell(f"curve {t}")) for t in range(20,101)]
        self.assertEqual(values,sorted(values))
    def test_cpu_fault(self):
        for temp in ["", "bad", "-1", "151"]:
            self.assertEqual(shell(f"cpu={shlex.quote(temp)}; choose_speed; echo $target:$reason"), "100:sensor_fault")
    def test_hysteresis(self):
        for temp, expected in [(55,55),(54,55),(53,0)]:
            self.assertEqual(shell(f"current=55; cpu={temp}; choose_speed; echo $target"), str(expected))
    def test_manual_and_override(self):
        for temp, requested, expected in [(40,0,0),(60,80,80),(40,10,55),(65,0,0),(85,0,100),(105,55,100)]:
            self.assertEqual(shell(f"mode=manual; cpu={temp}; manual_speed={requested}; choose_speed; echo $target"), str(expected))
    def test_cpu_only(self):
        self.assertEqual(shell("cpu=40; wifi=120; modem=120; choose_speed; echo $target"), "0")
    def test_temperature_millidegrees(self):
        with tempfile.TemporaryDirectory() as tmp:
            zone=Path(tmp)/"class/thermal/thermal_zone0"
            zone.mkdir(parents=True)
            env=dict(os.environ, OPENFI_SYSFS=tmp)
            for raw, expected in [("105000","105"),("55000","55"),("9000","9"),("bad",""),("-1",""),("151000","")]:
                (zone/"temp").write_text(raw+"\n")
                self.assertEqual(shell("read_temperatures; printf '%s' \"$cpu\"",env),expected)
    def test_dual_inverted_pwm(self):
        with tempfile.TemporaryDirectory() as tmp:
            chip=Path(tmp)/"class/pwm/pwmchip0"
            for ch in [0,1]:
                p=chip/f"pwm{ch}";p.mkdir(parents=True)
                for f in ["duty_cycle","period","enable"]: (p/f).write_text("0")
            env=dict(os.environ, OPENFI_SYSFS=tmp)
            shell("init_pwm",env)
            for percent,duty in [(0,40000),(5,38000),(55,18000),(77,9200),(100,0)]:
                shell(f"write_speed {percent}",env)
                for ch in [0,1]:
                    self.assertEqual((chip/f"pwm{ch}/duty_cycle").read_text().strip(),str(duty))
                    self.assertEqual((chip/f"pwm{ch}/period").read_text().strip(),"40000")
                    self.assertEqual((chip/f"pwm{ch}/enable").read_text().strip(),"1")
    def test_status_json(self):
        with tempfile.TemporaryDirectory() as tmp:
            shell("cpu=; reason=sensor_fault; current=100; target=100; status",dict(os.environ,OPENFI_RUNDIR=tmp))
            data=json.loads((Path(tmp)/"openfi-fan.json").read_text())
            self.assertIsNone(data["cpu"])
            self.assertEqual(data["output"],100)
            self.assertNotIn("modem",data)
    def test_invalid_config_fallback(self):
        out=shell("""uci() { case "$*" in
        *cpu_temp_low) echo 90;; *cpu_temp_high) echo 30;;
        *min_speed) echo 08;; *period) echo 0005;; *) echo broken;; esac; }
        load_config; echo "$cpu_low:$cpu_high:$min_speed:$period:$mode"
        """)
        self.assertEqual(out,"55:65:8:5:auto")
    def test_five_percent_floor(self):
        self.assertEqual(shell("min_speed=5; s1=5; cpu=55; choose_speed; echo $target"), "5")
        self.assertEqual(shell("min_speed=5; mode=manual; manual_speed=1; cpu=40; choose_speed; echo $target"), "5")
        self.assertEqual(shell("min_speed=5; mode=manual; manual_speed=0; cpu=40; choose_speed; echo $target"), "0")
        self.assertEqual(shell("min_speed=5; mode=manual; manual_speed=5; cpu=85; choose_speed; echo $target"), "100")
    def test_slew_and_floor(self):
        for current,target,expected in [(90,60,80),(55,0,0),(80,100,100),(0,59,59),(60,55,55)]:
            self.assertEqual(shell(f"current={current}; target={target}; next_output; echo $next"),str(expected))
    def test_boost_goes_straight_to_target(self):
        # The 1 s full-speed boost must not be ramped down 10 points per sample.
        self.assertEqual(shell("min_speed=5; boosted=1; current=100; target=5; next_output; echo $next:$boosted"),"5:0")
        self.assertEqual(shell("boosted=1; current=100; target=0; next_output; echo $next"),"0")
        # Only the boost itself is exempt; later reductions are rate limited again.
        self.assertEqual(shell("min_speed=5; boosted=1; current=100; target=60; next_output; current=$next; target=20; next_output; echo $next"),"50")
    def test_pwm_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertEqual(shell("if init_pwm; then echo bad; else echo failed; fi",dict(os.environ,OPENFI_SYSFS=tmp)),"failed")

if __name__ == "__main__": unittest.main(verbosity=2)
