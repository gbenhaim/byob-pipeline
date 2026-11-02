export GOCACHE ?= /tmp/byob-go-cache
CLI_IMAGE ?= localhost/byob-cli:devel

.PHONY: build image generate check-generated check test lint validate smoke system-test
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
	shellcheck internal/remote/*.sh examples/component/*.sh examples/component/scripts/*.sh build.sh
	test -z "$$(gofmt -l cmd internal task-generator tests)"
	go vet ./...
	go -C tests/system vet ./...

validate:
	cd tests/tekton && go run . ../../task/*/0.1/*.yaml ../../pipelines/*.yaml ../../examples/pipelineruns/*.yaml ../../examples/pac/*.yaml

check: check-generated test lint validate

smoke:
	go test -count=1 ./...

system-test:
	@case "$${BYOB_CLUSTER:-existing}" in \
		existing) ;; \
		install) \
			printf '%s\n' \
				"BYOB_CLUSTER=install does not install a cluster." \
				"From a konflux-ci checkout, set KIND_MEMORY_GB=16 and run:" \
				"  export KIND_EXPERIMENTAL_PROVIDER=podman" \
				"  SKIP_SECRETS=true ./scripts/deploy-local.sh" \
				"Then rerun make system-test with BYOB_CLUSTER=existing."; \
			exit 1 ;; \
		*) printf '%s\n' "BYOB_CLUSTER must be existing or install"; exit 1 ;; \
	esac
	$(MAKE) image
	@bash -eu -c 'cluster="$${BYOB_KIND_CLUSTER:-}"; \
		if [ -z "$$cluster" ]; then \
			mapfile -t clusters < <(kind get clusters); \
			if [ "$${#clusters[@]}" -ne 1 ]; then \
				printf "%s\n" "Set BYOB_KIND_CLUSTER. kind get clusters returned: $${clusters[*]}"; \
				exit 1; \
			fi; \
			cluster="$${clusters[0]}"; \
		fi; \
		archive=$$(mktemp); \
		kubeconfig=$$(mktemp); \
		trap "rm -f $$archive $$kubeconfig" EXIT; \
		podman save localhost/byob-cli:devel -o "$$archive"; \
		kind load image-archive "$$archive" --name "$$cluster"; \
		kind get kubeconfig --name "$$cluster" > "$$kubeconfig"; \
		KUBECONFIG="$$kubeconfig" BYOB_KIND_CLUSTER="$$cluster" \
			go -C tests/system test -count=1 -timeout 25m .'
