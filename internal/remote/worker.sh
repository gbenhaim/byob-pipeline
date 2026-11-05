#!/bin/bash
# This program runs on the VM; all controller and transfer logic lives in Go.
set -euo pipefail

WORKER_DIR=$1
BUILDER_IMAGE=$2
ENTRYPOINT=$3
shift 3
CONTAINER_ID=''

# Disable mounts.conf injection for every Podman command.
podman() {
	command podman --default-mounts-file=/dev/null "$@"
}

# Require the VM's Podman service to use rootless containers.
require_rootless_podman() {
	if [[ "$(podman info --format '{{.Host.Security.Rootless}}')" != true ]]; then
		echo 'Rootless Podman is required' >&2
		exit 1
	fi
}

# Remove the container and its ID file when the worker exits.
cleanup_container() {
	if [[ -n "$CONTAINER_ID" ]]; then
		podman rm -f "$CONTAINER_ID" >/dev/null || true
		rm -f "$WORKER_DIR/container-id" || true
	fi
}

# Pull the requested image and record the immutable digest observed on the VM.
pull_builder_image() {
	local digest

	podman pull --quiet "$BUILDER_IMAGE"
	digest=$(podman image inspect --format '{{.Digest}}' "$BUILDER_IMAGE")
	[[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || exit 1
	printf '%s@%s' "${BUILDER_IMAGE%@*}" "$digest" > "$WORKER_DIR/builder-image"
}

# Create a restricted container and write its ID before create returns.
# The caller uses the cidfile to clean up if SSH is interrupted.
create_builder_container() {
	CONTAINER_ID=$(
		podman create \
			--cidfile "$WORKER_DIR/container-id" \
			--user 1000:0 \
			--cap-drop ALL \
			--security-opt no-new-privileges \
			--tmpfs /run/secrets:rw,noexec,nosuid,nodev \
			--http-proxy=false \
			--entrypoint "$ENTRYPOINT" \
			--workdir /var/source \
			--env HOME=/var/workdir/home \
			--env BUILD_INPUTS=/var/workdir/inputs \
			--env BUILD_OUTPUT=/var/byob/output \
			"$BUILDER_IMAGE" \
			"$@"
	)
}

# Tar a directory into the container with rewritten archive paths.
copy_directory() {
	local source_path=$1
	local destination_path=$2
	local transform=$3
	shift 3

	# Avoid padding past the archive end; Podman's copier may close its pipe
	# early. Transform archive names and hard links while preserving relative
	# symlink targets.
	tar --blocking-factor=1 "$@" \
		-C "$source_path" \
		--transform "$transform" \
		-cf - . |
		podman cp - "$CONTAINER_ID:$destination_path"
}

# Create the builder's private HOME with ownership and permissions for UID 1000.
copy_home() {
	mkdir -m 0700 "$WORKER_DIR/home"
	copy_directory "$WORKER_DIR/home" "/" 'flags=rh;s,^\.,var/workdir/home,' \
		--owner=1000 --group=0 --mode=0700 --numeric-owner
}

# Create an empty BUILD_OUTPUT whose parent the build user cannot replace.
copy_output_directory() {
	mkdir -m 0755 "$WORKER_DIR/output-parent"
	copy_directory "$WORKER_DIR/output-parent" "/" \
		'flags=rh;s,^\.,var/byob,' \
		--owner=0 --group=0 --mode=0755 --numeric-owner
	mkdir -m 0775 "$WORKER_DIR/empty-output"
	copy_directory "$WORKER_DIR/empty-output" "/" \
		'flags=rh;s,^\.,var/byob/output,' \
		--owner=1000 --group=0 --mode=0775 --numeric-owner
}

# Copy the source checkout, indexed inputs, and empty output directory.
copy_build_inputs() {
	copy_home
	copy_output_directory
	copy_directory "$WORKER_DIR/source" "/" \
		'flags=rh;s,^\.,var/source,' \
		--owner=1000 --group=0 --numeric-owner
	copy_directory "$WORKER_DIR/inputs" "/" \
		'flags=rh;s,^\.,var/workdir/inputs,' \
		--owner=1000 --group=0 --numeric-owner
}

# Run the developer command and fail if its container exits unsuccessfully.
run_build_container() {
	# Keep attached execution from consuming the rest of the SSH bash program.
	podman start --attach "$CONTAINER_ID" </dev/null

	local exit_code
	exit_code=$(podman inspect --format '{{.State.ExitCode}}' "$CONTAINER_ID")
	if [[ "$exit_code" != 0 ]]; then
		exit 1
	fi
}

# Transfer only BUILD_OUTPUT back to the task pod.
collect_build_output() {
	mkdir "$WORKER_DIR/returned-output" "$WORKER_DIR/output"
	# Copy the parent so Podman preserves an output symlink
	# instead of following it. The snapshot stays on the VM.
	podman cp "$CONTAINER_ID:/var/byob/." \
		"$WORKER_DIR/returned-output/"

	local output_dir="$WORKER_DIR/returned-output/output"
	if [[ ! -d "$output_dir" || -L "$output_dir" ]]; then
		echo 'BUILD_OUTPUT must be a real directory' >&2
		exit 1
	fi

	find "$output_dir" -mindepth 1 -maxdepth 1 \
		-exec mv {} "$WORKER_DIR/output/" \;
}

# Execute the complete remote-build lifecycle.
main() {
	require_rootless_podman
	trap cleanup_container EXIT
	pull_builder_image
	create_builder_container "$@"
	copy_build_inputs
	run_build_container
	collect_build_output
}

main "$@"
