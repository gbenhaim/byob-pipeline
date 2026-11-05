# Verification

## User-selected ADR 0073 presentation

At the user's request, `docs/presentation/index.html` is a verbatim copy of
the architecture worktree's `0073-bring-your-own-build-logic.html`. The original
separate stylesheet and browser script were removed. The runbook and timed
narration now follow the selected deck's seven slides and a separate Konflux UI
walkthrough.

- `cmp` confirmed the copied HTML matches the selected source byte for byte.
- `make check`, Prettier, inline JavaScript and local-server syntax checks,
  and whitespace checks passed.
- Headless Chrome verified all seven slides, keyboard/button navigation,
  overview, notes and source links, deep links, print visibility, 28 responsive
  layouts, and opening the copied file directly from disk. The deck made no
  external requests and reported no browser errors.
- HTTP checks confirmed the server returns the unchanged HTML, supports HEAD,
  and refuses obsolete asset routes, repository metadata, and POST requests.

The Kind demo was not rerun for this presentation replacement. The earlier
cluster results below remain the recorded evidence for the companion demo.

## 2026-11-05: initial presentation and local Kind demo

The browser checks in this section describe the original presentation before
the user selected the replacement deck above.

Added a seven-minute local presentation and an orchestrated demo using the
existing build catalog, Conforma IntegrationTestScenario, and unpack StepAction.

Verification performed for this change:

- `make check` passed: Go tests, Go formatting/vet, ShellCheck (including the
  demo script), generated Task drift checks, and Tekton admission validation.
  Existing HTTP fixture tests required execution outside the sandbox because
  the sandbox prevents binding local test-server sockets.
- Go/Ginkgo demo tests exercise real rendered resources, exact source identity,
  immutable digest validation, release-gate dependencies, and conversion of
  Tekton's defaulted inline validator into a standalone Task.
- The generated validator Task also passed the catalog's Tekton admission
  validator. The release example is included in `make validate`.
- Browser JavaScript syntax and Prettier formatting passed. Headless Chrome
  exercised all seven slides, deep links, keyboard navigation and focus,
  interactive build stages, policy cases, notes, clipboard, print visibility,
  390-pixel layouts, and opening the presentation directly from disk.
  Desktop and mobile screenshots were visually inspected.
- Independent review identified default Make target, cross-context preparation,
  and skip-link navigation regressions. Each was reproduced and fixed with
  a failing-then-passing behavior check. Final `make check` and browser QA
  passed after the fixes. A complete `demo.sh all` rehearsal exited zero;
  repeat preparation on the original context also succeeded.
- Built and loaded the CLI image, then rehearsed in the isolated
  `byob-demo-presentation` namespace on the existing `kind-konflux` cluster.
  The original `byob` Pipeline completed checkout, build, and pack, and Chains
  signed its OCI output. The actual IntegrationTestScenario returned
  `TEST_OUTPUT.result=SUCCESS`, with 18 successes, zero failures, and zero
  warnings. The release Pipeline revalidated that digest, invoked
  `unpack-build-output`, and consumed `hello.txt` containing `Built on x86_64`.
- A separate Pipeline variant with an unsupported artifact type completed and
  was signed. The scenario returned `FAILURE`, with `policy.artifact_pack`
  and an additional `policy.pipeline_name` violation for the variant's name.
  The release verifier failed and **no unpack TaskRun was created**.

Actual PipelineRun JSON, logs, and run identities are retained locally in
`dist/demo/byob-demo-presentation/` (ignored by Git). Replay them with
`BYOB_NAMESPACE=byob-demo-presentation bash scripts/demo.sh evidence STAGE`.
No external release destination, ReleasePlan, Source VSA validation, remote
VM lifecycle, or production admission configuration was exercised. These
results do not establish SLSA certification.

The Konflux UI follow-up was verified against the same rehearsal namespace:

- `make lint`, `go test ./tests -count=1` with the project Go cache, Bash syntax,
  Prettier, and whitespace checks passed. New behavior tests cover UI labels,
  the configured user viewer binding, ownership checks, context isolation, and
  upgrading recorded runs without starting workloads.
