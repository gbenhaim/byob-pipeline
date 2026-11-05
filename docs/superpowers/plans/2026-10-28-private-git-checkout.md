# Private Git Checkout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow BYOB PipelineRuns to bind private HTTPS Git credentials using
the optional `git-auth` workspace pattern used by Konflux build Pipelines.

**Architecture:** Each Pipeline exposes optional `git-auth` and maps it only to
`git-clone-oci-ta`'s `basic-auth` workspace. PipelineRuns bind an existing
namespace Secret by name; the checkout Task alone receives it. Public checkout
remains unchanged when the workspace is omitted.

**Tech Stack:** Tekton Pipelines 1.16 YAML, Go 1.27, Ginkgo/Gomega catalog tests.

**Spec:** `docs/superpowers/specs/2026-10-28-private-git-checkout-design.md`

## Global Constraints

- Follow the Konflux `git-auth` to checkout `basic-auth` workspace pattern.
- Keep the workspace optional and bind it only to checkout.
- Never commit Git credentials or pass them to build Tasks or remote VMs.
- Conforma must allow only approved repository URLs and Git Secret references.
- Preserve the existing source revision results and Trusted Artifact flow.

## Review Focus

- Public checkout must still validate with no workspace bound.
- Every Pipeline and the embedded PaC Pipeline must map `git-auth` only to
  checkout's `basic-auth` workspace.
- Private PipelineRun examples must refer to a Secret name, with no Secret data.
- The credentials must not become Task params, TaskRun service accounts, or
  developer-step/remote-VM mounts.
- Conforma documentation must constrain the Git URL and Secret reference.

---

### Task 1: Preserve nil validation results

**Files:**
- Modify: `tests/tekton/main.go`
- Create: `tests/tekton/main_test.go`

**Interfaces:**
- Consumes: `validateResource` and its Task, Pipeline, and PipelineRun
  validators.
- Produces: nil errors for valid catalog resources, while preserving actual
  Tekton validation errors.

- [x] **Step 1: Add the failing Go regression test**

Add `TestValidResourcesReturnNoValidationError` using the real run-build Task,
byob Pipeline, and byob PipelineRun files. Assert `validateResource` returns
nil for each valid object.

- [x] **Step 2: Run the focused test and confirm it fails**

Run: `cd tests/tekton && GOCACHE=/tmp/byob-go-cache go test -run TestValidResourcesReturnNoValidationError .`

Expected: FAIL because the validator helpers return typed-nil
`*apis.FieldError` values through the `error` interface.

- [x] **Step 3: Return nil explicitly after successful validation**

In `validateTask`, `validatePipeline`, and `validatePipelineRun`, return the
validation error only when it is non-nil; otherwise return a nil `error`.

- [x] **Step 4: Run the focused regression test again**

Run: `cd tests/tekton && GOCACHE=/tmp/byob-go-cache go test -run TestValidResourcesReturnNoValidationError .`

Expected: PASS for all three real resource kinds.

---

### Task 2: Add private checkout support with catalog coverage

**Files:**
- Modify: `tests/catalog_test.go`
- Modify: `pipelines/byob.yaml`
- Modify: `pipelines/byob-remote.yaml`
- Modify: `pipelines/byob-matrix.yaml`
- Modify: `pipelines/byob-chained.yaml`
- Modify: `examples/pac/push.yaml`
- Create: `examples/pipelineruns/byob-private.yaml`

**Interfaces:**
- Consumes: parsed `spec` and `pipelineTask` catalog structures.
- Produces: an optional Pipeline workspace `git-auth` mapped only to
  `git-clone-oci-ta`'s `basic-auth` workspace, plus a private PipelineRun
  example that binds Secret `git-source-credentials`.

- [x] **Step 1: Add the failing Ginkgo spec**

Add workspace fields and a `findWorkspace` helper, then add
`It("scopes optional git auth to checkout", ...)` to inspect `byob`,
`byob-remote`, `byob-matrix`, `byob-chained`, and `examples/pac/push.yaml`'s
embedded `pipelineSpec`. Assert `git-auth` is optional, checkout maps it to
`basic-auth`, no other Task maps it, and `byob-private.yaml` binds it to the
`git-source-credentials` Secret without Secret data or a checkout-specific
service account.

- [x] **Step 2: Run the focused test and confirm it fails**

Run: `GOCACHE=/tmp/byob-go-cache go test ./tests -ginkgo.focus="scopes optional git auth to checkout"`

Expected: FAIL because the workspace declarations, mappings, and private
PipelineRun example do not exist yet.

- [x] **Step 3: Declare and bind `git-auth`**

Add an optional Pipeline workspace named `git-auth` to all four Pipelines and
the embedded PaC Pipeline. In each, map `checkout.workspaces.basic-auth` to
`git-auth`; do not bind it to any build or packaging Task.

- [x] **Step 4: Add the private PipelineRun example**

Copy the standard `byob` example and add:

```yaml
  workspaces:
  - name: git-auth
    secret:
      secretName: git-source-credentials
```

- [x] **Step 5: Run the focused test and confirm it passes**

Run: `GOCACHE=/tmp/byob-go-cache go test ./tests -ginkgo.focus="scopes optional git auth to checkout"`

Expected: PASS for all four catalog Pipelines, the PaC PipelineSpec, and the
private PipelineRun binding.

---

### Task 3: Align examples and platform guidance

**Files:**
- Modify: `examples/platform/serviceaccounts.yaml`
- Modify: `README.md`
- Modify: `AGENTS.md`

**Interfaces:**
- Consumes: the `git-auth` workspace and Secret name in the private example.
- Produces: setup instructions for a `kubernetes.io/basic-auth` Secret and
  current guidance that credentials reach checkout only.

- [x] **Step 1: Remove the unused checkout ServiceAccount scaffold**

Keep `byob-build`; remove `byob-checkout` and the TaskRun mutator comment because
the workspace binding supplies credentials directly to the checkout Task.

- [x] **Step 2: Document private checkout**

Explain how to create `git-source-credentials` with `username` and `password`
keys, bind it to `git-auth`, and submit `byob-private.yaml`. Document adding the
same workspace binding to a PaC PipelineRun when needed. State that the Secret
must remain in the PipelineRun namespace and must not be committed.
Update the platform and Conforma sections: no credentialed ServiceAccount
mutator is needed; admission must restrict the approved Git URL and Secret
reference; build Tasks and VMs never receive the credential.

- [x] **Step 3: Update `AGENTS.md`**

Record the optional Konflux workspace pattern in `AGENTS.md`, including that
the Secret is bound only to checkout and is never passed to builds or VMs.

- [x] **Step 4: Check the updated guidance and patch formatting**

Run: `git diff --check`
Expected: no output and exit status 0. Confirm no `byob-checkout` or checkout
TaskRun mutator guidance remains in the README, AGENTS file, or platform example.

---

### Task 4: Validate the catalog

**Files:**
- Verify: all files above

- [x] **Step 1: Run the full project checks**

Run: `make check`

Expected: Go/Ginkgo tests, formatting, lint, generated Task drift, and Tekton
Pipeline/PipelineRun validation all pass.

- [x] **Step 2: Check the patch for whitespace errors**

Run: `git diff --check`

Expected: no output and exit status 0.
