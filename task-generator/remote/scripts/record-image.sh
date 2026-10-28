#!/bin/bash
set -euo pipefail
image="$BUILDER_IMAGE"
if [[ "$EXECUTION_MODE" == remote ]]; then
    image=$(cat "$CONTROL_ROOT/builder-image")
fi
if [[ ! "$image" =~ ^.+@sha256:[a-f0-9]{64}$ ]]; then
    echo 'Builder must use an immutable sha256 image reference' >&2
    exit 1
fi
# On localhost this is the requested input pin. Chains also records the actual
# developer step imageID. On remote it is the manifest observed by Podman.
jq -cn --arg uri "${image%@*}" --arg digest "${image##*@}" \
    '{uri:$uri,digest:$digest}' > "$(step.results.BUILD_IMAGE_ARTIFACT_INPUTS.path)"
