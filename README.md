# Bring-your-own-build pipelines

A Tekton catalog implementing the pipeline portion of [ADR 73](https://github.com/konflux-ci/architecture/pull/387).
Commit an executable `build.sh` that writes deliverables into `out/`. The pipeline
checks out the reviewed source, executes the script, and packages its output as an
OCI artifact with an SPDX SBOM. The default builder is digest-pinned Konflux
`task-runner`; another image and script path can be literals in the committed
PipelineRun.

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
source artifact after the build. All matrix builds must succeed before packaging.

## Task contract

- `run-build` takes `image`, `script-path`, `SOURCE_ARTIFACT`, and platform-owned
  `ociStorage`/`ociArtifactExpiresAfter`. It emits `SOURCE_ARTIFACT` for the entire
  modified tree. `run-build-remote` additionally takes `PLATFORM`.
- The script path is relative to the source root, executable, and has a shebang.
  Images must provide `/bin/sh` and the script's interpreter. The image entrypoint
  is bypassed. Scripts run as UID 1000/GID 0 with dropped capabilities and no
  privilege escalation; their working directory is `/var/workdir/source`.
- Scripts may fetch dependencies. Absolute/traversing script paths, escaping
  symlinks, and special files are rejected. No cache or PVC is shared between runs.
- `pack` accepts an **array** named `SOURCE_ARTIFACT`. `layout: flat` requires one
  artifact; `layout: platform` combines matrix outputs. `layout` is platform-owned.
  Each source must contain nonempty `out/` with regular files and optional safe
  relative symlinks. Packaging never executes source files.
- The default OCI `artifactType` is `application/vnd.konflux.build-output.v1` with
  a deterministic `application/vnd.oci.image.layer.v1.tar+gzip` layer. Formats may
  set a different literal `artifactType` in the committed PipelineRun.
- Only `pack` emits `IMAGE_URL`/`IMAGE_DIGEST`. Its `SBOM_BLOB_URL` identifies the
  attached SPDX JSON blob. Pipeline source identity comes directly from checkout.

## Install and render

Requires Tekton Pipelines 1.16+ with matrix/step-when support (`enable-api-fields:
beta`), the bundles resolver, OCI registry access, and the platform setup below.
Remote platforms additionally require the multi-platform controller and rootless
Podman on the allocated VM, including its `--default-mounts-file` global option
(used to disable automatic host-secret mounts). Developer images must be publicly pullable on the VM;
credentials are not forwarded there.

For development, install the three Tasks from `task/*/0.1/*.yaml` and the desired
definition from `pipelines/`. The checkout already references the upstream
digest-pinned `git-clone-oci-ta` bundle. The other Task references in these
development Pipelines are local names.

For committed PaC runs, publish to a registry you control and render immutable
Task references:

```sh
python3 -m pip install -r requirements-dev.txt
make check
# These commands publish only when you explicitly run them:
python3 hack/publish-bundles.py --registry-prefix quay.io/MY_ORG/byob \
  --output dist/bundles.yaml
python3 hack/render-run.py --pipeline byob-matrix --bundles dist/bundles.yaml \
  --output-image quay.io/MY_ORG/component:BUILD_TAG \
  --oci-storage quay.io/MY_ORG/component-ta \
  --platform linux/amd64 --platform linux/arm64 > dist/push.yaml
```

Run the rendering command from this catalog and copy `dist/push.yaml` to the
component's `.tekton/push.yaml`. Use a unique output tag per build (PaC template variables can be
provided as literal CLI arguments). `oci-storage` must be a repository without a
tag; the Tasks add unique per-TaskRun tags. Build image, script, platform list,
artifact wiring, and artifact type are committed literals, not run-time override
parameters. Publication is not signing or trusted-catalog admission.

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
  array inputs, and require ADR-55 Source VSA evidence. Admission must enforce the
  reviewed literals and reject inline Tasks and execution overrides. New bundles
  require the normal signing/review process before that policy can trust them.

## Development and tests

Edit `hack/render-catalog.py`, `scripts/`, or `task-generator/remote/`; then run
`make generate`. Generated YAML is self-contained and committed. `make check`
runs Python behavior tests, Go generator tests, ShellCheck, and generation drift
checks, plus validation against Tekton 1.16's actual admission types. Go 1.26.4+,
Python 3.11+, PyYAML, and ShellCheck are needed (the generator itself needs Go 1.23+).

`make smoke` uses rootless Podman and a temporary local registry to exercise the
real Trusted Artifact tools and output packaging. It pulls the pinned images and
does not contact a Kubernetes cluster. A complete cluster check must additionally
run the local and mixed-platform PipelineRuns in an explicitly selected test
namespace, check their signed results, and verify VM cleanup after failure and
cancellation; no configured cluster is used implicitly by these tools.

`hack/test-cluster.py` submits and waits for a selected scenario, requiring explicit
`--context`, `--namespace`, `--git-url`, `--revision`, `--output-image`,
`--oci-storage`, and `--bundles` arguments. Choose `--pipeline byob-matrix` and
repeat `--platform` to exercise a mixed controller configuration. The namespace
must already have the documented platform prerequisites. Runs are retained for
debugging; the helper verifies build results but does not certify Chains or release
policy behavior.
