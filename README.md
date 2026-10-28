# Bring-your-own-build pipelines

A Tekton catalog implementing the pipeline portion of [ADR 73](https://github.com/konflux-ci/architecture/pull/387).
Commit an executable `build.sh` that writes deliverables into `out/`. The pipeline
checks out the reviewed source, executes the script, and packages its output as an
OCI artifact with an SPDX SBOM. The default builder is digest-pinned Konflux
`task-runner`; another image and build command can be literals in the committed
PipelineRun.

Trusted Task logic lives in the PoC `byob` Go CLI. The Tasks call its subcommands
for validation, input restoration, packaging, publication, and remote execution.

## Pipelines

| Pipeline | Behavior |
| --- | --- |
| `byob` | Checkout → local `run-build` → `pack`. |
| `byob-remote` | Checkout → generated `run-build-remote` → `pack`; the controller can select localhost. |
| `byob-matrix` | Checkout once → one build per platform → one artifact containing platform directories. |
| `byob-chained` | Checkout → `scripts/generate.sh` → `build.sh` → `pack`. |

The matrix example uses literal `linux/amd64` and `linux/arm64` values. Configure
the multi-platform controller's `local-platforms` and `dynamic-platforms` to map
them to in-cluster execution and dedicated remote VMs respectively. `localhost`
is supported, without SSH or Podman in the local path. Mixed local/remote matrix
runs produce `linux-amd64/` and `linux-arm64/` directories in the final archive.
Result order is immaterial: trusted export code records the platform in each
build artifact after the build. All matrix builds must succeed before packaging.

## Task contract

- `run-build` takes `image`, `build-cmd`, checkout's `SOURCE_ARTIFACT`, and
  platform-owned `ociStorage`/`ociArtifactExpiresAfter`. It emits `BUILD_ARTIFACT`
  containing only `out/`, plus platform metadata for matrix builds. The source
  tree, build scripts, and caches outside `out/` are never re-exported.
  `run-build-remote` additionally takes `PLATFORM`.
- A chained step restores the original `SOURCE_ARTIFACT` and accepts preceding
  outputs through the optional `BUILD_ARTIFACTS` array (default: `[]`). Each input
  is restored separately at `$BUILD_INPUTS/<index>/out/` in array order, starting
  at zero, on local and remote builders. For example, the first input is at
  `/var/workdir/inputs/0/out/`. Each step writes its own deliverables to `out/`;
  it must copy any preceding outputs it wants to retain. The chained example
  demonstrates this. The output result remains the string `BUILD_ARTIFACT`.
- `build-cmd` is an argument array, defaulting to `["./build.sh"]`. For example,
  `["make", "build"]` or `["/usr/local/bin/builder", "--release"]` executes directly
  from `/var/workdir/source`. Arguments are literal, without shell parsing.
  The developer image needs the selected executable and its own runtime dependencies;
  neither Bash nor `sh` is required. The image's entrypoint and default command are
  overridden. Builds run as UID 1000/GID 0 with dropped capabilities and no
  privilege escalation, with writable `HOME=/var/workdir/home`.
- A static launcher is staged in a separate volume and mounted read-only by the
  local developer step. It sets the working directory after source restoration
  and replaces itself with the build executable. Remote Podman executes the same
  argument array directly. Source preparation validates the source tree; it does
  not require the build executable to be a repository script.
- Builds may fetch dependencies. Escaping source/output symlinks and special
  files are rejected. No cache or PVC is shared between runs.
- After the script finishes, `validate-output` checks only `out/` and copies it
  into a separate volume mounted only by trusted validation/export steps. Output
  must be a real directory with at least one regular file; symlinks must stay
  within `out/`. The remote launcher returns only `out/` from the VM.
- `pack` accepts an **array** named `BUILD_ARTIFACTS`. `layout: flat` requires one
  artifact; `layout: platform` combines matrix outputs. `layout` is platform-owned.
  Each build artifact contains nonempty `out/` with regular files and optional safe
  relative symlinks. Packaging never executes source files.
- The default OCI `artifactType` is `application/vnd.konflux.build-output.v1` with
  a deterministic `application/vnd.oci.image.layer.v1.tar+gzip` layer. Formats may
  set a different literal `artifactType` in the committed PipelineRun.
- Only `pack` emits `IMAGE_URL`/`IMAGE_DIGEST`. Its `SBOM_BLOB_URL` identifies the
  attached SPDX JSON blob. Pipeline source identity comes directly from checkout.

This output-only handoff is an intentional change from the ADR's original
full-source `SOURCE_ARTIFACT` chain. Checkout still publishes the reviewed source
once so the build Tasks can restore it. Every subsequent intermediate upload
contains only build outputs and optional platform metadata; the final package
contains the contents of `out/` without that metadata.

## Install and run

Requires Tekton Pipelines 1.16+ with matrix/step-when support (`enable-api-fields:
beta`), the bundles resolver, OCI registry access, and the platform setup below.
Remote platforms additionally require the multi-platform controller and rootless
Podman on the allocated VM, including its `--default-mounts-file` global option
(used to disable automatic host-secret mounts). Developer images must be publicly pullable on the VM;
credentials are not forwarded there.

The Tasks currently reference the local PoC image `localhost/byob-cli:devel`.
Build it with `make image`. Before installing on a cluster, push the image to your
registry, replace that reference in the two authored Tasks with the published
`repository@sha256:digest`, and run `make generate` to update the remote Task.
The local tag is for development; it is not a published catalog image.

The development Pipelines reference locally installed Tasks. Checkout already
uses the upstream digest-pinned `git-clone-oci-ta` bundle. After configuring the
platform prerequisites below, install the catalog in an explicitly chosen test
namespace:

```sh
BYOB_CONTEXT=YOUR_TEST_CONTEXT
BYOB_NAMESPACE=YOUR_TEST_NAMESPACE
kubectl --context "$BYOB_CONTEXT" --namespace "$BYOB_NAMESPACE" \
  apply --recursive -f task/ -f pipelines/
mkdir -p dist
cp examples/pipelineruns/byob.yaml dist/run.yaml
```

Edit `dist/run.yaml` to replace every `YOUR_*` and `REPLACE_WITH_*` value with your
repository, exact source commit, registry destinations, and a unique output tag.
`examples/pipelineruns/` also includes remote, matrix, and chained runs. Change
build images, command arrays, or platforms directly in the corresponding Pipeline
YAML before installing it. Submit and inspect the run with:

```sh
BYOB_RUN=$(kubectl --context "${BYOB_CONTEXT:?Set BYOB_CONTEXT}" \
  --namespace "${BYOB_NAMESPACE:?Set BYOB_NAMESPACE}" \
  create -f dist/run.yaml -o name)
kubectl --context "$BYOB_CONTEXT" --namespace "$BYOB_NAMESPACE" \
  wait --for=condition=Succeeded "$BYOB_RUN" --timeout=30m
kubectl --context "$BYOB_CONTEXT" --namespace "$BYOB_NAMESPACE" \
  get "$BYOB_RUN" -o yaml
```

The final status should contain `IMAGE_URL`, `IMAGE_DIGEST`, `SBOM_BLOB_URL`, and
`CHAINS-GIT_COMMIT`. Runs are retained for inspection. If a run fails or times
out, inspect its TaskRun status and logs. Matrix tests also require the controller
platform mapping described above.

## Publish bundles and configure PaC

Publish the Tasks using `tkn` and your registry credentials:

```sh
BYOB_CATALOG=quay.io/YOUR_ORG/byob
BYOB_TAG=REPLACE_WITH_UNIQUE_CATALOG_TAG
for byob_task in run-build run-build-remote pack; do
  tkn bundle push "$BYOB_CATALOG/$byob_task:$BYOB_TAG" \
    -f "task/$byob_task/0.1/$byob_task.yaml"
done
```

Copy [examples/pac/push.yaml](examples/pac/push.yaml) to the component repository's
`.tekton/push.yaml`. Replace `YOUR_*` and `REPLACE_WITH_*`, including the two bundle
digest placeholders with the actual digests reported by their respective pushes.
Keep `@sha256:` and use the 64 hexadecimal characters after it. The example embeds
the matrix Pipeline with bundle references for every Task; it requires no locally
installed Pipeline. For another scenario, copy its `spec` from `pipelines/` into
`pipelineSpec` and replace local Task names with the same bundle resolver structure.

Use a unique output tag per run. `oci-storage` must be a repository without a tag;
the Tasks add unique per-TaskRun tags. Build image, script, platform list, artifact
wiring, and artifact type are committed literals, not run-time override parameters.
Publication is not signing or trusted-catalog admission.

`examples/component` contains executable scripts for the simple and chained
pipelines. Repository-specific Konflux component/application labels and PaC branch
configuration should be set when onboarding the component.

## Required platform integration

These manifests are a candidate catalog, **not a standalone SLSA certification**.
Admission, release policy, signing, and controller deployment are outside this repo.

- Use platform-owned service accounts with token automount disabled and no build
  permission to patch TaskRun/PipelineRun status or read Chains signing keys.
  `examples/platform/serviceaccounts.yaml` shows the account boundary.
- Tekton's credential initializer mounts annotated service-account Secrets into
  every step. The build account must not carry those Secrets. New trusted transfer
  and publication steps explicitly mount `byob-registry-auth`, a
  `kubernetes.io/dockerconfigjson` Secret; developer steps do not.
- Unmodified upstream checkout uses ordinary Tekton credentials. Authenticated
  deployments therefore require a **platform-owned TaskRun mutator** that selects
  the credentialed checkout account only for its allowlisted bundle. Component
  authors must not supply `taskRunSpecs` or pod overrides to achieve this.
  Public Git plus a disposable anonymous registry can exercise development flows.
- Native Tekton steps inherit internal control/result mounts. Step-owned export
  results reduce result-file spoofing, but native step ordering is not a hostile
  container termination boundary. Validate the local platform isolation model
  before making Build L3 claims; these manifests do not solve that platform issue.
- Actual remote builds require per-TaskRun ephemeral VMs, cleaned up by the
  controller on completion/cancellation. Localhost uses the local isolation model.
  Supplied `known_hosts` data enables strict SSH host verification; otherwise the
  launcher uses an ephemeral accept-new host-key store, matching a trust-on-first-use
  deployment assumption that operators must assess.
- Chains automatically observes local developer step imageIDs. For remote Podman,
  the trusted launcher records `BUILD_IMAGE_ARTIFACT_INPUTS`. On localhost this
  result records the requested immutable input; the step imageID records execution.
  Enable Chains deep inspection for matrix child TaskRuns. Index pins and selected
  platform manifest digests may differ; policy must verify their relationship.
- The future Conforma policy must allow the actual checkout identity
  `git-clone-oci-ta`, the two BYOB Tasks, and `pack`, verify artifact chains including
  checkout's `SOURCE_ARTIFACT`, chained `BUILD_ARTIFACTS` inputs and `BUILD_ARTIFACT`
  results, and pack's array inputs, and require ADR-55 Source VSA evidence.
  Admission must enforce the
  reviewed literals and reject inline Tasks and execution overrides. New bundles
  require the normal signing/review process before that policy can trust them.

## Development and tests

Edit `task/run-build/0.1/run-build.yaml`, `task/pack/0.1/pack.yaml`, and `pipelines/`
directly. The Cobra CLI lives in `cmd/byob` and `internal/`; the Go remote generator
adds CLI invocations to the local Task. Run `make generate` after changing
`run-build` or the remote generator; only
`task/run-build-remote/0.1/run-build-remote.yaml` is generated.
Commit that output with its source changes. Update examples when changing their
corresponding Pipeline definitions.

```sh
make build
make generate
make check
make image
make smoke
```

Go 1.27 and ShellCheck are needed for `make check`; the image and smoke targets
also need rootless Podman. `make check` runs Go behavior, CLI, catalog and generator
tests, formatting/vet, ShellCheck for retained shell code, generated Task drift,
and Tekton 1.16 admission validation. There is no Python test dependency.

`Containerfile` builds with the digest-pinned Red Hat `hi/go:1.27` image and copies
the binary into the pinned Konflux task-runner image, which supplies SSH, tar,
ORAS, Syft, and Cosign. It also copies the upstream Trusted Artifacts restoration
helpers so repository-specific registry authentication and retries stay upstream.
The remote VM worker is a small Bash/Podman script embedded in the CLI; Bash is
required on the VM, not in the developer image. No CLI installation is required
in developer images or on VMs. `catalog-lock.yaml` records inspected upstream
versions; it does not generate or override manifest pins.

`make smoke` runs the Go integration test in `tests/smoke`, using the locally built
CLI image, rootless Podman, and a temporary registry to exercise real Trusted
Artifact tools, output packaging, and remote execution through a local SSH
transport fixture. It pulls the pinned upstream images and does not contact a
Kubernetes cluster. A separate native fixture image contains no shell and checks
local/remote argument handling, writable HOME, and overriding the image's default
command. A complete cluster check must additionally
run the local and mixed-platform PipelineRuns in an explicitly selected test
namespace, check their signed results, and verify VM cleanup after failure and
cancellation; no configured cluster is used implicitly by these tools.

Use the development examples and commands under [Install and run](#install-and-run)
for cluster testing. Use `byob-matrix.yaml` with a mixed controller configuration
to exercise local and remote execution together. Inspect signed results and VM
cleanup separately; successful PipelineRun completion does not certify Chains or
release-policy behavior.
