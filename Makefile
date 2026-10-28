PYTHON ?= python3
export GOCACHE ?= /tmp/byob-go-cache

.PHONY: generate check-generated check test lint validate smoke
generate:
	cd task-generator/remote && go run . --task ../../task/run-build/0.1/run-build.yaml --remote-task ../../task/run-build-remote/0.1/run-build-remote.yaml

check-generated:
	@generated=$$(mktemp); trap 'rm -f "$$generated"' EXIT; \
		(cd task-generator/remote && go run . --task ../../task/run-build/0.1/run-build.yaml --remote-task "$$generated") && \
		diff -u task/run-build-remote/0.1/run-build-remote.yaml "$$generated"

test:
	$(PYTHON) -m unittest discover -s tests -v
	cd task-generator/remote && go test ./...

lint:
	shellcheck task-generator/remote/scripts/*.sh examples/component/*.sh examples/component/scripts/*.sh
	$(PYTHON) -m compileall -q tests
	test -z "$$(cd task-generator/remote && gofmt -l .)"

validate:
	cd tests/tekton && go run . ../../task/*/0.1/*.yaml ../../pipelines/*.yaml ../../examples/pipelineruns/*.yaml ../../examples/pac/*.yaml

check: check-generated test lint validate

smoke:
	$(PYTHON) tests/smoke.py
