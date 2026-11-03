package system

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("byob pipeline policy", Ordered, func() {
	var configured bool

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
		installPipelinePolicy()
	})

	AfterAll(func() {
		if !configured {
			return
		}

		restoreCatalogPipeline()
		_, _, _ = kubectl(
			"delete", "snapshots", "-n", namespace,
			"-l", "appstudio.openshift.io/application=byob-slsa",
			"--ignore-not-found=true", "--wait=false",
		)
		deleteNamespace()
	})

	It("accepts the Chains attestation when resource limits are overridden", func() {
		gitURL, revision := sourceRevision(repoRoot())
		image := submitAttestedRun(gitURL, revision)
		snapshot := submitPolicySnapshot(image)
		waitPolicyResult(snapshot, "")
	})

	It("rejects an attestation that changes the pack artifact type", func() {
		DeferCleanup(restoreCatalogPipeline)
		applyUnsafeArtifactType()
		gitURL, revision := sourceRevision(repoRoot())
		image := submitAttestedRun(gitURL, revision)
		snapshot := submitPolicySnapshot(image)
		waitPolicyResult(snapshot, policyFailureCode)
	})

	It("rejects a build input from outside this build", func() {
		DeferCleanup(restoreCatalogPipeline)
		gitURL, revision := sourceRevision(repoRoot())
		producer, _ := submitPipelineRun("byob", gitURL, revision)
		waitSucceeded(producer)
		foreign := pipelineTaskResult(producer, "checkout", "SOURCE_ARTIFACT")
		Expect(foreign).To(MatchRegexp(`^oci:.+@sha256:[a-f0-9]{64}$`))
		applyForeignSource(foreign)
		image := submitAttestedRun(gitURL, revision)
		snapshot := submitPolicySnapshot(image)
		waitPolicyResult(snapshot, policySourceCode)
	})
})
