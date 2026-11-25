import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import tomllib
import unittest

ROOT = Path(__file__).resolve().parents[2]
MIGRATION = (ROOT / 'target/linux/mediatek/filogic/base-files'
             / 'etc/uci-defaults/96-openfi-modemd-wan')


class UsbWanTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory(dir=ROOT / '.build-local')
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.board = self.root / 'board'
        self.board.write_text('openfi,6c\n')
        self.modemd = self.root / 'modemd.toml'
        self.modemd.write_text('[net]\nauto_wan = true\n')
        self.config = self.root / 'network.json'
        self.commits = self.root / 'commits'
        self.values = {
            'network.wan': 'interface',
            'network.wan.device': 'eth1',
            'network.wan.proto': 'dhcp',
            'network.wan.openfi_usb_auto': '1',
            'network.wan.openfi_usb_device': 'eth1',
            'network.wan6': 'interface',
            'network.wan6.device': '@wan',
            'network.wan6.proto': 'dhcpv6',
        }

        uci = self.root / 'uci'
        uci.write_text(f'#!{sys.executable}\n' + '''
import json, os, sys
from pathlib import Path
path = Path(os.environ['TEST_CONFIG'])
config = json.loads(path.read_text())
args = [arg for arg in sys.argv[1:] if arg != '-q']
if args[0] == 'get':
    if args[1] not in config:
        sys.exit(1)
    print(config[args[1]])
elif args[0] == 'set':
    key, value = args[1].split('=', 1)
    config[key] = value
    path.write_text(json.dumps(config))
elif args == ['commit', 'network']:
    with open(os.environ['TEST_COMMITS'], 'a') as commits:
        commits.write('network\\n')
else:
    raise RuntimeError(args)
''')
        uci.chmod(0o755)

        self.script = self.root / 'migrate.sh'
        self.script.write_text(MIGRATION.read_text()
                               .replace('/tmp/sysinfo/board_name', str(self.board))
                               .replace('/etc/modemd/config.toml', str(self.modemd)))

    def apply(self):
        self.config.write_text(json.dumps(self.values))
        env = dict(os.environ, PATH=f'{self.root}:{os.environ["PATH"]}',
                   TEST_CONFIG=str(self.config), TEST_COMMITS=str(self.commits))
        subprocess.run(['sh', str(self.script)], env=env, check=True, capture_output=True)
        self.values = json.loads(self.config.read_text())

    def test_daemon_defaults_own_wan(self):
        with (ROOT / 'package/modem-pro/config.toml').open('rb') as file:
            config = tomllib.load(file)
        self.assertIs(config['net']['auto_wan'], True)
        self.assertEqual(config['net']['wan_mode'], 'dhcp')
        self.assertIs(config['net']['wan_ipv6'], True)

    def test_legacy_pair_is_disabled_once(self):
        self.board.write_text('openfi,6c1\n')
        original = self.values.copy()
        self.apply()
        self.assertEqual(self.values, original | {
            'network.wan.auto': '0',
            'network.wan.openfi_usb_auto': '0',
            'network.wan6.auto': '0',
        })
        self.apply()
        self.assertEqual(self.commits.read_text(), 'network\n')

    def test_custom_ipv6_is_retained(self):
        self.values['network.wan6.device'] = 'eth7'
        self.apply()
        self.assertEqual(self.values['network.wan.auto'], '0')
        self.assertNotIn('network.wan6.auto', self.values)
        self.assertEqual(self.values['network.wan6.device'], 'eth7')

    def test_fresh_and_manual_configs_are_retained(self):
        original = self.values.copy()
        for values in ({'network.lan': 'interface'},
                       original | {'network.wan.device': 'eth7'},
                       original | {'network.wan.proto': 'pppoe'},
                       original | {'network.wan.openfi_usb_auto': '0'}):
            with self.subTest(values=values):
                self.values = values.copy()
                self.apply()
                self.assertEqual(self.values, values)
        self.assertFalse(self.commits.exists())

    def test_other_board_and_daemon_opt_out_are_retained(self):
        original = self.values.copy()
        self.board.write_text('another,board\n')
        self.apply()
        self.assertEqual(self.values, original)
        self.board.write_text('openfi,6c\n')
        self.modemd.write_text('[net]\n  auto_wan = false # configured locally\n')
        self.apply()
        self.assertEqual(self.values, original)
        self.modemd.unlink()
        self.apply()
        self.assertEqual(self.values, original)
        self.assertFalse(self.commits.exists())


if __name__ == '__main__':
    unittest.main(verbosity=2)
