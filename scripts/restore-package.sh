#!/bin/bash
set -euo pipefail
[[ $# -gt 0 ]] || { echo 'At least one source artifact is required' >&2; exit 1; }
mkdir -p /var/workdir/inputs
index=0
for artifact in "$@"; do
    [[ "$artifact" =~ ^oci:.+@sha256:[a-f0-9]{64}$ ]] || { echo 'Invalid Trusted Artifact reference' >&2; exit 1; }
    destination="/var/workdir/inputs/$index"
    mkdir -p "$destination"
    /usr/local/bin/use-archive "$artifact=$destination"
    index=$((index + 1))
done
