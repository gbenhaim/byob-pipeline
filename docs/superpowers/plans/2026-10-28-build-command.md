# Shell-independent build commands and CLI cleanup

> Implement in the current feature branch; use the existing execution and parallel-agent workflows for independent Go packages.

**Goal:** Replace `script-path` with argv-array `build-cmd`, execute without a shell in developer images, use Cobra, document every declared Go function, and remove Python leftovers and empty directories.

**Spec:** The user's five requested changes. Preserve Go 1.27, output-only handoffs, indexed artifact inputs, localhost/remote behavior, and credential separation.

**Architecture:** `build-cmd` defaults to `["./build.sh"]`. A static copy of the CLI is staged by trusted `prepare` into a separate volume, mounted read-only by the local developer step. Its `exec-build` command sets up HOME and the source working directory, then replaces itself with the argv using `exec`. Remote Podman directly overrides the developer image entrypoint with the executable and passes its remaining arguments. The VM worker can still use Bash on the VM; the developer container needs only its selected executable and any libraries/interpreter that executable itself uses.

**Interfaces:**
- `artifact.Prepare(source string) error` validates source only; the selected executable may live in the developer image.
- `launcher.Install(directory string) error` stages the current static CLI as `directory/byob`.
- `launcher.Exec(source, home string, command []string) error` directly executes the build command.
- `remote.BuildOptions.BuildCmd []string` replaces `ScriptPath`.
- Cobra `prepare --source=... --tools=...`, `exec-build --source=... --home=... -- COMMAND ARG...`, and `build-remote ... -- COMMAND ARG...` preserve argument boundaries.

## Tasks

- [x] Port the CLI to Cobra and implement static launcher installation/execution, with tests for shell-free execution, argument boundaries, exit status, and help/error handling.
- [x] Update remote execution and tests to pass argv directly to Podman; preserve quoting, cleanup, source isolation, and image metadata.
- [x] Simplify source preparation and retain filesystem safety tests; document all artifact functions and test helpers.
- [x] Update authored Tasks, generator, all Pipeline/example callers, catalog tests, and Go smoke coverage; prove local and remote builds in an image containing no shell.
- [x] Add meaningful documentation to every Go function declaration, including tests and entrypoints; audit with Go's AST parser.
- [x] Delete remaining repository Python files/bytecode and remove empty directories, excluding Git metadata.
- [x] Run `make check`, image build, and smoke tests; perform an independent review, fix material findings, and commit locally.

## Review focus

- The local launcher must be static, executable, and unavailable for modification by developer code.
- Kubernetes must not precreate the source directory as the container working directory before checkout restoration.
- Command arguments containing spaces, quotes, shell operators, or leading dashes remain data; no implicit shell or image CMD is appended.
- A missing executable or failed build returns failure and preserves remote cleanup behavior.
- Shell-free image coverage must exercise both local Task command wiring and the remote Podman worker.
