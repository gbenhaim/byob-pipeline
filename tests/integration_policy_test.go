package tests

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("BYOB integration policy", func() {
	It("verifies Chains signatures before it evaluates the policy", func() {
		data, err := os.ReadFile(filepath.Join("..", "examples", "integration", "byob-pipeline-policy.yaml"))
		Expect(err).NotTo(HaveOccurred())
		script := string(data)

		Expect(script).To(ContainSubstring("ec validate image"))
		Expect(script).To(ContainSubstring("--public-key /keys/cosign.pub"))
		Expect(script).To(ContainSubstring("--ignore-rekor"))
		Expect(script).To(ContainSubstring(`--output "appstudio=/tmp/ec/$$n.appstudio.json"`))
		Expect(script).To(ContainSubstring("secretName: chains-public-key"))
		Expect(script).NotTo(ContainSubstring("ec validate input"))
		Expect(script).NotTo(ContainSubstring(".payload"))
	})
})
