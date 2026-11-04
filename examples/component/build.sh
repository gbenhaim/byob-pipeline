#!/bin/sh
set -eu
printf 'Running component build in %s\n' "$(pwd)"
mkdir -p "$BUILD_OUTPUT"
printf 'Built on %s\n' "$(uname -m)" > "$BUILD_OUTPUT/hello.txt"
printf 'Wrote %s/hello.txt\n' "$BUILD_OUTPUT"
if test -n "${BUILD_INPUTS:-}" && test -f "$BUILD_INPUTS/codegen/out/generated.txt"; then
    cp "$BUILD_INPUTS/codegen/out/generated.txt" "$BUILD_OUTPUT/"
    printf 'Copied generated.txt into %s\n' "$BUILD_OUTPUT"
fi