- The namespace-lister API returned `byob-demo-presentation` for
  `user1@konflux.dev`. Kubernetes impersonation confirmed read access to all
  12 application PipelineRuns (four each of build, test, and release), logs,
  and namespace metadata, with no Secret access.
- Before/after comparisons confirmed existing PipelineRun specs and statuses
  were unchanged. Updated build/release manifests and the viewer binding
  passed server-side dry-run admission. The UI activity URL returned HTTP 200.
- Interactive visual verification of the Konflux UI was unavailable because
  the browser access host was unavailable. Namespace discovery and data
  permissions were verified through the APIs; the runbook provides direct
  run links and the presenter script follows the UI walkthrough.

The unpack logging follow-up passed `make lint validate`, the catalog Go tests,
Prettier, whitespace checks, and server-side dry-run admission. A fresh release
PipelineRun, `byob-demo-1793890681-30211-release`, completed both Tasks with
Conforma reporting 18 successes and zero failures/warnings. Its `consume`
step logs list `./hello.txt` and print `Built on x86_64` between filename
markers. This verifies the output shown in the presenter walkthrough.

## Earlier Go CLI verification

The results below describe the previous Go CLI revision, before the Podman smoke
test removal and readability refactor. For this change, `gofmt` and
`git diff --check` were run; the Go suite and catalog checks were not rerun.

That revision was validated on 2026-10-28 with the Go 1.27 Cobra CLI, directly
maintained catalog YAML, and the remote Task generated by `make generate`.

`make check` passed:

- Go tests for filesystem validation, output staging, deterministic packaging,
  CLI restoration/publication, remote orchestration, the generator, and catalog
  wiring. Coverage includes empty/multiple build inputs, filename collisions,
  restore failures, malformed paths, symlinks, direct executable/argv handling,
  and failed publication without final results.
- Remote unit fixtures cover TLS OTP exchange with a controller CA, strict SSH
  host verification, quoting, and cancellation cleanup.
- ShellCheck for the retained VM worker and example scripts, Go vet and formatting.
- Admission-type validation against Tekton Pipelines 1.16.0 for all 3 Tasks and
  4 Pipelines, plus 5 example PipelineRuns.
- Generated remote Task drift checks.

A Go AST audit checked all 143 named function declarations, including tests,
helpers, methods, and entrypoints: every function has a name-leading documentation
comment. A full repository scan found no Python source or bytecode files; obsolete
Python ignore patterns and stale empty directories were removed.

`make image` built `localhost/byob-cli:devel` with the digest-pinned Red Hat
`hi/go:1.27` builder, verified as Go 1.27.3, and the pinned Konflux task-runner
runtime. The image includes the unmodified upstream artifact restoration helpers.

`make smoke` runs the Go tests without building containers. Command fixtures cover
registry and SSH process boundaries; they verify OCI results, argv handling,
output-only transfer, failure propagation, cancellation, and cleanup. Filesystem
tests cover empty and multiple artifact inputs, colliding filenames, deterministic
packaging, symlinks, and path validation. These tests do not verify actual Podman
mount, user, entrypoint, or image CMD behavior.

The earlier revision's container smoke exercised actual Trusted Artifact creation
and restoration, local packaging and OCI publication, rootless Podman, and a native
image without a shell through a local SSH fixture. That opt-in smoke was removed;
the current tests run the Go logic directly and use command fixtures for external
tools. The CLI image was built with the digest-pinned Red Hat `hi/go:1.27` builder
and the pinned Konflux task-runner runtime at the previous revision.

The fixtures do not validate an actual container runtime, a real SSH server or
controller deployment, cloud VM allocation, or controller VM cleanup on
completion/cancellation. OTP transport and CLI cancellation behavior are covered
by local fixtures. No live cluster was used,
and no catalog bundles were published. Chains signing, Source VSA, admission, and
release-policy integration remain platform validation work; these checks do not
establish SLSA certification. See [the platform prerequisites](../README.md#required-platform-integration)
and [the manual cluster test commands](../README.md#install-and-run) for the next
integration step.
