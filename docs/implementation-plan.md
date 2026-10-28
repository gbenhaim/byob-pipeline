# BYOB pipeline implementation

Implements architecture PR 387 (8a1811d), with the user's changes: a new native
run-build Task, a remote variant generated using the container-build-catalog
generator's approach, localhost support, and a mixed-platform matrix example.

## Decisions

- Reuse pinned upstream git-clone-oci-ta and Trusted Artifacts images. The default
  developer image is Konflux task-runner.
- Developer code runs directly in a restricted, non-root local step. The remote
  generator emits separate conditional local and trusted SSH steps: the developer
  image never receives the controller's SSH Secret.
- Remote execution uses rootless Podman, with copied source rather than host bind
  mounts. The controller owns remote VM lifetime. localhost uses the local step.
- Each build consumes checkout's SOURCE_ARTIFACT and emits BUILD_ARTIFACT containing
  only out/ and platform metadata. A chained step also consumes preceding outputs
  through a `BUILD_ARTIFACTS` array, defaulting to `[]`. Only pack publishes IMAGE_URL
  and IMAGE_DIGEST. pack accepts a `BUILD_ARTIFACTS` array, with flat or platform
  layout.
- Matrix results carry their platform inside the trusted exported build artifact;
  packaging never assumes ordering of parallel TaskRun results.
- Admission, Conforma, Source VSA, signing, isolated credential delivery, and
  validation of Tekton's local step isolation remain external prerequisites.
- No registry publishing, deployment, or cloud provisioning is performed here.

## Implementation tasks

1. Write behavioral tests, then implement source validation and packaging helpers.
2. Implement the local Task and adapt the Go remote generator; test generated
   routing, credential separation, source transfer, and failure propagation.
3. Implement pack and local/matrix pipelines, examples, and documentation. Test
   complete artifact wiring and publication.
4. Run local checks and available integration checks, perform an independent
   review, address material findings, and report infrastructure-dependent limits.

## Validation

Python unittest exercises filesystem behavior and shell helpers; Go tests exercise
generation; ShellCheck checks scripts; generated remote Task drift is checked in CI.
An opt-in disposable-registry smoke test exercises real Trusted Artifact handoffs
and packaging. Cluster tests require a deliberately selected Tekton environment.

## Catalog maintenance cleanup

The user approved replacing the Python catalog/deployment helpers with directly
maintained YAML, the existing Go remote generator, static PipelineRun examples,
and documented `tkn`/`kubectl` commands. Runtime behavior stays the same.

1. Make local Task and Pipeline YAML authoritative; remove duplicate local helper
   scripts and have behavioral tests execute the embedded Task scripts. Keep the
   remote generator's three shell inputs next to the generator.
2. Remove `hack/`, move the smoke test to `tests/`, and have `make generate` and
   `make check-generated` invoke only the Go remote generator. Preserve syntax
   validation of embedded scripts and actual Tekton admission validation.
3. Replace run rendering and cluster submission with static development and PaC
   examples plus explicit publishing, installation, and test commands. Validate
   examples and preserve literal build settings and bundle reference structure.
4. Run `make check` and `make smoke`, review the cleanup, and commit locally.

## Build output handoffs

The user explicitly requested output-only intermediate uploads. This overrides
the ADR's full-source-tree handoff: every build restores the original checkout,
and an optional `BUILD_ARTIFACTS` array restores preceding outputs separately at
`BUILD_INPUTS/<index>/out/` in array order, starting at zero.
The existing source checkout artifact remains necessary to distribute reviewed
source; run-build never republishes it.

1. Cover output-only staging, invalid output paths, matrix metadata, and chained
   execution with behavioral tests. Keep source-path injection regressions.
2. Stage validated out/ in a separate Task volume accessible only to trusted
   validation/export steps. Export BUILD_ARTIFACT from that volume, update pack
   and Pipeline wiring, and remove the unused prepare/export branches.
3. Transfer preceding outputs to remote builders and return only out/. Regenerate
   the remote Task and preserve localhost behavior.
4. Update examples/docs and exercise real output artifact upload/restore, chained
   builds, packaging, and remote output copy-back in the container smoke test.
   Run make check and make smoke, obtain an independent review, and commit locally.
