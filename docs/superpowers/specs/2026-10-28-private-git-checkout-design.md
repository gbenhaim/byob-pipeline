# Private Git Checkout Design

## Intent

Allow BYOB Pipelines to clone private HTTPS Git repositories using the optional
workspace pattern used by Konflux build Pipelines. Keep credentials scoped to
the trusted checkout Task.

## Selected approach

Expose an optional Pipeline workspace named `git-auth`. Map it to the
`basic-auth` workspace of `git-clone-oci-ta` only on the checkout Task. A
PipelineRun can bind a namespace Secret to `git-auth`; the checkout Task accepts
the upstream Task's username/password or `.gitconfig` and `.git-credentials`
format. Public repository runs omit the workspace and remain unchanged.

Alternatives considered:

- A platform mutator selecting a credentialed ServiceAccount for checkout would
  require cluster-specific logic and differs from the Konflux workspace pattern.
- A credential parameter or a workspace attached to build Tasks would make
  secret use less controlled and expose it beyond checkout.

The user selected the Konflux pattern. This change covers HTTPS Basic Auth or a
read-only token; SSH-specific workspace support is outside this change.

## Scope and constraints

- Add the optional workspace and checkout-only mapping to all four Pipeline
  definitions and the embedded PaC Pipeline.
- Add a private PipelineRun example that references a Secret by name; never put
  credential values in the repository.
- Remove the unused `byob-checkout` ServiceAccount scaffold and mutator guidance.
- Document that Conforma admission must allow only approved repository URLs
  and Secret references before Task Pods start. Continue to reject inline Tasks
  and execution overrides.
- Do not pass Git credentials to build Tasks, remote VMs, or artifact handoffs.
- Preserve the existing source revision, Chains results, and artifact flow.

## Verification

Extend the Go catalog tests to check that each Pipeline exposes optional
`git-auth`, only checkout binds it to `basic-auth`, and the private example
references a Secret without containing secret data. Validate Pipeline and
PipelineRun manifests with the existing catalog checks.
