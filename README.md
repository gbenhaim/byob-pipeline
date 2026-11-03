# Bring-your-own-build pipelines

A Tekton catalog for the build portion of [ADR 73](https://github.com/konflux-ci/architecture/pull/387). It checks out an exact source revision, runs a configured build command, and publishes the contents of `out/` as an OCI artifact. Trusted Tasks use the PoC Go CLI; the developer image only needs the build executable and its runtime.

## Pipelines

- **`byob`** — checkout, build locally, package.
- **`byob-remote`** — build through the multi-platform controller, locally or
  on a remote VM.
- **`byob-matrix`** — build once per platform and package outputs under
  platform directories such as `linux-amd64/`.
- **`byob-chained`** — run `scripts/generate.sh`, then `build.sh`, passing the
  first step’s outputs to the next.

## Build contract

Commit an executable `build.sh` that writes at least one regular file to `out/`.
The default `build-cmd` is `["./build.sh"]`; it selects the executable and any
fixed leading arguments. Set the separate `build-args` array on the
PipelineTask to append arguments to that command. Each array element is passed
as one argv entry, directly from the restored checkout at `/var/source`,
without shell parsing. The selected image must contain the command and its
runtime dependencies. Builds may fetch dependencies.

For example, set `build-args` on the `build` PipelineTask like this:

```yaml
- name: build-args
  value:
  - --target
  - release
```

Only validated files from `out/` are exported; source files and caches are not.
Chained builds receive earlier outputs at `$BUILD_INPUTS/<index>/out/` and must
copy any files they want to retain into their own `out/`. Packaging supports a
flat layout for one build or a platform layout for matrix outputs. The default
OCI artifact type is `application/vnd.konflux.build-output.v1`.

## Try it

Requires Tekton Pipelines 1.16+, beta API fields enabled for matrix and
step-when support, the bundles resolver, and registry access. Remote builds also
require the multi-platform controller, ephemeral VMs with rootless Podman, and
controller mappings for the requested platforms. Configure remote Podman with
`--default-mounts-file` to disable automatic host-secret mounts; developer images
must be pullable without forwarded registry credentials. The controller must
clean up VMs after completion or cancellation.

The Tasks use `localhost/byob-cli:devel` for development. Build it with
`make image`, push it to a registry reachable by the cluster, replace its
reference in `task/run-build/0.1/run-build.yaml` and
`task/pack/0.1/pack.yaml` with the published digest, then run `make generate`
to refresh the generated remote Task.

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

## System test

`make system-test` runs the `byob` pipeline on a Kind cluster that already has
Konflux installed. The suite is the separate module `tests/system`, so it is
not part of `make check`. The default
`BYOB_CLUSTER=existing` never installs or deletes the cluster. When more than
one Kind cluster exists, set `BYOB_KIND_CLUSTER` to the cluster name.
`BYOB_KUBE_CONTEXT` selects a context inside that cluster's kubeconfig.
`BYOB_NAMESPACE` runs in an existing namespace and does not delete it. Without
that variable the suite creates and deletes `byob-system`. Each PipelineRun is
labeled `appstudio.openshift.io/application` and
`appstudio.openshift.io/component` with `BYOB_APPLICATION`, which defaults to
`sample-component`.

The test checks out a public GitHub commit and publishes to the cluster's
internal registry at `registry-service.kind-registry`, the name on the Kind
registry certificate. `BYOB_GIT_URL` defaults to
`https://github.com/gbenhaim/byob-pipeline`. `BYOB_GIT_REF` defaults to the
current local branch. That ref must already be pushed, and the commit must
contain an executable root `build.sh`. The chained run also needs an executable
`scripts/generate.sh` whose `out/generated.txt` is copied into the build
output. The remote run requests `linux/arm64`. The matrix run requests `linux/amd64`
and `linux/arm64`, and the published archive contains `linux-amd64/hello.txt`
and `linux-arm64/hello.txt`. When the multi-platform controller is ready, that
controller creates the SSH secret and chooses the host. When it is not
installed, the test creates the localhost secret the controller would create
for a local platform, and the build runs in the cluster. The suite also
creates an integration test scenario that runs the Conforma policy in
[policy](policy/) against the Chains attestation of a build. One build
overrides task resource requests and limits and must pass. A build that
changes the pack artifact type must fail. A build whose `SOURCE_ARTIFACT` is
a trusted artifact from another run, rather than this run's checkout or a
previous build task, must fail. No Git
credentials are created. When
`kind-registry/internal-registry-client-auth` exists, the test copies it to
`byob-registry-auth` and attaches it to `byob-build` so Trusted Artifacts can
push. Set `BYOB_SYSTEM_KEEP=1` to leave the suite-created namespace after the run.

`BYOB_CLUSTER=install` does not install anything. It prints the one-time Kind
setup and exits. From a `konflux-ci` checkout, set `KIND_MEMORY_GB=16` and run:

```sh
export KIND_EXPERIMENTAL_PROVIDER=podman
SKIP_SECRETS=true ./scripts/deploy-local.sh
```

Then rerun `make system-test`.

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
- The source checkout, build workspace, and exported output are kept in separate
  volumes. Only validated files from `out/` are exported. Chained and matrix
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

Use Go 1.27 and ShellCheck. `make check` runs Go tests, lint and formatting
checks, generated-Task drift checks, and Tekton manifest validation. `make image`
builds the local CLI image and requires rootless Podman.

Edit the authored Tasks, Pipelines, or generator as needed.
`task/run-build-remote/0.1/run-build-remote.yaml` is generated; run
`make generate` after changing its source Task or generator.
