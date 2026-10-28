# BYOB remote Task generator

Adapted from [container-build-catalog's remote generator](https://github.com/konflux-ci/container-build-catalog/tree/85e966730ae50688b7f37c98cce47c3de5bdeb33/task-generator/remote), licensed under Apache-2.0.

`make generate` produces only `run-build-remote` from the directly maintained
local Task by adding `byob` CLI commands. Do not edit the
generated YAML. The generator retains the upstream Task transformation approach,
controller Secret naming, OTP protocol, and localhost behavior. It uses a smaller
YAML-only Go dependency rather than the Kubernetes client stack. The generator
shares the root Go 1.27 module with the PoC CLI.

The upstream generator assumes a trusted Buildah image and a step named `build`.
BYOB's build image is untrusted, so the generated Task has three distinct steps:

1. A trusted selector reads the controller's `host` and emits `local` or `remote`.
2. The original developer-image step runs only for `localhost`, without the SSH
   Secret or registry-auth volume. It runs `build-cmd` directly from the restored
   checkout; the developer image does not need the BYOB CLI.
3. A trusted SSH step runs only for a remote host, using rootless Podman and
   `podman cp` to transfer the original source and preceding build outputs. It
   returns only `out/`, preserving the local checkout. It has no privileged or
   host-volume option.

Both paths consume `build-cmd` and `build-args` arrays. The build arguments
follow the command's fixed arguments and remain separate argv entries. The VM
worker sets Podman's entrypoint to the executable and passes the remaining
arguments directly, without requiring a shell in the developer image.

The generator also wires platform metadata and the remote builder input result.
Selection, SSH/OTP orchestration, and image recording live in `internal/remote`.
Only the VM's Bash/Podman worker is embedded as shell code in the CLI.
The local Task's private output staging volume and `BUILD_ARTIFACT` export are
retained. Both local and remote scripts read the `BUILD_ARTIFACTS` array inputs at
`$BUILD_INPUTS/<index>/out/`, starting at index zero.
It intentionally omits upstream Buildah-specific image tagging, GPU flags,
automatic credential forwarding, and shared-volume assumptions.

Manual invocation:

```sh
go run ./task-generator/remote --task task/run-build/0.1/run-build.yaml \
  --remote-task task/run-build-remote/0.1/run-build-remote.yaml
```

`make check-generated` regenerates into a temporary file and compares it with the
committed remote Task, without modifying either the local or remote manifest.
