#!/bin/sh
set -eu
printf 'Running component build in %s\n' "$(pwd)"
mkdir -p out
printf 'Built on %s\n' "$(uname -m)" > out/hello.txt
printf 'Wrote out/hello.txt\n'
if test -n "${BUILD_INPUTS:-}" && test -f "$BUILD_INPUTS/0/out/generated.txt"; then
    cp "$BUILD_INPUTS/0/out/generated.txt" out/
    printf 'Copied generated.txt into out/\n'
fi
