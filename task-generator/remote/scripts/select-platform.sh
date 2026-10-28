#!/bin/bash
set -euo pipefail
if [[ -f "$SSH_DIR/error" ]]; then
    cat "$SSH_DIR/error" >&2
    exit 1
fi
host=$(cat "$SSH_DIR/host")
if [[ "$host" == localhost ]]; then
    printf local > "$(step.results.MODE.path)"
elif [[ -n "$host" ]]; then
    printf remote > "$(step.results.MODE.path)"
else
    echo 'Controller supplied an empty host' >&2
    exit 1
fi
