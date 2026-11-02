//go:build system

package system

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// TestSystem runs the byob PipelineRun specs against an existing Konflux Kind cluster.
func TestSystem(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "System Suite")
}

var _ = Describe("byob pipeline", func() {
	var configured bool

	BeforeEach(func() {
		configured = false
		mode, err := clusterMode()
		Expect(err).NotTo(HaveOccurred())
		Expect(mode).To(Equal("existing"))
		useKindKubeconfig()
		configured = true
		preflight()
		prepareNamespace(registryCA())
	})

	AfterEach(func() {
		if !configured {
			return
		}

		deleteNamespace()
	})

	It("checks out a GitHub commit and publishes the build output", func() {
		gitURL, revision := sourceRevision(repoRoot())
		name, image := submitPipelineRun(gitURL, revision)
		waitSucceeded(name)

		results := pipelineResults(name)
		Expect(results).To(HaveKeyWithValue("CHAINS-GIT_URL", gitURL))
		Expect(results).To(HaveKeyWithValue("CHAINS-GIT_COMMIT", revision))
		Expect(results).To(HaveKeyWithValue("IMAGE_URL", image))
		Expect(results["IMAGE_DIGEST"]).To(MatchRegexp(`^sha256:[a-f0-9]{64}$`))
		expectTaskRunsSucceeded(name)
		expectNoGitAuth(name)

		reference := image + "@" + results["IMAGE_DIGEST"]
		Expect(pullHello(reference)).To(HavePrefix("Built on "))
	})
})
