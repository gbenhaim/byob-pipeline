#!/bin/sh
set -eu
mkdir -p out
printf 'Built on %s\n' "$(uname -m)" > out/hello.txt
if test -n "${BUILD_INPUTS:-}" && test -f "$BUILD_INPUTS/out/generated.txt"; then
    cp "$BUILD_INPUTS/out/generated.txt" out/
fi
