# Presenter script: your build, our trust boundary

Audience: developers and managers. Total: **7:00**. Slides 1–5 take four
minutes; the prepared Konflux UI demo takes 2:15; the closing takes 45 seconds.

Open `make presentation`, then <http://127.0.0.1:4173>. Use `N` for the shorter
notes embedded in each slide. Keep the Konflux UI in separate browser tabs
and a terminal at the repository root for recovery. Complete
[setup and rehearsal](README.md) before the talk.

## Before the audience arrives

```sh
export KIND_EXPERIMENTAL_PROVIDER=podman
export BYOB_KIND_CLUSTER=konflux
export BYOB_NAMESPACE=byob-demo-presentation
bash scripts/demo.sh all
bash scripts/demo.sh ui

# Confirm all four evidence views are available.
bash scripts/demo.sh evidence build
bash scripts/demo.sh evidence verify
bash scripts/demo.sh evidence release
bash scripts/demo.sh evidence reject
```

Sign in to <https://localhost:9443> as `user1@konflux.dev` / `password`
(standard local deployment). Select workspace `byob-demo-presentation` and
Application `byob-slsa`, then **Activity → Pipeline runs**. Open the links
printed by `ui`: `good_build`, `good_test`, `good_release`, `bad_test`,
`bad_release`. Keep those tabs in that order. The release is a demo Tekton
PipelineRun in Activity, not a production Release resource.

Increase terminal font size. Leave the four evidence commands in shell history
as a fallback.
Keep the namespace and exported environment the same during the talk. A cold
setup is outside the time budget. A fresh build also depends on external Git
and registries; do not promise that it will complete during the presentation.

## 00:00–00:35 · Slide 1: the idea

“A team wants to build a Python wheel, a binary, or a new artifact format.
Tekton can already run the command. The harder question is whether Konflux
can still explain and trust what produced the result.

“Bring your own build separates those concerns: developers own their build
logic, while Konflux keeps ownership of the execution boundary and evidence.
That is the idea behind proposed ADR 73, and this repo implements a working
proof of concept.”

Point to the developer build between platform-owned checkout and packaging.

## 00:35–01:15 · Slide 2: the friction

“Today, adopting a new tool often means extending the shared catalog:
a new Task, Pipeline, platform review, and release. That is platform work
before the developer gets to solve the actual build problem.

“The alternative is a custom Pipeline. It can build, but it then needs a
matching Conforma policy and approval before it has a release path. Each
team ends up rebuilding part of the trust story.

“The goal is faster adoption with one shared platform and shared policy.”

## 01:15–02:10 · Slide 3: the boundary

“The developer brings an image and a command reviewed with the source.
Konflux owns the Tasks, source checkout, credentials, isolation, output
packaging, and provenance. The developer does not get to replace the Task,
mount a platform secret, or choose a ServiceAccount.

“In this repo, the default command is `./build.sh`. It writes deliverables
to `$BUILD_OUTPUT`. The build step has no registry or Git credential mounts;
trusted steps handle the artifact transfers.

“Builds may fetch dependencies. Isolation and hermeticity are different
properties. Production admission controls and source-review evidence are
still required.”

Click **Reviewed build command**, then **Declared outputs**. Do not explore
every control during the talk.

## 02:10–03:10 · Slide 4: the implementation

“This is `pipelines/byob.yaml`. Checkout fetches an exact commit and emits a
Trusted Artifact. The fixed build Task restores it and executes the
developer command. Only declared output files are exported.”

Click **run-build**.

“Commands and arguments are passed as argv, without shell parsing. Source,
working files, and exported outputs stay separate.”

Click **pack**.

“Pack publishes the output as an OCI artifact containing `payload.tar.gz`.
It emits the final image URL and manifest digest. Chains then signs the
provenance from outside the developer's environment.

“The repo also has chained, matrix, and remote variants. Today's demo is
the simplest in-cluster path.”

