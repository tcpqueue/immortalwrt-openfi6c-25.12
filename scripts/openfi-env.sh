#!/bin/bash
# Source this file from Bash before working with the build system.
OPENFI_BUILD_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
case "$OPENFI_BUILD_ROOT" in
    /mnt/*)
        printf '%s\n' 'Move this checkout to a Linux native directory before building.' >&2
        return 1
        ;;
esac
if [ ! -x "$OPENFI_BUILD_ROOT/.venv/bin/python" ]; then
    printf '%s\n' 'Create the project environment: uv venv --python 3.12.14 .venv' >&2
    return 1
fi
export PATH="$OPENFI_BUILD_ROOT/.venv/bin:$HOME/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
export UV_CACHE_DIR="$OPENFI_BUILD_ROOT/.build-local/uv-cache"
export PYTHONNOUSERSITE=1
export LC_ALL=C.UTF-8
unset OPENFI_BUILD_ROOT
