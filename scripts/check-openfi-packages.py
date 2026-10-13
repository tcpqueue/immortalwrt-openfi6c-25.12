from pathlib import Path
root = Path(__file__).resolve().parent.parent
config = (root / '.config').read_text().splitlines()
missing = [key for key in (root / 'defconfig/openfi6c-required-packages.txt').read_text().splitlines() if key + '=y' not in config]
assert not missing, missing
assert 'CONFIG_TARGET_mediatek_filogic_DEVICE_openfi_6c=y' in config
for prefix in ('CONFIG_PACKAGE_shadowsocks', 'CONFIG_PACKAGE_v2ray-core=', 'CONFIG_PACKAGE_v2ray-plugin=', 'CONFIG_PACKAGE_sing-box='):
    assert not any(line.startswith(prefix) and line.endswith(('=y', '=m')) for line in config), prefix
for path in (root / 'package/user-apps').iterdir():
    assert not any((root / 'package/feeds').glob('*/' + path.name)), path.name
for parent in ('passwall2', 'passwall-packages', 'tailscale-community', 'openlist'):
    for file in (root / 'package' / parent).rglob('Makefile'):
        assert not any((root / 'package/feeds').glob('*/' + file.parent.name)), file
assert not any(word in (root / 'feeds.conf.default').read_text() for word in ('passwall', 'openlist', 'tailscale', 'aurora', 'orchard'))
print('OpenFi 6C target, all requested selections, Xray-only proxy core and direct package sources verified.')
