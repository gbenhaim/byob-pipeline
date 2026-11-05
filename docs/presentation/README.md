# BYOB: seven-minute presentation and Kind demo

For developers and managers. The talk connects
[ADR 73](https://github.com/konflux-ci/architecture/pull/387) to this repository,
the real Conforma IntegrationTestScenario, and release-time consumption using
`unpack-build-output`.

## Open the presentation

From the repository root, with Node.js 20+:

```sh
make presentation
```

Open <http://127.0.0.1:4173>. Use arrow keys to move, `O` for the overview,
`N` for notes and sources, `F` for fullscreen, and `P` to print all seven
slides. Each slide has a numbered URL, such as `/#5` for Conforma. If the
port is busy, use `PORT=4174 make presentation`.

The deck is the user-selected `0073-bring-your-own-build-logic.html` from the
architecture PR worktree, copied unchanged to `docs/presentation/index.html`.
You can also open that file directly. Its logo, styles, JavaScript, diagrams,
and speaker notes are embedded, so the presentation works offline. External
source links need internet access. The website never accesses your cluster.

Read the [timed presenter script](presenter-script.md). The seven-minute slot
includes a **prepared walkthrough in the Konflux UI**, not cluster installation or
waiting for cold image pulls. A full live rerun takes longer and depends on
Git, registry, Chains, and controller availability.

## One-time platform setup

Use an existing **Konflux-on-Kind** cluster. A bare `kind create cluster` is
insufficient. Required platform services are Tekton 1.16+ with beta API fields
and the bundles/cluster resolvers, Chains, Application/Snapshot CRDs, and the
integration service. The release example is an ordinary Tekton Pipeline; it
does not deploy a ReleasePlan or publish to an external release destination.

If you do not have Konflux on Kind, run this **from a konflux-ci checkout**:

```sh
export KIND_EXPERIMENTAL_PROVIDER=podman
export KIND_MEMORY_GB=16
SKIP_SECRETS=true ./scripts/deploy-local.sh
```

Allow that deployment to finish before returning to this repository.

Local tools: Go 1.27, rootless Podman, `kind`, `kubectl`, `jq`, Git, Make,
Bash, and standard Unix tools. Ensure `kind` is on your `PATH`. Use the
Podman provider when that is how your Kind cluster was created.

The demo follows the existing system suite's platform conventions:

- Registry: Service `kind-registry/registry-service`, TLS Secret
  `kind-registry/local-registry-tls`, and optional auth Secret
  `kind-registry/internal-registry-client-auth`.
- Chains public key: Secret `openshift-pipelines/public-key`, key `cosign.pub`.
  Only that public key is copied into the demo namespace.
- Chains must use `in-toto` PipelineRun provenance, OCI storage,
  `artifacts.pipelinerun.enable-deep-inspection: "true"`, and OCI signatures.
  The existing scenario uses `--ignore-rekor` for local Chains.
- `trusted-ca` is extended with the local registry CA in the demo namespace
  and `tekton-pipelines`, preserving existing public roots. The script does
  not change Chains signing configuration or the current kubeconfig context.

The builder runs without registry credentials; trusted artifact-handling
steps receive the namespace registry credential. The demo uses public Git
and never creates Git credentials.

## Rehearse before presenting

Run from this repository, with a public component whose executable `build.sh`
writes `hello.txt` to `$BUILD_OUTPUT`. The default fixture already does that.

```sh
export KIND_EXPERIMENTAL_PROVIDER=podman
export BYOB_KIND_CLUSTER=konflux
export BYOB_NAMESPACE=byob-demo-presentation
export BYOB_GIT_URL=https://github.com/gbenhaim/byob-system-fixture
export BYOB_GIT_REF=main

bash scripts/demo.sh all
```

Equivalent Make entry point: `make demo STAGE=all`.

This command prepares the namespace, builds and loads the CLI image, runs the
real `byob` Pipeline, waits for Chains, submits a Snapshot that triggers the
existing IntegrationTestScenario, runs the release Pipeline, then proves the
negative case. It records nonsecret evidence in
`dist/demo/$BYOB_NAMESPACE/`. All waits are bounded; override the default
600 seconds per wait with `BYOB_DEMO_TIMEOUT`.

## Show the runs in Konflux UI

The current rehearsal runs in **`kind-konflux`**, namespace/workspace
**`byob-demo-presentation`**, Application **`byob-slsa`**. Open its
[PipelineRuns activity](https://localhost:9443/ns/byob-demo-presentation/applications/byob-slsa/activity/pipelineruns).
For the standard local Konflux deployment, sign in as `user1@konflux.dev`
with password `password`.

To connect an existing rehearsal and print direct links to its six recorded runs:

```sh
export BYOB_KIND_CLUSTER=konflux
export BYOB_NAMESPACE=byob-demo-presentation
bash scripts/demo.sh ui
```

`prepare` also configures UI visibility automatically. This adds the tenant
namespace label and a namespace-scoped `konflux-viewer-user-actions` binding
for `BYOB_UI_USER` (default `user1@konflux.dev`). The binding permits reading
applications, runs, and logs. It grants no Secret access or write permissions.
For a different login or UI address, set `BYOB_UI_USER` or `BYOB_UI_URL`
(default `https://localhost:9443`) before running `ui`.

Build and release PipelineRuns carry the application and `build`/`release`
type labels used by the UI. Integration-service runs already carry the
application and `test` labels. `ui` backfills only demo-owned build/release
metadata; it does not start new runs or modify their specs or results.

Before the talk, open the printed `good_build`, `good_test`, `good_release`,
`bad_test`, and `bad_release` links in separate tabs. Show:

1. **Build:** the checkout → build → pack graph; Results for `IMAGE_DIGEST`.
2. **Integration test:** the scenario's validation/report/assert steps;
   Results for `TEST_OUTPUT` with 18 successes and zero failures for the default
   fixture. Use Logs to show signature verification and Conforma details.
3. **Release:** verify → unpack; expand unpack and its `consume` step in Logs
   to show the extracted file list and the contents of `hello.txt` between
   filename markers (`Built on x86_64` for the default fixture).
4. **Rejection:** the failed test's report log containing `policy.artifact_pack`,
   then the failed release graph with unpack skipped.

Use **Activity → Pipeline runs** and its type filter to navigate all three
types together. The demo release appears here as a Tekton PipelineRun; it
does not create a Release resource in the separate Releases view. No Component
onboarding is needed for this application activity walkthrough.

Keep Konflux UI in a separate browser tab; `demo.sh ui` provides the exact
workspace/run URLs for your selected namespace. Slide 7 explains the release
consumer pattern. Switch to the UI after that slide for the prepared demo.

## Rehearsal settings

The namespace must be `byob-demo` or `byob-demo-SUFFIX`. If it already exists,
it must carry the demo ownership label. The script never deletes a namespace.
Use one namespace per presenter/rehearsal; run names and output tags are unique.
Do not run two demo commands concurrently in the same namespace.
An existing evidence directory is bound to its recorded Kind context; use a
new namespace suffix when switching clusters, including during preparation.

`BYOB_REVISION` optionally supplies an exact 40-character commit. Otherwise
the script resolves `BYOB_GIT_REF` on the public remote to an exact commit.
Local unpushed work is not checked out by the build.

To skip rebuilding/loading an image that is **already loaded into every Kind
node**, set `BYOB_DEMO_SKIP_IMAGE=1` for `prepare` or `all`. Rebuild after any
Go CLI change. This option is intended for repeated rehearsals.

## Run individual stages

```sh
bash scripts/demo.sh prepare
bash scripts/demo.sh build
bash scripts/demo.sh verify
bash scripts/demo.sh release
bash scripts/demo.sh reject
bash scripts/demo.sh ui
bash scripts/demo.sh status
```

`build` references the installed, unmodified `pipelines/byob.yaml`. Its
results include the source identity and final OCI manifest digest.

`verify` creates a real Snapshot for that digest, selected for Application
`byob-slsa`. The integration service resolves the existing
`byob-pipeline-policy` Pipeline and executes Conforma. The script requires
both a successful PipelineRun and `TEST_OUTPUT.result == SUCCESS`.

`release` runs [the demo release Pipeline](../../examples/demo/release.yaml).
The `verify` Task is generated from the existing integration validator,
including signature validation, report output, and the final assertion.
The dependent `unpack` Task calls the actual StepAction and reads `hello.txt`.
Both Tasks receive the same immutable manifest reference. Unpacking itself
does not perform signature verification.

`reject` installs a separate `byob-demo-rejected` Pipeline with an unsupported
pack artifact type. It does not edit the original `byob` Pipeline. The build
and signing must succeed, while integration must report `FAILURE` with
`policy.artifact_pack`. The variant's different Pipeline name can also
trigger `policy.pipeline_name`; this is visible in the report. Finally, the
release validation must fail with `policy.artifact_pack`, with **zero unpack
TaskRuns created**. An unrelated infrastructure failure does not count as
the expected rejection.

If the UI is unavailable, replay actual retained evidence in the terminal:

```sh
bash scripts/demo.sh evidence build
bash scripts/demo.sh evidence verify
bash scripts/demo.sh evidence release
bash scripts/demo.sh evidence reject
```

These commands read saved PipelineRun JSON and logs without a cluster or
network connection. Explain that they are retained results from your rehearsal.
The presentation's diagrams explain the architecture; the UI and retained
evidence show the actual rehearsal results.

## Inspect or troubleshoot

```sh
bash scripts/demo.sh status

kubectl --context "kind-$BYOB_KIND_CLUSTER" -n "$BYOB_NAMESPACE" \
  get pipelineruns,taskruns,pods

kubectl --context "kind-$BYOB_KIND_CLUSTER" -n "$BYOB_NAMESPACE" \
  logs -l "tekton.dev/pipelineRun=RUN_NAME" --all-containers --prefix \
  --max-log-requests=10
```

- **Build pending:** inspect Pod events and resolver logs. Check image pulls,
  Git access, registry trust/auth, and the digest-pinned checkout bundle.
- **Build succeeds, signing stalls:** inspect `tekton-chains-controller` logs
  in `tekton-pipelines`. Check OCI signature/provenance storage and CA trust.
  Do not bypass the wait or change keys during the talk.
- **Snapshot has no test run:** inspect the IntegrationTestScenario resolver
  namespace and the integration-service controller. The expected label is
  `appstudio.openshift.io/snapshot=SNAPSHOT_NAME`.
- **Workspace or runs missing from UI:** export the rehearsal's namespace and
  rerun `bash scripts/demo.sh ui`. Check you logged in as `BYOB_UI_USER`, then
  refresh the workspace selector. Clear activity name/status/type filters.
- **Signature failure:** confirm that the copied public key matches Chains'
  signer and that the exact output digest was used. Do not substitute an
  unsigned validation mode.
- **Unpack fails:** check destination ownership, registry CA/auth mounts,
  artifact type, `payload.tar.gz`, and `hello.txt` in the chosen fixture.
- **Interrupted rehearsal:** rerun the failed stage. Setup is repeatable.
  A new build clears that branch's downstream state, so rerun its subsequent
  stages before using the evidence walkthrough.
- **No network during the talk:** use the four retained evidence commands
  above. Do not present expected output as if it was observed.

Read-only manifest preview, without contacting Kubernetes:

```sh
BYOB_REVISION=0123456789abcdef0123456789abcdef01234567 \
  bash scripts/demo.sh render build

bash scripts/demo.sh render release \
  registry.example/team/output@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
```

To remove the resources later, explicitly delete only the demo namespace you
created. The script intentionally supplies no automatic cleanup command.

## Scope of the claims

The [ADR version used for the talk](https://github.com/konflux-ci/architecture/blob/8a1811d7d8c59ebf6093ea3e795ac91cdd27980c/ADR/0073-bring-your-own-build-logic.md)
is **Proposed**. The repository is a proof of concept, with important differences:

- `build-cmd`/`build-args` replace the ADR's `script-path` interface.
- Local Tasks use namespace references and a development image; the checkout
  bundle is pinned. Production must enforce approved immutable Task identities
  and execution literals at admission.
- The local policy allows resource overrides; the ADR rejects `taskRunSpecs`
  more broadly. Private URL/Secret restrictions remain platform responsibilities.
- The current policy recognizes its catalog builder image and documented
  output type; accepting another image or type needs platform policy work.
- Source identity is checked, but Source L4 two-party review and Source VSA
  verification are separate SCM/platform requirements.
- Build L3 depends on the complete platform, isolation, and signing setup.
  A successful local run is not SLSA certification. Remote and matrix catalog
  support is not evidence that every configured remote host meets that bar.

Created with assistance from Gal's AI assistant.
