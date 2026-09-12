from pathlib import Path
import os
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
DTB = ROOT / '.build-local/openfi6c-mtwifi.dtb'

def run(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, capture_output=True, **kwargs).stdout.strip()

def prop(node, name, kind='s'):
    return run('fdtget', '-t', kind, str(DTB), node, name)

class AdaptationTests(unittest.TestCase):
    def test_hnat_single_mac_defaults(self):
        node = '/soc/hnat@15000000'
        self.assertEqual(prop(node, 'mtketh-ppd'), 'eth0')
        self.assertEqual(prop(node, 'mtketh-lan'), 'eth0')
        self.assertEqual(prop(node, 'mtketh-wan'), '/')
        self.assertEqual(prop(node, 'mtketh-lan2'), '/')
        self.assertEqual(prop(node, 'ext-devices-prefix'), 'wwan usb eth')
        self.assertEqual(prop('/soc/ethernet@15100000/mac@1', 'reg', 'x'), '1')
        children = run('fdtget', '-l', str(DTB), '/soc/ethernet@15100000').splitlines()
        self.assertNotIn('mac@0', children)

    def test_driver_and_config(self):
        config = (ROOT / '.config').read_text().splitlines()
        for symbol in ('TARGET_mediatek_filogic_DEVICE_openfi_6c',
                       'PACKAGE_kmod-mt_wifi', 'MTK_CHIP_MT7981',
                       'MTK_MT_WIFI_DRIVER_VERSION_7673',
                       'PACKAGE_mtwifi-cfg-ucode', 'PACKAGE_luci-app-mtwifi-cfg'):
            self.assertIn(f'CONFIG_{symbol}=y', config)
        for line in config:
            if line.startswith(('CONFIG_PACKAGE_kmod-mt76', 'CONFIG_PACKAGE_kmod-mt7915e',
                                'CONFIG_PACKAGE_kmod-usb-serial-qualcomm=')):
                self.assertFalse(line.endswith(('=y', '=m')), line)
        self.assertEqual(prop('/soc/wifi@18000000', 'compatible'), 'mediatek,wbsys')
        self.assertEqual(prop('/soc/wifi@18000000', 'chip_id', 'x'), '7981')
        self.assertEqual(prop('/soc/wifi@18000000', 'status'), 'okay')

    def test_factory_and_flash_layout(self):
        factory = '/soc/spi@1100a000/flash@0/partitions/partition@180000'
        self.assertEqual(prop(factory, 'reg', 'x'), '180000 200000')
        self.assertEqual(prop(factory, 'label'), 'Factory')
        phandle = prop(factory, 'phandle', 'x')
        self.assertEqual(prop('/soc/wifi@18000000', 'mediatek,mtd-eeprom', 'x'), phandle + ' 0')
        self.assertEqual(prop('/soc/spi@1100a000/flash@0/partitions/partition@680000', 'reg', 'x'),
                         '680000 e980000')
        baseline = run('git', 'show', 'HEAD:target/linux/mediatek/dts/mt7981b-openfi-6c.dts', cwd=ROOT)
        current = (ROOT / 'target/linux/mediatek/dts/mt7981b-openfi-6c.dts').read_text()
        self.assertEqual(baseline.split('&spi0 {')[1].split('&pio {')[0],
                         current.split('&spi0 {')[1].split('&pio {')[0])

    def test_network_defaults(self):
        path = 'target/linux/mediatek/filogic/base-files/etc/board.d/02_network'
        self.assertIn('ucidef_set_interfaces_lan_wan "eth0" "usb0"', (ROOT / path).read_text())
        self.assertTrue((ROOT / 'target/linux/mediatek/filogic/base-files/etc/uci-defaults/96-openfi-usb-wan').exists())

    def test_official_gpio_and_pwm_ownership(self):
        self.assertEqual(prop('/pwm-fan', 'status'), 'disabled')
        self.assertEqual(prop('/soc/pwm@10048000', 'status'), 'okay')
        self.assertEqual(prop('/gpio-export/lte_power', 'gpios', 'x').split()[1:], ['9', '1'])
        self.assertEqual(prop('/gpio-export/lte_reset', 'gpios', 'x').split()[1:], ['a', '1'])
        self.assertEqual(prop('/gpio-export/lte_reset_2', 'gpios', 'x').split()[1:], ['c', '0'])
        for node, name, pin in [('led-0', 'system', '2'), ('led-1', 'internet', '22'),
                                ('led-2', 'wifi', '23'), ('led-3', 'modem', 'b')]:
            self.assertEqual(prop('/gpio-leds/' + node, 'label'), name)
            self.assertEqual(prop('/gpio-leds/' + node, 'gpios', 'x').split()[1:], [pin, '1'])
        package = ROOT / 'package/mtk/applications/luci-app-openfi/root'
        self.assertFalse((package / 'usr/opkg/distfeeds.conf').exists())
        migration = (package / 'etc/uci-defaults/95-openfi-hardware').read_text()
        for operation in ('uci set network', 'uci commit network', 'config_load network'):
            self.assertNotIn(operation, migration)

    def test_mac_assignment_and_user_override(self):
        system = (ROOT / 'package/base-files/files/lib/functions/system.sh').read_text()
        hotplug = (ROOT / 'target/linux/mediatek/filogic/base-files/etc/hotplug.d/net/09-fix-mtwifi-mac').read_text()
        script = '\n'.join(line for line in (system + '\n' + hotplug).splitlines()
                           if not line.startswith('. /'))
        shims = '''
board_name() { echo "${TEST_BOARD:-openfi,6c}"; }
mtd_get_mac_binary() { [ "$1:$2" = 'Factory:0x4' ] || exit 91; echo "$TEST_BASE"; }
l1util() { echo "$TEST_DIR/$2.dat"; }
'''
        # Define helpers before the real hotplug event handler executes.
        script = script.replace('[ "${ACTION}" = "add" ]', shims + '\n[ "${ACTION}" = "add" ]')
        with tempfile.TemporaryDirectory(dir=ROOT / '.build-local') as work:
            directory = Path(work)
            for base, expected in [('00:11:22:33:44:55', ('00:11:22:33:44:57', '00:11:22:33:44:58')),
                                   ('00:11:22:33:44:fe', ('00:11:22:33:45:00', '00:11:22:33:45:01'))]:
                for interface in ('ra0', 'rax0'):
                    (directory / (interface + '.dat')).write_text('Default\nSSID1=test\n')
                env = dict(os.environ, TEST_DIR=work, TEST_BASE=base, ACTION='add', INTERFACE='ra0')
                run('bash', '-c', script, env=env)
                for interface, mac in zip(('ra0', 'rax0'), expected):
                    self.assertIn('MacAddress=' + mac, (directory / (interface + '.dat')).read_text())
                # Subsequent events must preserve configured addresses.
                env['TEST_BASE'] = '00:11:22:33:55:66'
                run('bash', '-c', script, env=env)
                for interface, mac in zip(('ra0', 'rax0'), expected):
                    self.assertIn('MacAddress=' + mac, (directory / (interface + '.dat')).read_text())
            for base in ('', 'invalid', '00:00:00:00:00:00', 'ff:ff:ff:ff:ff:ff'):
                for interface in ('ra0', 'rax0'):
                    (directory / (interface + '.dat')).write_text('Default\n')
                env['TEST_BASE'] = base
                run('bash', '-c', script, env=env)
                self.assertEqual((directory / 'ra0.dat').read_text(), 'Default\n')
                self.assertEqual((directory / 'rax0.dat').read_text(), 'Default\n')

if __name__ == '__main__':
    unittest.main(verbosity=2)
