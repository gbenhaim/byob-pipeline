#!/bin/sh
set -eu
mkdir -p out
printf 'Built on %s\n' "$(uname -m)" > out/hello.txt
if test -f generated.txt; then cp generated.txt out/; fi
