# Repository guidance

## Go and tests

- Use Go 1.27, Cobra, and Ginkgo/Gomega. Test Go CLI behavior with Go-only tests
  and command fixtures for external tools; avoid Podman smoke tests.
- Keep functions focused and readable. Add a blank line between code blocks,
  run `gofmt`, and document every Go function.
- Do not add Python scripts or test harnesses for behavior implemented by the
  Go CLI.
- Keep `internal/remote/worker.sh` split into named functions and limit its
  lines to 80 characters.

## Private Git checkout

- Use the optional `git-auth` Pipeline workspace and map it only to checkout's
  `basic-auth` workspace, following the Konflux build Pipeline pattern.
- PipelineRuns reference a namespace Secret by name. Do not commit Secret data
  or pass Git credentials to build Tasks, remote VMs, or artifact handoffs.
- Conforma admission must restrict private runs to approved Git URLs and
  approved Secret references.
