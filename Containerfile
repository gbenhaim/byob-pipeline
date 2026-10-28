FROM registry.access.redhat.com/hi/go:1.27@sha256:54644838229bd919c8718ab13e0696f269884e89d3ad42f052a802944d5e9903 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/byob ./cmd/byob
COPY internal ./internal
ENV CGO_ENABLED=0
RUN ["go", "build", "-trimpath", "-ldflags=-s -w", "-o", "/byob", "./cmd/byob"]

FROM quay.io/konflux-ci/build-trusted-artifacts:latest@sha256:adf22f3ec90bfa3f7e2c832a7d52febd1ea31aa9fff6db21324c965d7d622327 AS trusted-artifacts

# The PoC reuses SSH, Bash, tar, ORAS, Syft and Cosign from task-runner.
FROM quay.io/konflux-ci/task-runner:2.1.0@sha256:c34c933c269e2401bb042fe69e2999cf288331b6586d4f4eca9c845270d9b1f9
COPY --from=trusted-artifacts /usr/local/bin/use-archive /usr/local/bin/oras_opts.sh /usr/local/bin/select-oci-auth.sh /usr/local/bin/retry /usr/local/bin/
COPY --from=build /byob /usr/local/bin/byob
USER 1000:0
ENTRYPOINT ["/usr/local/bin/byob"]
