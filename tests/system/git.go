package system

import (
	"os"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// sourceRevision resolves the exact public Git commit the pipeline checks out.
func sourceRevision(root string) (string, string) {
	GinkgoHelper()
	gitURL := os.Getenv("BYOB_GIT_URL")
	if gitURL == "" {
		gitURL = "https://github.com/gbenhaim/byob-pipeline"
	}

	ref := os.Getenv("BYOB_GIT_REF")
	if ref == "" {
		cmd := exec.Command("git", "-C", root, "rev-parse", "--abbrev-ref", "HEAD")
		out, err := cmd.Output()
		Expect(err).NotTo(HaveOccurred(), "git rev-parse failed")
		ref = strings.TrimSpace(string(out))
		Expect(ref).NotTo(Equal("HEAD"), "set BYOB_GIT_REF when HEAD is detached")
	}

	cmd := exec.Command("git", "ls-remote", gitURL, "refs/heads/"+ref)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "git ls-remote %s refs/heads/%s failed: %s", gitURL, ref, out)
	fields := strings.Fields(string(out))
	Expect(fields).NotTo(BeEmpty(), "push refs/heads/%s to %s before the system test", ref, gitURL)
	Expect(fields[0]).To(MatchRegexp(`^[0-9a-f]{40}$`))
	return gitURL, fields[0]
}
