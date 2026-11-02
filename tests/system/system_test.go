package system

import (
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// TestSystem runs the byob PipelineRun specs against an existing Konflux Kind cluster.
func TestSystem(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "System Suite")
}

var _ = Describe("byob pipeline", Ordered, func() {
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
		name, image = submitPipelineRun("byob", gitURL, revision)
		waitSucceeded(name)
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

	It("succeeds at checkout, build, and pack", func() {
		expectTaskRunsSucceeded(name, "checkout", "build", "pack")
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

	It("publishes hello.txt from the build", func() {
		reference := image + "@" + pipelineResults(name)["IMAGE_DIGEST"]
		Expect(pullHello(reference)).To(HavePrefix("Built on "))
	})
})
