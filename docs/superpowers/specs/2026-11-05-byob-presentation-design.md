# BYOB presentation and local Kind demo

Status: Proceeding with the local slide presentation selected by the user.

## Intent

Create an interactive website for presenting the problem and proposed solution
in [ADR 73 / architecture PR 387](https://github.com/konflux-ci/architecture/pull/387)
and the implementation in this repository. Provide an executable local Kind
demo and a presenter script that connect the architecture to real build,
integration-test, and release PipelineRuns.

The user specified developers and managers, with seven minutes available.
Budget approximately five minutes for the narrative and two minutes for a
prepared live demo. Provide a longer rehearsal runbook outside that timebox.

## Approaches

1. **Locally served slide presentation (recommended).** A static site with
   keyboard navigation, clickable diagrams, presenter notes, and copyable
   commands. No frontend build step or external resources during the talk.
   Demo execution happens in a terminal. This is portable and easy to rehearse.
2. **Scrolling technical walkthrough.** Better for independent reading and
   detailed reference, with less control over what an audience sees at once.
3. **Site connected directly to the cluster.** Can show live status within
   the presentation, but needs a backend and cluster access, and introduces
   extra failure modes during a talk.

The user selected approach 1: local slides and a separate live demo.

## Presentation

Create a self-contained site under `docs/presentation/`, using HTML, CSS,
and browser JavaScript. Use a small local server command without adding a
frontend framework to the Go CLI repository. The site must also work when
opened directly from disk, except for browser-restricted conveniences.

Proposed narrative:

1. The problem: adding a build tool or artifact format currently needs
   catalog work and platform review; a bespoke Pipeline also needs bespoke
   release policy.
2. The decision: let developers supply their build logic within a
   platform-owned execution boundary and a shared policy.
3. Who owns what: developer image and reviewed command; platform checkout,
   Tasks, execution environment, packaging, credentials, and provenance.
4. Click through `checkout -> run-build -> pack -> Chains`, showing exact
   source identity, Trusted Artifact handoffs, and final results.
5. The actual contract: executable `./build.sh`, argv-based `build-cmd` and
   `build-args`, `$BUILD_OUTPUT`, and optional `$BUILD_INPUTS`.
6. Extension points: chained, remote, and matrix Pipelines, without implying
   that the local demo establishes remote-VM isolation.
7. Conforma: verify signatures and subject digest before evaluating the
   recorded Tasks, source, builder, artifact wiring, and packaging contract.
8. Demo checkpoints: actual Kind commands and expected evidence, clearly
   distinguished from any explanatory animation or example result.
9. Release consumption: verify the same digest before an approved release
   Task invokes `unpack-build-output`.
10. Proposal versus implementation and remaining production integration.

Combine those topics into seven short slides, with supplementary details in
interactive panels and notes. Keep long-running setup and image pulls outside
the seven-minute talk. A warm demo can show retained real evidence and start a
new run without promising registry-dependent completion within two minutes.

Include fullscreen, keyboard and on-screen navigation, URL-addressable slides,
speaker notes, accessible focus behavior, reduced-motion support, responsive
layout, source links, and a print layout. Diagrams and policy explorations must
be explicitly labeled as explanatory rather than live cluster status.

## Accuracy and trust boundaries

Ground the presentation in ADR PR head
`8a1811d7d8c59ebf6093ea3e795ac91cdd27980c`. The ADR is proposed, not accepted.
Link to the ADR file at that revision as well as the PR discussion.

Explain these differences rather than silently smoothing them over:

- The ADR describes `script-path`; this repo uses `build-cmd` and `build-args`.
- Local build and pack Tasks are referenced by namespace name and use
  `localhost/byob-cli:devel`; production needs reviewed immutable identities
  and an admission integration. The checkout bundle is already digest-pinned.
- The local policy allows resource requests and limits in `taskRunSpecs`;
  the ADR describes broader rejection of `taskRunSpecs` at admission.
- The current Rego policy recognizes the catalog's developer image and
  documented output type. It is not already a general allowlist for every
  conceivable developer image and artifact format.
- Source identity checks do not prove two-person review or verify a Source
  VSA. Source L4 requires separate SCM evidence and ADR-0055 integration.
- Build L3 depends on the deployed platform and isolation, Chains
  configuration, admission policy, and credential boundaries. A successful
  local run is not a certification.
- `unpack-build-output` validates and extracts the OCI payload. Signature and
  policy verification are a preceding release gate, not performed by unpack.

## Kind demo

Add a Bash orchestration script, dedicated demo manifests, and a written
presenter runbook. Keep behavior implemented by the Go CLI in Go; the Bash
script orchestrates existing commands and Kubernetes resources.

Target an existing Konflux-on-Kind cluster. A bare Kind cluster lacks Chains,
the integration service, application CRDs, and the TLS registry. Document the
existing Konflux deployment command as one-time setup. Do not reinstall the
cluster, change the user's current context, or alter shared signing settings.

Use explicit context selection, an isolated demo namespace, unique run names,
bounded waits with failure diagnostics, and retained evidence. Do not delete
pre-existing namespaces. Keep credentials out of generated evidence and logs.

Proposed commands:

- `prepare`: check local tools and Konflux prerequisites; build and load the
  existing CLI image; install catalog resources and demo release resources;
  configure registry trust and namespace registry auth; copy only the Chains
  public key; install the existing Conforma IntegrationTestScenario.
- `build`: resolve a public, pushed Git revision to an exact commit, submit
  an unmodified `pipelines/byob.yaml` PipelineRun, display the three TaskRuns,
  collect `IMAGE_URL` and `IMAGE_DIGEST`, and wait for Chains signing.
- `verify`: create a Snapshot containing that digest and wait for the
  integration service's scenario PipelineRun. Display `TEST_OUTPUT` and
  Conforma's report, requiring `SUCCESS`.
- `release`: submit an example Tekton release Pipeline. Its first Task runs
  the existing Conforma validation logic against the exact Snapshot digest;
  only after success does a release Task reference the installed
  `unpack-build-output` StepAction and display `hello.txt`.
- `reject`: create an isolated variant of the build with an unsupported
  artifact type, leaving the original `byob` Pipeline intact. Show build and
  signing success followed by integration failure with
  `policy.artifact_pack`. Run the release Pipeline on that same bad digest
  and require that verification fails and unpack never executes.
- `status`: print retained object names and inspection commands for replay.

The demo release Pipeline demonstrates release-task consumption and a policy
gate. It is not a full ReleasePlan / ReleasePlanAdmission / production publish
configuration. State that distinction in the website and runbook.

Use the same validation task body as
`examples/integration/byob-pipeline-policy.yaml`, avoiding a separate policy
implementation that can diverge. Use the existing registry host
`registry-service.kind-registry`, TLS CA, and public-key conventions from the
system suite. The default public component fixture writes `hello.txt`; allow
source URL and revision overrides.

## Presenter script

Provide what to say for each slide and terminal checkpoint, commands to run,
expected observable evidence, approximate timing, and recovery steps. Include
a rehearsal checklist, an early setup command, and an evidence-based fallback
when registry or Git access prevents a live rerun. No fabricated successful
output may be presented as observed evidence.

## Verification

- Apply the repository's formatter, ShellCheck, scoped tests, and Tekton
  admission validation to changed executable code and manifests.
- Check website JavaScript and exercise navigation, diagrams, notes,
  fullscreen fallback, copied commands, and mobile/print layouts in a browser.
- Validate both the release dependency gate and the StepAction invocation.
- Rehearse against the available local `kind-konflux` cluster when tools and
  image loading permit; retain actual pass/fail output. Report any portion
  that could only be checked statically.

## Choices left to implementation

Visual styling, file organization, server implementation, exact slide count,
polling intervals, and concise diagram wording can be settled during building.
Keep the presentation independent of internet-hosted fonts, scripts, and icons.
Do not change the existing CLI behavior or production policy for the demo.
