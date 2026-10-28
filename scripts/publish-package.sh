#!/bin/bash
set -euo pipefail
auth_args=()
if [[ -f "$REGISTRY_AUTH_DIR/config.json" ]]; then
    auth_args+=(--registry-config "$REGISTRY_AUTH_DIR/config.json")
    export DOCKER_CONFIG="$REGISTRY_AUTH_DIR"
fi
oras push "${auth_args[@]}" --format json --artifact-type "$ARTIFACT_TYPE" \
    "$IMAGE" payload.tar.gz:application/vnd.oci.image.layer.v1.tar+gzip > manifest.json
digest=$(jq -er '.digest' manifest.json)
[[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo 'Registry returned an invalid manifest digest' >&2; exit 1; }
repo=${IMAGE%@*}
if [[ "${repo##*/}" == *:* ]]; then repo=${repo%:*}; fi
cosign attach sbom --sbom sbom.spdx.json --type spdx "$repo@$digest"
sbom_digest=$(sha256sum sbom.spdx.json)
sbom_digest=${sbom_digest%% *}
printf '%s' "$IMAGE" > "$(step.results.IMAGE_URL.path)"
printf '%s' "$digest" > "$(step.results.IMAGE_DIGEST.path)"
printf '%s@sha256:%s' "$repo" "$sbom_digest" > "$(step.results.SBOM_BLOB_URL.path)"
