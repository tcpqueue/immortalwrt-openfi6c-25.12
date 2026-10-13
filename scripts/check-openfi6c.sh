#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p .build-local
kernel_dir=$(find build_dir/target-*/linux-mediatek_filogic -maxdepth 1 -type d -name 'linux-6.12.*' -print -quit)
test -n "$kernel_dir" || { echo 'Run make target/linux/prepare first.' >&2; exit 1; }
gcc -E -nostdinc -undef -D__DTS__ -x assembler-with-cpp \
    -I "$kernel_dir/scripts/dtc/include-prefixes" \
    -I "$kernel_dir/arch/arm64/boot/dts/mediatek" \
    target/linux/mediatek/dts/mt7981b-openfi-6c.dts \
    -o .build-local/openfi6c.dts.preprocessed
dtc -I dts -O dtb -o .build-local/openfi6c-mtwifi.dtb \
    .build-local/openfi6c.dts.preprocessed 2> .build-local/dtc.log
for test in board fan reload hardware usb_wan hnat_rx theme; do
    uv run --no-project --python .venv/bin/python "tests/openfi6c/$test.py"
done
lua tests/openfi6c/form.lua
while IFS= read -r -d '' file; do luac -p "$file"; done < <(find package/mtk/applications/luci-app-openfi/luasrc -name '*.lua' -print0)
while IFS= read -r -d '' file; do
    if head -1 "$file" | grep -q '^#!/bin/sh'; then sh -n "$file"; fi
done < <(find package/mtk/applications/luci-app-openfi/root -type f -print0)
git diff --check
