# BYOB remote Task generator

Adapted from [container-build-catalog's remote generator](https://github.com/konflux-ci/container-build-catalog/tree/85e966730ae50688b7f37c98cce47c3de5bdeb33/task-generator/remote), licensed under Apache-2.0.

`make generate` produces `run-build-remote` from the local Task. Do not edit the
generated YAML. The generator retains the upstream Task transformation approach,
controller Secret naming, OTP protocol, and localhost behavior. It uses a smaller
YAML-only Go dependency rather than the Kubernetes client stack.

The upstream generator assumes a trusted Buildah image and a step named `build`.
BYOB's build image is untrusted, so the generated Task has three distinct steps:

1. A trusted selector reads the controller's `host` and emits `local` or `remote`.
2. The original developer-image step runs only for `localhost`, without the SSH
   Secret or registry-auth volume.
3. A trusted SSH step runs only for a remote host, using rootless Podman and
   `podman cp` for source transfer. It has no privileged or host-volume option.

The generator also wires platform metadata and the remote builder input result.
It intentionally omits upstream Buildah-specific image tagging, GPU flags,
automatic credential forwarding, and shared-volume assumptions.

Manual invocation:

```sh
cd task-generator/remote
go run . --task ../../task/run-build/0.1/run-build.yaml \
  --remote-task ../../task/run-build-remote/0.1/run-build-remote.yaml \
  --scripts ../../scripts
```
