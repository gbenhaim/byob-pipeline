# Bring-your-own-build pipelines

A Tekton catalog for the build portion of [ADR 73](https://github.com/konflux-ci/architecture/pull/387). It checks out an exact source revision, runs a configured build command, and publishes the contents of `$BUILD_OUTPUT` as an OCI artifact. Trusted Tasks use the PoC Go CLI; the developer image only needs the build executable and its runtime.

## Presentation and local demo

Run `make presentation` and open <http://127.0.0.1:4173> for a seven-minute
interactive presentation for developers and managers. It uses the selected
ADR 73 deck, with diagrams, keyboard navigation, an overview, and speaker notes.

The [demo runbook](docs/presentation/README.md) and
[presenter script](docs/presentation/presenter-script.md) cover a real
Konflux-on-Kind run of `pipelines/byob.yaml`, Conforma integration testing,
and `unpack-build-output` in a gated release Pipeline, including a rejected
artifact. Rehearse with `bash scripts/demo.sh all`; show the prepared runs in
Konflux UI using links from `bash scripts/demo.sh ui`. Replay retained results
offline with `bash scripts/demo.sh evidence STAGE`.

## Pipelines

- **`byob`** — checkout, build locally, package.
- **`byob-remote`** — build through the multi-platform controller, locally or
  on a remote VM.
- **`byob-matrix`** — build once per platform and package outputs under
  platform directories such as `linux-amd64/`.
- **`byob-chained`** — run `scripts/generate.sh`, then `build.sh`, passing the
  first step’s outputs to the next.

## Build contract

Commit an executable `build.sh` that writes at least one regular file to
`$BUILD_OUTPUT`. The task sets that variable to an empty directory outside the
checkout, `/var/byob/output`. The default `build-cmd` is `["./build.sh"]`; it
selects the executable and any fixed leading arguments. Set the separate
`build-args` array on the PipelineTask to append arguments to that command.
Each array element is passed as one argv entry, directly from the restored
checkout at `/var/source`, without shell parsing. The selected image must
contain the command and its runtime dependencies. Builds may fetch dependencies.

For example, set `build-args` on the `build` PipelineTask like this:

```yaml
- name: build-args
  value:
  - --target
  - release
```

Only validated files from `$BUILD_OUTPUT` are exported; source files and caches
are not. A directory named `out/` in the repository stays in the checkout and
is not published. Chained builds receive earlier outputs at
`$BUILD_INPUTS/<task>/out/`. The pipeline author labels each input with the
producing task name, for example `codegen=$(tasks.codegen.results.BUILD_ARTIFACT)`.
A chained build must copy any files it wants to retain into its own
`$BUILD_OUTPUT`. `pack` selects the archive layout from its artifact references.
One bare reference publishes files at the archive root. Several bare references
use each artifact's platform marker as its directory. A
`task=$(tasks.task.results.BUILD_ARTIFACT)` entry publishes that task's files
under `task/`. The default OCI artifact type is
`application/vnd.konflux.build-output.v1`.

## Unpack

`pack` publishes one OCI artifact per PipelineRun. The Snapshot records that
artifact's manifest digest. The artifact type is
`application/vnd.konflux.build-output.v1`, and the only layer is
`payload.tar.gz` (`application/vnd.oci.image.layer.v1.tar+gzip`). A flat
archive extracts as files in the destination directory. An archive built from
several platforms or tasks keeps its directories, such as `linux-amd64/` or
`build/`.

A release Task restores the artifact with the `unpack-build-output` StepAction.
The image reference is the manifest digest, `registry/repo@sha256:<digest>`.
`artifactType` defaults to `application/vnd.konflux.build-output.v1`. The step
requires that artifact type, the layer name `payload.tar.gz`, and the layer
media type `application/vnd.oci.image.layer.v1.tar+gzip`.

```yaml
stepTemplate:
  volumeMounts:
  - name: output
    mountPath: /var/workdir/output
  - name: registry-auth
    mountPath: /var/run/byob-registry-auth
    readOnly: true
  - name: trusted-ca
    mountPath: /etc/pki/tls/certs/ca-custom-bundle.crt
    subPath: ca-bundle.crt
    readOnly: true
steps:
- name: unpack
  ref:
    name: unpack-build-output
  params:
  - name: image
    value: registry.example/org/component@sha256:DIGEST
  - name: destination
    value: /var/workdir/output
```

The destination is an empty directory writable by UID 1000. The StepAction runs
as that user, with privilege escalation disabled and all Linux capabilities
dropped. Declare the volumes on the Task and mount them from the step
template: the destination, plus `registry-auth` (`config.json`) and
`trusted-ca` (`ca-bundle.crt`) when the registry needs them. Tekton applies
that template after it inlines the StepAction. Install
[unpack-build-output.yaml](stepactions/unpack-build-output/0.1/unpack-build-output.yaml)
in the Task namespace, or resolve that file with a StepAction resolver. A
Pipeline references a Task, so a release Pipeline keeps its own Task and uses
the unpacked files.

## Try it

Requires Tekton Pipelines 1.16+, beta API fields enabled for matrix and
step-when support, the bundles resolver, and registry access. Remote builds also
require the multi-platform controller, ephemeral VMs with rootless Podman, and
controller mappings for the requested platforms. Configure remote Podman with
`--default-mounts-file` to disable automatic host-secret mounts; developer images
must be pullable without forwarded registry credentials. The controller must
clean up VMs after completion or cancellation.

The Tasks and the unpack StepAction use `localhost/byob-cli:devel` for
development. Build it with `make image`, push it to a registry reachable by
the cluster, replace its reference in `task/run-build/0.1/run-build.yaml`,
`task/pack/0.1/pack.yaml`, and
`stepactions/unpack-build-output/0.1/unpack-build-output.yaml` with the
published digest, then run `make generate` to refresh the generated remote
Task.

Choose an existing test namespace and install the example service accounts,
Tasks, and Pipelines:

```sh
export BYOB_CONTEXT=YOUR_TEST_CONTEXT
export BYOB_NAMESPACE=YOUR_TEST_NAMESPACE

kubectl --context "$BYOB_CONTEXT" --namespace "$BYOB_NAMESPACE" \
  apply -f examples/platform/serviceaccounts.yaml
kubectl --context "$BYOB_CONTEXT" --namespace "$BYOB_NAMESPACE" \
  apply --recursive -f task/ -f pipelines/
mkdir -p dist
cp examples/pipelineruns/byob.yaml dist/run.yaml
```

Replace the placeholders in `dist/run.yaml` with your repository, reviewed
commit, registry destinations, and a unique output tag, then submit it:

```sh
kubectl --context "$BYOB_CONTEXT" --namespace "$BYOB_NAMESPACE" \
  create -f dist/run.yaml
```

Other PipelineRun examples are in [examples/pipelineruns](examples/pipelineruns/).
The matrix example uses `linux/amd64` and `linux/arm64`.

For a private HTTPS repository, create a `kubernetes.io/basic-auth` Secret in
the PipelineRun namespace. It must contain `username` and `password` keys; use
a read-only Git token as the password. For example, with credentials stored in
files outside the repository:

```sh
kubectl --context "$BYOB_CONTEXT" --namespace "$BYOB_NAMESPACE" \
  create secret generic git-source-credentials \
  --type=kubernetes.io/basic-auth \
  --from-file=username=/secure/path/git-username \
  --from-file=password=/secure/path/read-only-git-token
```

Copy [byob-private.yaml](examples/pipelineruns/byob-private.yaml), fill its
placeholders, and submit it in the same namespace. It binds the Secret to the
optional `git-auth` workspace. For a private Pipelines-as-Code run, add the
same `git-auth` workspace binding to the PipelineRun and ensure the Secret
exists in that PipelineRun's namespace. Do not commit Secret data or credential
files.

## Tests

### Catalog checks

`make check` does not use a cluster. It needs Go 1.27, ShellCheck, Bash, and
`jq` (for the demo manifest renderer tests). It runs
the Go tests, `gofmt`, `go vet`, ShellCheck, a check that the generated remote
Task matches its source, and Tekton admission validation of the Tasks,
StepAction, Pipelines, PipelineRun examples, and the policy Pipeline.

### System tests

`make system-test` runs `byob`, `byob-chained`, `byob-remote`, `byob-matrix`,
and the Conforma integration scenario. The `byob` and `byob-matrix` scenarios
also run the `unpack-build-output` StepAction on the published manifest and
check the unpacked files. The suite is the module `tests/system`, so it is not
part of `make check`. It never installs or deletes the Kind cluster or
Konflux.

Prerequisites:

- Go 1.27.
- Rootless Podman. The target builds `localhost/byob-cli:devel` and loads it
  into Kind.
- `kind` and `kubectl`. When Kind uses Podman, export
  `KIND_EXPERIMENTAL_PROVIDER=podman`.
- A Ready Konflux Kind cluster with Tekton Pipelines, the bundles resolver,
  Chains, and the integration service.
- The Kind registry Service `registry-service` in namespace `kind-registry`,
  and the TLS Secret `kind-registry/local-registry-tls`.
- When that registry requires authentication, the Secret
  `kind-registry/internal-registry-client-auth`. The suite copies it to
  `byob-registry-auth` and attaches it to `byob-build`.
- A public Git branch that is already pushed. Its commit must contain an
  executable root `build.sh` that writes its deliverables to `$BUILD_OUTPUT`.
  The chained run also needs an executable `scripts/generate.sh` that writes
  `$BUILD_OUTPUT/generated.txt`. The build copies
  `$BUILD_INPUTS/codegen/out/generated.txt` into its own output.
  `examples/component` and
  `https://github.com/gbenhaim/byob-system-fixture` follow that contract.

The multi-platform controller is optional. The remote run requests
`linux/arm64`. The matrix run requests `linux/amd64` and `linux/arm64`. When
the controller is ready, it creates the SSH secret and chooses the host. When
it is not installed, the test creates the localhost secret the controller
would create for a local platform, and the build runs in the cluster. The
matrix archive then contains `linux-amd64/hello.txt` and
`linux-arm64/hello.txt`. No Git credentials are created. Runs publish to
`registry-service.kind-registry`, the name on the Kind registry certificate.

```sh
export KIND_EXPERIMENTAL_PROVIDER=podman
export BYOB_CLUSTER=existing
export BYOB_KIND_CLUSTER=konflux
export BYOB_NAMESPACE=YOUR_EXISTING_NAMESPACE
export BYOB_GIT_URL=https://github.com/gbenhaim/byob-system-fixture
export BYOB_GIT_REF=main
make system-test
```

- `BYOB_CLUSTER` defaults to `existing`. `install` does not install a cluster.
  It prints the one-time Kind setup and exits.
- `BYOB_KIND_CLUSTER` is required when more than one Kind cluster exists.
- `BYOB_KUBE_CONTEXT` selects a context inside that cluster's kubeconfig.
- `BYOB_NAMESPACE` uses an existing namespace and does not delete it. Without
  it, the suite creates and deletes `byob-system`.
- `BYOB_SYSTEM_KEEP=1` leaves a suite-created namespace in place.
- `BYOB_APPLICATION` defaults to `sample-component`. PipelineRuns set
  `appstudio.openshift.io/application` and
  `appstudio.openshift.io/component` to that value.
- `BYOB_GIT_URL` defaults to `https://github.com/gbenhaim/byob-pipeline`.
- `BYOB_GIT_REF` defaults to the current local branch. That branch must exist
  on the remote.

The policy specs install Application `byob-slsa`, IntegrationTestScenario
`byob-pipeline-policy`, and ConfigMap `byob-pipeline-policy` from
[policy](policy/). One build overrides task resource requests and limits and
must pass. A build that changes the pack artifact type must fail with
`policy.artifact_pack`. A build whose `SOURCE_ARTIFACT` is a trusted artifact
from another run, rather than this run's checkout or a previous build task,
must fail with `policy.artifact_source`. Each scenario run records
`TEST_OUTPUT`.

From a `konflux-ci` checkout, the one-time Kind setup is:

```sh
export KIND_EXPERIMENTAL_PROVIDER=podman
export KIND_MEMORY_GB=16
SKIP_SECRETS=true ./scripts/deploy-local.sh
```

Then rerun `make system-test` with `BYOB_CLUSTER=existing`.

## Publish for Pipelines-as-Code

Push the Task bundles and use the digests in
[examples/pac/push.yaml](examples/pac/push.yaml):

```sh
BYOB_CATALOG=quay.io/YOUR_ORG/byob
BYOB_TAG=REPLACE_WITH_UNIQUE_CATALOG_TAG
for byob_task in run-build run-build-remote pack; do
  tkn bundle push "$BYOB_CATALOG/$byob_task:$BYOB_TAG" \
    -f "task/$byob_task/0.1/$byob_task.yaml"
done
```

Copy the example to the component repository as `.tekton/push.yaml`, fill its
placeholders, and commit it. It embeds the matrix Pipeline and resolves Tasks
from bundles. Update the two bundle digest placeholders with the digests for
`run-build-remote` and `pack`. Use a unique output tag for each run;
`oci-storage` must be a repository without a tag.

## Required platform integration

The catalog supplies build workflow controls that support a SLSA Build L3
deployment. It does not establish SLSA Build L3 or SLSA Source L4 by itself;
those claims depend on the cluster, Chains, SCM, and Conforma assumptions below.

### Pipeline contribution

- Checkout is requested at an exact source commit and exposes the repository
  URL and commit for provenance. Trusted build Tasks run the developer command
  as a non-root user, without privilege escalation or Linux capabilities.
- The source checkout, build workspace, script output, and exported output are
  kept in separate volumes. Only validated files from `$BUILD_OUTPUT` are
  exported. Chained and matrix
  handoffs contain build outputs and platform metadata, not the source tree or
  caches. `pack` publishes the build outputs as the final OCI artifact.
- These controls provide inputs for provenance and policy evaluation. The
  pipeline itself does not generate provenance, sign it, or enforce source
  review policy.

### Cluster and Chains assumptions

- The cluster runs Tekton, the multi-platform controller, and Chains, and owns
  service accounts, registry credentials, VM allocation, and cleanup. Disable
  token automount for build accounts; do not grant build workloads permission
  to change TaskRun or PipelineRun status or read Chains signing keys. Keep
  registry credentials out of developer build steps. For private Git checkout,
  no credentialed ServiceAccount or TaskRun mutator is needed: bind the
  namespace Secret through the PipelineRun's optional `git-auth` workspace.
  The Pipelines map it only to checkout's `basic-auth` workspace; build Tasks
  and remote VMs do not receive it.
- Each build gets a clean, dedicated execution environment. For remote builds,
  the controller provisions a fresh VM per TaskRun and removes it after success,
  failure, or cancellation. The VM uses rootless Podman with automatic host
  secret mounts disabled, and developer images are pulled without forwarding
  registry credentials. If a matrix member uses the controller's `localhost`
  path, the local execution path must provide equivalent isolation; mixed local
  and remote matrices are suitable only when both paths meet this boundary.
- This README assumes Chains runs in the pipeline cluster and generates and
  signs provenance for the build. Configure it to bind the final OCI artifact
  digest to the exact source URL and commit, approved Pipeline and Task
  identities, builder image identities, and the Trusted Artifact input/output
  chain. For remote builds, include the builder image identity recorded by the
  trusted remote Task. For matrix builds, include and verify the child TaskRuns
  and their relationship to the packaged output. Keep signing keys outside
  build workloads.

### SCM assumptions for Source L4

Source L4 comes from SCM controls, not from these Tasks. The source repository
must preserve verifiable history and protect the reviewed branch or tag. Require
two distinct reviewers for changes, prevent self-approval, and produce a signed
Source Verification Attestation (Source VSA) for the exact commit the pipeline
checks out. The pipeline reports that commit; it does not prove that the commit
was reviewed under these rules.

### Conforma policy assumptions

[policy](policy/) checks the Chains provenance of a snapshot image. It
rejects a build whose recorded tasks, revision, builder, or artifact chain
drop the controls below. A build input must be the checkout source artifact
or a `BUILD_ARTIFACT` produced by `run-build` or `run-build-remote` in that
same attestation. Resource requests and limits may be overridden.
[examples/integration](examples/integration/) runs that policy from a Konflux
integration test scenario.

The scenario Pipeline records `TEST_OUTPUT` in the same shape as the upstream
[enterprise-contract Pipeline](https://github.com/conforma/tekton-catalog/blob/main/pipelines/enterprise-contract/0.1/enterprise-contract.yaml).
`ec validate image` checks the snapshot image and its attestation with the
Chains public key in Secret `chains-public-key`, key `cosign.pub`. Copy that
key from the Chains public key; do not mount the signing private key. Rekor is
ignored because Chains on this cluster does not write transparency entries.
The attestation subject must be the snapshot digest. Unsigned envelopes,
untrusted signatures, and an attestation for a different image fail before a
successful summary is written. The summary contains `timestamp`, `namespace`,
`successes`, `failures`, `warnings`, and `result` (`SUCCESS`, `WARNING`, or
`FAILURE`). The task prints that summary and the rule report, then fails
unless `result` is `SUCCESS` or `WARNING`.

Use the cluster's admission integration to evaluate policy before Task Pods
start. It must allow only approved, immutable Task bundle references and
platform-approved build literals, including the source revision, developer
image, build command, build arguments, and output settings. Reject inline Tasks
(`pipelineSpec.tasks[].taskSpec`) and runtime execution overrides that replace
a step or add a pod template. Resource requests and limits may be set in
`taskRunSpecs`.
For private checkout, allow only approved Git repository URLs and Secret
references bound to `git-auth`. This is where the platform prevents a
PipelineRun from changing the reviewed execution plan or exposing credentials
to Tasks other than checkout.

The integration scenario evaluates that signed Chains provenance. A Source
VSA is still a separate SCM control. Require it to match the source commit in
the provenance. A successful PipelineRun alone is not evidence that these
checks passed.

See [examples/platform](examples/platform/) for the service-account example and
[examples/pac](examples/pac/) for a PaC PipelineRun.

## Development

Use Go 1.27 and ShellCheck. `make check` is described under
[Tests](#tests). `make image` builds the local CLI image and requires rootless
Podman.

Edit the authored Tasks, Pipelines, or generator as needed.
`task/run-build-remote/0.1/run-build-remote.yaml` is generated; run
`make generate` after changing its source Task or generator.
