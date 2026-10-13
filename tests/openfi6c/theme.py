import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path.cwd()
SCRIPT = ROOT / 'target/linux/mediatek/filogic/base-files/etc/uci-defaults/97-openfi-theme'
# Minimal UCI for the options the script touches, stored as key=value lines.
UCI = r'''#!/bin/sh
db="$TEST_UCI"
[ "$1" = -q ] && shift
case "$1" in
  get) sed -n "s|^$2=||p" "$db" | grep . || exit 1;;
  set) key=${2%%=*}; grep -v "^$key=" "$db" > "$db.new"; echo "$2" >> "$db.new"; mv "$db.new" "$db";;
  delete) grep -v "^$2=" "$db" > "$db.new"; mv "$db.new" "$db";;
  commit) echo committed >> "$db.log";;
esac
'''


class ThemeDefaultTests(unittest.TestCase):
    def run_script(self, values, board='openfi,6c', themes=('orchard', 'bootstrap')):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            (base / 'tmp/sysinfo').mkdir(parents=True)
            (base / 'tmp/sysinfo/board_name').write_text(board + '\n')
            for theme in themes:
                (base / 'www/luci-static' / theme).mkdir(parents=True)
            bin_dir = base / 'bin'
            bin_dir.mkdir()
            (bin_dir / 'uci').write_text(UCI)
            (bin_dir / 'uci').chmod(0o755)
            db = base / 'uci.db'
            db.write_text(''.join(f'{k}={v}\n' for k, v in values.items()))
            subprocess.run(['sh', str(SCRIPT)], check=True, env=dict(
                os.environ, OPENFI_ROOT=tmp, TEST_UCI=str(db), PATH=f'{bin_dir}:{os.environ["PATH"]}'))
            return dict(line.split('=', 1) for line in db.read_text().splitlines())

    def test_fresh_install_uses_orchard(self):
        result = self.run_script({'luci.main.mediaurlbase': '/luci-static/bootstrap'})
        self.assertEqual(result['luci.main.mediaurlbase'], '/luci-static/orchard')
        self.assertEqual(result['luci.main.openfi_theme'], '1')

    def test_aurora_upgrade_moves_to_orchard(self):
        result = self.run_script({'luci.main.mediaurlbase': '/luci-static/aurora',
                                  'luci.themes.Aurora': '/luci-static/aurora'})
        self.assertEqual(result['luci.main.mediaurlbase'], '/luci-static/orchard')
        self.assertNotIn('luci.themes.Aurora', result)

    def test_later_choice_is_kept(self):
        values = {'luci.main.mediaurlbase': '/luci-static/bootstrap', 'luci.main.openfi_theme': '1'}
        self.assertEqual(self.run_script(values)['luci.main.mediaurlbase'], '/luci-static/bootstrap')

    def test_missing_theme_is_repaired(self):
        values = {'luci.main.mediaurlbase': '/luci-static/argon', 'luci.main.openfi_theme': '1'}
        self.assertEqual(self.run_script(values)['luci.main.mediaurlbase'], '/luci-static/orchard')

    def test_installed_custom_theme_is_kept(self):
        values = {'luci.main.mediaurlbase': '/luci-static/argon'}
        result = self.run_script(values, themes=('orchard', 'bootstrap', 'argon'))
        self.assertEqual(result['luci.main.mediaurlbase'], '/luci-static/argon')

    def test_other_boards_unchanged(self):
        values = {'luci.main.mediaurlbase': '/luci-static/bootstrap'}
        self.assertEqual(self.run_script(values, board='other,board'), values)


if __name__ == '__main__':
    unittest.main(verbosity=2)
