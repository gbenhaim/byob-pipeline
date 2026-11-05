# Presenter script: bring your own build logic

Audience: developers and managers. Total: **7:00**. Walk through the seven
slides in four minutes, show the prepared Konflux UI demo for 2:30, then
close in 30 seconds.

Run `make presentation` and open <http://127.0.0.1:4173>. This is the selected
ADR 0073 presentation, copied unchanged from the architecture worktree.
Use arrow keys to move, `O` for the overview, and `N` for the deck's detailed
notes and sources. Keep the Konflux UI in separate tabs and a terminal at the
repository root for recovery. Complete [setup and rehearsal](README.md) first.

## Before the audience arrives

```sh
export KIND_EXPERIMENTAL_PROVIDER=podman
export BYOB_KIND_CLUSTER=konflux
export BYOB_NAMESPACE=byob-demo-presentation
bash scripts/demo.sh all
bash scripts/demo.sh ui

# Confirm the offline evidence fallback is available.
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

Keep the namespace and exported environment the same during the talk. Cold
setup and fresh builds depend on external Git and registries, so use the
prepared runs during the seven-minute slot.

## 00:00–00:25 · Slide 1: Bring your own build logic

“We want teams to introduce a new artifact type without needing a new
platform build Task for every tool. Developers provide their script and
builder image; Konflux supplies the trusted execution environment.

“This is a working proof of concept for proposed ADR 73. The script writes
deliverables to `$BUILD_OUTPUT`; trusted platform code exports them.”

Point to `build.sh` and the output file. The deck's source references identify
the implementation snapshot it describes.

## 00:25–01:00 · Slide 2: Build logic becomes a user input

“Traditionally the build recipe lives inside a trusted catalog Task. A new
tool or format can therefore require catalog changes and platform review.

“BYOB separates that recipe from the execution envelope. Application teams
own the script, while Konflux owns the generic Task, confinement, artifact
transfers, and provenance integration.

“The current proof-of-concept policy approves one builder image. Broader
image choice still needs the platform's approval policy.”

## 01:00–01:40 · Slide 3: The script comes from the cloned source

“The default command is `./build.sh`, taken from the restored SCM checkout.
The builder image supplies its runtime and tools. Commands and arguments
are passed directly as argv, without shell parsing.

“The developer step has no Git or registry credential mounts. Trusted steps
restore source and export validated outputs. Conforma checks the recorded
source-relative command and approved builder digest.

“Production also needs protected Task definitions and admission controls
that prevent unsafe execution overrides.”

## 01:40–02:10 · Slide 4: Inputs stay within the same Pipeline

“One Pipeline can contain several builds, including chained or platform
matrix builds. Each input must come from checkout or another build recorded
in that same Pipeline's attestation.

“In the supplied chained Pipeline, each build restores the original source;
the previous build's output arrives separately under `$BUILD_INPUTS`.
Pack collects the selected outputs into the final OCI artifact. That gives
Conforma an input lineage it can check.”

## 02:10–02:55 · Slide 5: Conforma verifies the build contract

“Successful execution is not release approval. The integration scenario
first verifies Chains signatures and the signed subject digest. It then
evaluates the recorded command, builder, input lineage, and packaging
contract.

“The scenario returns its result through `TEST_OUTPUT`. A signed artifact
can still be rejected for violating policy. Our negative demo uses an
unsupported artifact type and reports `policy.artifact_pack`.

“Conforma checks the evidence. The trusted Task and platform enforce the
execution boundary.”

## 02:55–03:30 · Slide 6: SLSA Build L3: a trusted execution boundary

“Build freedom works only if user logic cannot alter platform state or
reach signing keys. Chains signs outside the developer's build environment;
Conforma checks the resulting evidence.

“The complete Build L3 claim depends on platform isolation, protected Task
identities, admission, and accurate provenance capture. A local successful
run is not SLSA certification.

“Build L3 does not require hermetic execution. Source-track assurance and
Source VSA verification are separate requirements.”

## 03:30–04:00 · Slide 7: Release BYOB artifacts to your destination

“A release Task invokes `unpack-build-output` to download and extract the
artifact by its immutable manifest digest. A later step in the same Task
can publish those files to the destination required by the use case.

“The StepAction restores the payload; the release Pipeline supplies policy
gating and destination-specific logic. In our demo, the later step prints
`hello.txt` so we can inspect the actual contents.”

## 04:00–06:30 · Prepared Konflux UI demo

Switch to the Konflux UI. Say explicitly:

“I ran the full workflow on local Kind before this talk. These are its actual
PipelineRuns, so we can inspect the complete story without waiting for pulls.”

**04:00–04:30 — Build.** Open **good_build**. Show checkout → build → pack,
then Results and the output digest.

“The existing `byob` Pipeline built and packaged the output. The demo script
waited for Chains to sign it. This manifest digest is the identity we carry
through the rest of the workflow.”

**04:30–05:00 — Integration scenario.** Open **good_test**. Show `TEST_OUTPUT`
and the report step's logs.

“Creating a Snapshot triggered the existing BYOB Conforma scenario. It
verified signatures and returned `SUCCESS`, with zero failures.”

**05:00–05:40 — Release consumption.** Open **good_release**. Show verify →
unpack, then **Logs → unpack → consume**. Point to the extracted file list
and `Built on x86_64` between the `hello.txt` markers.

“The release Pipeline verified the same digest before the Task invoked the
actual StepAction. The next step reads the extracted file. This demonstrates
release-task consumption; a production publisher would supply its own
destination-specific step.”

**05:40–06:30 — Rejection.** Open **bad_test** and show `policy.artifact_pack`
in the report logs. Then open **bad_release** and show the failed verifier
and skipped unpack Task.

“The separate negative build variant still built and was signed, but its
artifact type violated policy. Its different Pipeline name can also be
rejected. The release verifier failed before any unpack TaskRun was created.
A signed artifact is not automatically a releasable artifact.”

## 06:30–07:00 · Closing on slide 7

“Developers bring the build recipe. Konflux retains the execution contract
and signed evidence. Conforma decides whether the output can proceed, and
the release Task consumes the verified artifact for its use case.

“That is the path from a new build tool to a shared release workflow. The
remaining production work is enforcing the full platform trust boundary.”

Stop at seven minutes. Use the deck's notes and source links for questions.

## Fresh live runs and recovery

For a fresh run after the timed talk:

```sh
bash scripts/demo.sh build
bash scripts/demo.sh verify
bash scripts/demo.sh release
bash scripts/demo.sh reject
bash scripts/demo.sh status
```

Run the stages sequentially. A new build replaces that branch's recorded
artifact, so regenerate downstream stages before presenting those results.

- If the UI is unavailable, use the four `evidence` commands shown above and
  say they are retained rehearsal results.
- If a stage failed, show the failure honestly and use the slide diagrams to
  explain the intended handoff. Do not call an unverified run successful.
- If there is no recorded evidence, stay with the architectural explanation;
  the diagrams alone do not demonstrate a successful run.
- For detailed diagnosis and platform prerequisites, use the [runbook](README.md).

Prepared with assistance from Gal's AI assistant.
