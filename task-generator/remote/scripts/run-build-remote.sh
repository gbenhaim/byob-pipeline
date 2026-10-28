#!/bin/bash
# Controller/OTP protocol adapted from container-build-catalog's remote generator.
set -euo pipefail
mkdir -p "$CONTROL_ROOT"
private=$(mktemp -d)
remote_dir=''
host=$(cat "$SSH_DIR/host")
[[ "$host" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.@:-]*$ ]] || { echo 'Invalid SSH host' >&2; exit 1; }
[[ "$BUILDER_IMAGE" =~ ^.+@sha256:[a-f0-9]{64}$ ]] || { echo 'Builder image must be digest-pinned' >&2; exit 1; }
if [[ -f "$SSH_DIR/otp" ]]; then
    curl --fail --silent --show-error --cacert "$SSH_DIR/otp-ca" \
        --data-binary "@$SSH_DIR/otp" "$(cat "$SSH_DIR/otp-server")" > "$private/key"
    printf '\n' >> "$private/key"
else
    cp "$SSH_DIR/id_rsa" "$private/key"
fi
chmod 0600 "$private/key"
host_key_mode=accept-new
if [[ -f "$SSH_DIR/known_hosts" ]]; then
    cp "$SSH_DIR/known_hosts" "$private/known_hosts"
    host_key_mode=yes
fi
ssh_options=(-F /dev/null -i "$private/key" -o "BatchMode=yes" -o "IdentitiesOnly=yes" \
    -o "ServerAliveInterval=30" -o "ServerAliveCountMax=3" \
    -o "UserKnownHostsFile=$private/known_hosts" -o "StrictHostKeyChecking=$host_key_mode")
# Arguments are trusted commands or escaped with printf %q at the call sites.
# shellcheck disable=SC2029
remote() { ssh "${ssh_options[@]}" "$host" "$@"; }
cleanup() {
    status=$?
    trap - EXIT
    if [[ -n "$remote_dir" ]]; then
        printf -v quoted '%q' "$remote_dir"
        remote "rm -rf -- $quoted" >/dev/null 2>&1 || true
    fi
    rm -rf -- "$private"
    exit "$status"
}
trap cleanup EXIT
base=$(cat "$SSH_DIR/user-dir")
[[ "$base" =~ ^/[a-zA-Z0-9_./-]+$ && "/$base/" != *'/../'* ]] || { echo 'Invalid controller user-dir' >&2; exit 1; }
printf -v quoted '%q' "$base/byob.XXXXXXXX"
remote_dir=$(remote "mktemp -d $quoted")
[[ "$remote_dir" == "$base"/byob.* && "$remote_dir" != *$'\n'* ]] || exit 1
printf -v quoted '%q' "$remote_dir"
remote "mkdir -p $quoted/source $quoted/inputs"
tar -C "$SOURCE_ROOT" -cf - . | remote "tar -xf - -C $quoted/source"
tar -C "$INPUT_ROOT" -cf - . | remote "tar -xf - -C $quoted/inputs"
printf -v args '%q ' "$remote_dir" "$BUILDER_IMAGE" "$SCRIPT_PATH"
remote "bash -s -- $args" <<'REMOTE'
set -euo pipefail
directory=$1 image=$2 script=$3
# This global Podman option disables mounts.conf injection at create AND start.
# The similarly named containers.conf field is internal and cannot be set in TOML.
podman() { command podman --default-mounts-file=/dev/null "$@"; }
[[ "$(podman info --format '{{.Host.Security.Rootless}}')" == true ]] || { echo 'Rootless Podman is required' >&2; exit 1; }
container=''
cleanup_container() { if [[ -n "$container" ]]; then podman rm -f "$container" >/dev/null; fi; }
trap cleanup_container EXIT
podman pull --quiet "$image"
digest=$(podman image inspect --format '{{.Digest}}' "$image")
[[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || exit 1
printf '%s@%s' "${image%@*}" "$digest" > "$directory/builder-image"
container=$(podman create --user 1000:0 --cap-drop ALL \
    --security-opt no-new-privileges --tmpfs /run/secrets:rw,noexec,nosuid,nodev --http-proxy=false --entrypoint /bin/sh \
    --workdir /var/workdir/source --env HOME=/tmp/byob-home --env BUILD_INPUTS=/var/workdir/inputs \
    "$image" -eu -c 'mkdir -p "$HOME"; exec "./$1"' run-build "$script")
# Copy a directory tree into the stopped container without host bind mounts.
# Avoid padding past the archive end: Podman's copier can close its pipe early.
tar --blocking-factor=1 -C "$directory/source" --transform 'flags=rh;s,^\.,var/workdir/source,' -cf - . | podman cp - "$container:/"
tar --blocking-factor=1 -C "$directory/inputs" --transform 'flags=rh;s,^\.,var/workdir/inputs,' -cf - . | podman cp - "$container:/"
# Do not let attached execution consume the remaining bash -s program from SSH.
podman start --attach "$container" </dev/null
[[ "$(podman inspect --format '{{.State.ExitCode}}' "$container")" == 0 ]] || exit 1
mkdir "$directory/returned-source" "$directory/output"
# Copy the parent so Podman preserves an out/ symlink instead of following it.
# This temporary source snapshot stays on the VM; only out/ is returned below.
podman cp "$container:/var/workdir/source/." "$directory/returned-source/"
[[ -d "$directory/returned-source/out" && ! -L "$directory/returned-source/out" ]] || { echo 'out/ must be a real directory' >&2; exit 1; }
mv "$directory/returned-source/out" "$directory/output/out"
REMOTE
remote "cat $quoted/builder-image" > "$CONTROL_ROOT/builder-image"
returned=$(mktemp -d "$CONTROL_ROOT/returned.XXXXXXXX")
remote "tar -C $quoted/output -cf - ." | tar --no-same-owner -xf - -C "$returned"
# Keep the original checkout; return only the declared build outputs.
# The trusted validation step checks out/ before staging it for export.
rm -rf -- "$SOURCE_ROOT/out"
mv "$returned/out" "$SOURCE_ROOT/out"
rmdir "$returned"
