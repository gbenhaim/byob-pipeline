#!/bin/sh
set -eu
mkdir -p "$BUILD_OUTPUT"
printf 'Generated from reviewed source\n' > "$BUILD_OUTPUT/generated.txt"
