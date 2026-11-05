# BYOB Presentation Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan task-by-task. Track steps below.

**Goal:** Deliver a seven-minute interactive presentation for developers and managers, plus a real Kind build / Conforma / release-unpack demo.

**Slide replacement:** At the user's request, the shipped deck is now a verbatim
copy of the architecture worktree's `0073-bring-your-own-build-logic.html` in
`docs/presentation/index.html`. It embeds its own CSS and JavaScript. The original
slide-design tasks below are historical; the separate `styles.css` and `app.js`
were removed. The demo remains, and the narration follows the selected slides.

**Architecture:** Static HTML/CSS/JavaScript slides served locally with Node built-ins. Bash orchestrates the existing Tekton catalog and integration scenario. A demo release Pipeline reuses the integration validator before invoking the existing StepAction.

**Tech Stack:** Browser JavaScript, Node.js, Bash, jq, Kubernetes/Tekton YAML, existing Go/Ginkgo validation.

**Spec:** `docs/superpowers/specs/2026-11-05-byob-presentation-design.md`

## Global Constraints

- Seven minutes, developers and managers; no external frontend assets or runtime dependencies.
- Do not change existing CLI behavior or production policy for the demo.
- Use explicit Kind context, isolated namespace, unique names, and bounded waits.
- Never print/store registry credentials or signing private keys in evidence.
- Use an exact Git commit and OCI manifest digest throughout verification/release.
- Describe the ADR as proposed; distinguish PoC checks from production SLSA requirements.

## Review Focus

- Rejected release must never create an unpack TaskRun.
- A successful Tekton run with `TEST_OUTPUT=FAILURE` must not count as accepted.
- Registry auth absence is allowed only for an anonymous registry, not a masked API error.
- Invalid digest inputs and unsafe context/namespace selections must fail before mutation.
- Navigation from URL, keyboard use in controls, file opening, and narrow screens must remain usable.

### Task 1: Demo manifests and orchestration

**Files:** `scripts/demo.sh`, `examples/demo/release.yaml`, `tests/demo_test.go`, `Makefile`.

**Interfaces:** `bash scripts/demo.sh prepare|build|verify|release|reject|status|all`; read-only `render build|snapshot|release [DIGEST_REFERENCE]`. Environment: `BYOB_KIND_CLUSTER`, `BYOB_NAMESPACE`, `BYOB_GIT_URL`, `BYOB_GIT_REF`, optional `BYOB_REVISION`. Evidence: `dist/demo/<namespace>/state.json` and nonsecret object/log files.

- [x] Add Go/Ginkgo behavior tests for rendered build/source parameters, digest validation, and release wiring; run to observe missing-script failures.
- [x] Implement manifests and script with preflight, image loading, existing Conforma task reuse, scoped trust/auth, immutable state, positive/negative checks, and diagnostics.
- [x] Run tests, ShellCheck, and Tekton validation. Include generated validator Task validation.

### Task 2: Interactive presentation

**Files:** `docs/presentation/index.html`, `styles.css`, `app.js`, `scripts/serve-presentation.mjs`, `Makefile`.

**Interfaces:** `make presentation` serves `http://127.0.0.1:4173`; seven hash-linked slides, interactive build flow and policy cases, presenter notes, keyboard controls, accessible copy/fullscreen fallback.

- [x] Build the static presentation with an editorial layout, meaningful diagrams and seven-minute timing.
- [x] Check JavaScript syntax and use a real browser to exercise navigation, notes, diagrams, policy states, mobile layout, printing, and file opening.

### Task 3: Runbook and rehearsal

**Files:** `docs/presentation/README.md`, `docs/presentation/presenter-script.md`, `README.md`, `docs/verification.md`.

**Interfaces:** Document the commands produced by Task 1 and the narrative in Task 2; keep one source of executable demo commands.

- [x] Write timed narration, pre-talk preparation, actual evidence inspection, fresh-run commands, expected rejections, and recovery.
- [x] Run formatter, lint, scoped tests, and catalog admission validation; rehearse on local Konflux Kind where available.
- [x] Have a fresh reviewer check the full change, address substantive findings, and report exact verified outcomes and limitations.

## Execution notes

Work in the existing feature checkout; no merge, push, or commit was requested.
Implement inline and use one final reviewer. Low-impact presentation interactions
receive browser QA; demo gate behavior and manifest boundaries receive automated
tests. This follows the repository's scoped verification requirement.
