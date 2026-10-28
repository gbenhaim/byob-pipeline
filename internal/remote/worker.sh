#!/bin/bash
# This program runs on the VM; all controller and transfer logic lives in Go.
set -euo pipefail
directory=$1 image=$2 script=$3
# This global option disables mounts.conf injection at both create and start.
podman() { command podman --default-mounts-file=/dev/null "$@"; }
[[ "$(podman info --format '{{.Host.Security.Rootless}}')" == true ]] || { echo 'Rootless Podman is required' >&2; exit 1; }
container=''
cleanup_container() {
    if [[ -n "$container" ]]; then
        podman rm -f "$container" >/dev/null
        rm -f "$directory/container-id"
    fi
}
trap cleanup_container EXIT
podman pull --quiet "$image"
digest=$(podman image inspect --format '{{.Digest}}' "$image")
[[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || exit 1
printf '%s@%s' "${image%@*}" "$digest" > "$directory/builder-image"
# Keep the ID available even if SSH is interrupted before create returns.
# The literal HOME and positional argument expand only inside the container.
# shellcheck disable=SC2016
container=$(podman create --cidfile "$directory/container-id" --user 1000:0 --cap-drop ALL \
    --security-opt no-new-privileges --tmpfs /run/secrets:rw,noexec,nosuid,nodev --http-proxy=false --entrypoint /bin/sh \
    --workdir /var/workdir/source --env HOME=/tmp/byob-home --env BUILD_INPUTS=/var/workdir/inputs \
    "$image" -eu -c 'mkdir -p "$HOME"; exec "./$1"' run-build "$script")
# Avoid padding past the archive end: Podman's copier can close its pipe early.
# Transform archive names and hard links, preserving relative symlink targets.
tar --blocking-factor=1 -C "$directory/source" --transform 'flags=rh;s,^\.,var/workdir/source,' -cf - . | podman cp - "$container:/"
tar --blocking-factor=1 -C "$directory/inputs" --transform 'flags=rh;s,^\.,var/workdir/inputs,' -cf - . | podman cp - "$container:/"
# Attached execution must not consume the remaining bash -s program from SSH.
podman start --attach "$container" </dev/null
[[ "$(podman inspect --format '{{.State.ExitCode}}' "$container")" == 0 ]] || exit 1
mkdir "$directory/returned-source" "$directory/output"
# Copy the parent so Podman preserves an out/ symlink instead of following it.
# This snapshot stays on the VM; only out/ is transferred to the task pod.
podman cp "$container:/var/workdir/source/." "$directory/returned-source/"
[[ -d "$directory/returned-source/out" && ! -L "$directory/returned-source/out" ]] || { echo 'out/ must be a real directory' >&2; exit 1; }
mv "$directory/returned-source/out" "$directory/output/out"
