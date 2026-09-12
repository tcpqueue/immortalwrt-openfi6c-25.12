import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
BASE = ROOT / 'target/linux/mediatek/filogic/base-files'

class UsbWanTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(dir=ROOT / '.build-local')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        for name in ('net', 'run', 'bin'):
            (self.root / name).mkdir()
        (self.root / 'board').write_text('openfi,6c\n')
        self.config = self.root / 'config.json'
        self.values = {'network.wan.device': 'usb0', 'network.wan.proto': 'dhcp',
                       'network.wan.openfi_usb_auto': '1', 'network.wan.openfi_usb_device': 'usb0'}
        uci = self.root / 'bin/uci'
        uci.write_text(f'#!{sys.executable}\n' + '''
import json, os, sys
from pathlib import Path
p = Path(os.environ['TEST_CONFIG'])
c = json.loads(p.read_text())
a = [s for s in sys.argv[1:] if s != '-q']
if a[0] == 'get':
    if a[1] not in c: sys.exit(1)
    print(c[a[1]])
elif a[0] == 'set':
    k, v = a[1].split('=', 1); c[k] = v; p.write_text(json.dumps(c))
elif a[0] == 'commit': pass
else: raise RuntimeError(a)
''')
        uci.chmod(0o755)
        for command in ('logger', 'ubus'):
            path = self.root / 'bin' / command
            path.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$TEST_COMMANDS"\n')
            path.chmod(0o755)
        source = (BASE / 'usr/sbin/openfi-usb-wan').read_text()
        source = source.replace('/tmp/sysinfo/board_name', str(self.root / 'board'))
        source = source.replace('/sys/class/net', str(self.root / 'net'))
        source = source.replace('/var/run', str(self.root / 'run'))
        self.script = self.root / 'select.sh'
        self.script.write_text(source)

    def add_device(self, name, driver, usb=True):
        device = self.root / ('devices/usb1/1-1/1-1:1.0/' + name if usb else 'devices/platform/' + name)
        device.mkdir(parents=True)
        driver_dir = self.root / 'drivers' / driver
        driver_dir.mkdir(parents=True, exist_ok=True)
        (device / 'driver').symlink_to(driver_dir)
        net = self.root / 'net' / name
        net.mkdir()
        (net / 'device').symlink_to(device)

    def apply(self):
        self.config.write_text(json.dumps(self.values))
        env = dict(os.environ, PATH=str(self.root / 'bin') + ':' + os.environ['PATH'],
                   TEST_CONFIG=str(self.config), TEST_COMMANDS=str(self.root / 'commands'))
        subprocess.run(['sh', str(self.script)], env=env, check=True, capture_output=True)
        self.values = json.loads(self.config.read_text())
        return self.values['network.wan.device']

    def test_names_and_drivers(self):
        for name, driver in [('usb0', 'rndis_host'), ('eth1', 'cdc_ether'), ('wwan0', 'cdc_ether')]:
            with self.subTest(name=name):
                self.add_device(name, driver)
                self.values['network.wan.device'] = self.values['network.wan.openfi_usb_device'] = 'missing0'
                # Other prior fixtures must not affect selection.
                for node in (self.root / 'net').iterdir():
                    if node.name != name:
                        (node / 'device').unlink(); node.rmdir()
                self.assertEqual(self.apply(), name)

    def test_reject_physical_and_raw_modem(self):
        self.add_device('eth0', 'mtk_soc_eth', False)
        self.add_device('wwan0', 'qmi_wwan')
        self.add_device('wwan1', 'cdc_mbim')
        self.assertEqual(self.apply(), 'usb0')

    def test_manual_config_and_other_board(self):
        self.add_device('eth1', 'rndis_host')
        self.values['network.wan.device'] = 'custom0'
        self.assertEqual(self.apply(), 'custom0')
        self.values['network.wan.device'] = 'usb0'
        self.values['network.wan.proto'] = 'qmi'
        self.assertEqual(self.apply(), 'usb0')
        self.values['network.wan.proto'] = 'dhcp'
        self.values['network.wan.openfi_usb_auto'] = '0'
        self.assertEqual(self.apply(), 'usb0')
        self.values['network.wan.openfi_usb_auto'] = '1'
        (self.root / 'board').write_text('another,board')
        self.assertEqual(self.apply(), 'usb0')

    def test_keep_existing_and_rebind_after_removal(self):
        self.add_device('usb0', 'rndis_host')
        self.add_device('eth1', 'cdc_ether')
        self.assertEqual(self.apply(), 'usb0')
        (self.root / 'net/usb0/device').unlink()
        (self.root / 'net/usb0').rmdir()
        self.assertEqual(self.apply(), 'eth1')
        self.assertEqual(self.values['network.wan.openfi_usb_device'], 'eth1')
        self.assertEqual(self.apply(), 'eth1')

if __name__ == '__main__':
    unittest.main(verbosity=2)