## 03:10–04:00 · Slide 5: the release gate

“Successful execution is not release approval. Conforma verifies signatures
and the artifact's subject digest, then evaluates the recorded build.”

Click **Wrong artifact type**.

“This build can finish and be signed, yet still fail the packaging contract.
The integration test reports `policy.artifact_pack` and `TEST_OUTPUT=FAILURE`.

“The release example repeats the same validator before unpacking the same
immutable artifact. The StepAction validates and extracts the payload; it
does not replace the signature and policy gate.”

The cases on this slide are explanatory. The next step shows actual results.

## 04:00–06:15 · Slide 6: prepared demo

Switch to the Konflux UI. Say explicitly:

“I ran the full workflow on local Kind before this talk. These are the
actual PipelineRuns, so we can inspect the complete
story without waiting for image pulls.”

**04:00–04:30 — Build.**

Open **good_build**. Show the checkout → build → pack graph, then Results
and the output digest.

“Here are checkout, build, and pack in the real PipelineRun. The output is
identified by a manifest digest, and the demo script waited for Chains
to sign it. We pass that identity through the rest of the workflow.”

**04:30–05:00 — Integration scenario.**

Open **good_test**. Show the integration PipelineRun, its `TEST_OUTPUT`
result, and the report step's logs.

“The script created a Snapshot for that digest. The integration service
started the existing BYOB Conforma scenario. This is its actual
`TEST_OUTPUT`: `SUCCESS`, with zero failures. It verified signatures before
running the policy.”

**05:00–05:35 — Release consumption.**

Open **good_release**. Show verify → unpack, then Logs → unpack → consume
and the extracted file list. Between the `hello.txt` markers, point to the
line `Built on x86_64` printed from the unpacked file.

“The release Pipeline ran verification first, then its dependent unpack
Task. That Task invoked the existing `unpack-build-output` StepAction and
read `hello.txt`. This demonstrates release-task consumption; it is not a
full production ReleasePlan deployment.”

**05:35–06:15 — Rejection.**

Open **bad_test**, showing `policy.artifact_pack` in report logs. Open
**bad_release**, showing the failed verifier and skipped unpack Task.

“The negative run changed the artifact type in a separate Pipeline variant.
The build and signing still succeeded, but the integration report contains
`policy.artifact_pack`. Its different Pipeline name can also be rejected.
The release verifier failed, and the unpack Task was skipped. A signed
artifact is not automatically a releasable artifact.”

If asked for a fresh live run, use these after the timed talk:

```sh
bash scripts/demo.sh build
bash scripts/demo.sh verify
bash scripts/demo.sh release
bash scripts/demo.sh reject
```

Run stages sequentially; each command waits for the required evidence. A new
build replaces that branch's retained state, so regenerate the downstream
stages before replaying the evidence again.

## 06:15–07:00 · Slide 7: the takeaway

“The developer brings the recipe. Konflux keeps the execution contract.
Conforma decides what can release.

“The proof of concept connects build outputs, signed evidence, integration
policy, and release unpacking. Production still needs immutable trusted Task
identities, admission enforcement, complete isolation, and separate Source
VSA verification. Source identity alone does not establish Source L4.

“The proposed direction is more build freedom inside one shared trust
boundary, instead of a new platform and policy for every tool.”

Stop at seven minutes. Keep implementation differences collapsed unless asked.

## Recovery during the talk

- If the cluster or network is unavailable, the `evidence` commands still
  work from local files. State that they are retained rehearsal results.
- If a stage failed in rehearsal, show the failure honestly and use the
  architecture slide to explain the intended handoff. Do not call it verified.
- If there is no recorded evidence, stay with the explicitly labeled
  explanatory policy cases and offer to run the demo after the talk.
- For detailed diagnosis and exact platform prerequisites, use the
  [demo runbook](README.md).

Prepared with assistance from Gal's AI assistant.
