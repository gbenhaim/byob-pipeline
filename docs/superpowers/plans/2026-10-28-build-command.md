# Shell-independent build commands and CLI cleanup

> Implement in the current feature branch; use the existing execution and parallel-agent workflows for independent Go packages.

**Goal:** Replace `script-path` with argv-array `build-cmd`, execute without a shell in developer images, use Cobra, document every declared Go function, and remove Python leftovers and empty directories.

**Spec:** The user's five requested changes. Preserve Go 1.27, output-only handoffs, indexed artifact inputs, localhost/remote behavior, and credential separation.

**Architecture:** `build-cmd` defaults to `["./build.sh"]`. The local developer step expands it directly into the container command array and sets `workingDir` to a source volume mounted at `/var/source`. Trusted `prepare` validates the checkout and creates writable HOME. Remote Podman directly overrides the developer image entrypoint with the executable and passes its remaining arguments. The VM worker can still use Bash on the VM; the developer container needs only its selected executable and any libraries/interpreter that executable itself uses.

**Interfaces:**
- `artifact.Prepare(source string) error` validates source only; the selected executable may live in the developer image.
- `prepare --source=... --home=...` validates checkout and creates writable HOME.
- `remote.BuildOptions.BuildCmd []string` replaces `ScriptPath`.
- Cobra `build-remote ... -- COMMAND ARG...` preserves argument boundaries for VM execution.

## Tasks

- [x] Port the CLI to Cobra, prepare writable HOME, and use native build-command argv with tests for argument boundaries, exit status, and help/error handling.
- [x] Update remote execution and tests to pass argv directly to Podman; preserve quoting, cleanup, source isolation, and image metadata.
- [x] Simplify source preparation and retain filesystem safety tests; document all artifact functions and test helpers.
- [x] Update authored Tasks, generator, Pipeline callers, catalog tests, and Go-only command fixtures. Container runtime behavior is outside the Go smoke suite.
- [x] Add meaningful documentation to every Go function declaration, including tests and entrypoints; audit with Go's AST parser.
- [x] Delete remaining repository Python files/bytecode and remove empty directories, excluding Git metadata.
- [x] Run `make check`, image build, and smoke tests; perform an independent review, fix material findings, and commit locally.

## Review focus

- The developer image must run without a shell or BYOB binary; the source volume mount equals Task `workingDir` and is restored before execution.
- Kubernetes must not precreate the source directory as the container working directory before checkout restoration.
- Command arguments containing spaces, quotes, shell operators, or leading dashes remain data; no implicit shell or image CMD is appended.
- A missing executable or failed build returns failure and preserves remote cleanup behavior.
- Shell-free image coverage must exercise both local Task command wiring and the remote Podman worker.
