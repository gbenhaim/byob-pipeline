package system

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("byob-matrix pipeline", Ordered, func() {
	var (
		configured       bool
		gitURL, revision string
		name, image      string
	)

	BeforeAll(func() {
		configured = false
		mode, err := clusterMode()
		Expect(err).NotTo(HaveOccurred())
		Expect(mode).To(Equal("existing"))
		useKindKubeconfig()
		configured = true
		useTestNamespace()
		preflight()
		prepareNamespace(registryCA())
	})

	AfterAll(func() {
		if !configured {
			return
		}

		deleteNamespace()
	})

	It("completes the PipelineRun", func() {
		gitURL, revision = sourceRevision(repoRoot())
		name, image = submitPipelineRun("byob-matrix", gitURL, revision)
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- provisionPlatformSecrets(ctx, name, "build", 2)
		}()
		waitSucceeded(name, done)
	})

	It("records the requested Git URL", func() {
		Expect(pipelineResults(name)).To(HaveKeyWithValue("CHAINS-GIT_URL", gitURL))
	})

	It("records the exact source commit", func() {
		Expect(pipelineResults(name)).To(HaveKeyWithValue("CHAINS-GIT_COMMIT", revision))
	})

	It("records the requested image reference", func() {
		Expect(pipelineResults(name)).To(HaveKeyWithValue("IMAGE_URL", image))
	})

	It("records a sha256 image digest", func() {
		Expect(pipelineResults(name)["IMAGE_DIGEST"]).To(MatchRegexp(`^sha256:[a-f0-9]{64}$`))
	})

	It("succeeds at checkout, both builds, and pack", func() {
		expectTaskRunsSucceeded(name, "checkout", "build", "pack")
		names, err := taskRunNames(name, "build")
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(HaveLen(2))
	})

	It("builds with run-build-remote for both platforms", func() {
		raw := mustKubectl(
			"get", "taskruns", "-n", namespace,
			"-l", "tekton.dev/pipelineRun="+name+",tekton.dev/pipelineTask=build",
			"-o", "jsonpath={.items[*].spec.taskRef.name}",
		)
		fields := strings.Fields(raw)
		Expect(fields).To(HaveLen(2))
		Expect(fields).To(HaveEach("run-build-remote"))
	})

	It("binds no Git credential workspace", func() {
		expectNoGitAuth(name)
	})

	It("is labeled for the Konflux application", func() {
		labels := mustKubectl(
			"get", "pipelinerun", "-n", namespace, name,
			"-o", `jsonpath={.metadata.labels.appstudio\.openshift\.io/application}`,
		)
		Expect(strings.TrimSpace(labels)).To(Equal(applicationName()))
	})

	It("publishes hello.txt for each platform", func() {
		reference := image + "@" + pipelineResults(name)["IMAGE_DIGEST"]
		Expect(pullMember(reference, "linux-amd64/hello.txt")).To(HavePrefix("Built on "))
		Expect(pullMember(reference, "linux-arm64/hello.txt")).To(HavePrefix("Built on "))
	})
})
