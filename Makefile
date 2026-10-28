PYTHON ?= python3
export GOCACHE ?= /tmp/byob-go-cache

.PHONY: generate check test lint validate smoke
generate:
	$(PYTHON) hack/render-catalog.py

test:
	$(PYTHON) -m unittest discover -s tests -v
	cd task-generator/remote && go test ./...

lint:
	shellcheck scripts/*.sh examples/component/*.sh examples/component/scripts/*.sh
	$(PYTHON) -m compileall -q scripts hack tests
	test -z "$$(cd task-generator/remote && gofmt -l .)"

validate:
	cd tests/tekton && go run . ../../task/*/0.1/*.yaml ../../pipelines/*.yaml

check: test lint validate
	$(PYTHON) hack/render-catalog.py --check

smoke:
	$(PYTHON) hack/smoke.py
