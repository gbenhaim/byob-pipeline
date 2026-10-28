export GOCACHE ?= /tmp/byob-go-cache
CLI_IMAGE ?= localhost/byob-cli:devel

.PHONY: build image generate check-generated check test lint validate smoke
build:
	CGO_ENABLED=0 go build -trimpath -o dist/byob ./cmd/byob

image:
	podman build -t $(CLI_IMAGE) -f Containerfile .

generate:
	go run ./task-generator/remote --task task/run-build/0.1/run-build.yaml --remote-task task/run-build-remote/0.1/run-build-remote.yaml

check-generated:
	@generated=$$(mktemp); trap 'rm -f "$$generated"' EXIT; \
		go run ./task-generator/remote --task task/run-build/0.1/run-build.yaml --remote-task "$$generated" && \
		diff -u task/run-build-remote/0.1/run-build-remote.yaml "$$generated"

test:
	go test ./...

lint:
	shellcheck internal/remote/*.sh examples/component/*.sh examples/component/scripts/*.sh
	test -z "$$(gofmt -l cmd internal task-generator tests)"
	go vet ./...

validate:
	cd tests/tekton && go run . ../../task/*/0.1/*.yaml ../../pipelines/*.yaml ../../examples/pipelineruns/*.yaml ../../examples/pac/*.yaml

check: check-generated test lint validate

smoke:
	go test -count=1 ./...
