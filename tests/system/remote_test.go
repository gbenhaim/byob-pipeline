package system

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type taskStep struct {
	Name              string `json:"name"`
	TerminationReason string `json:"terminationReason"`
	Terminated        *struct {
		Reason   string `json:"reason"`
		ExitCode int    `json:"exitCode"`
	} `json:"terminated"`
	Results []flexibleResult `json:"results"`
}

// flexibleResult accepts a Tekton result whose value is a string or an object.
type flexibleResult struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

// stringValue returns a string result. Object results are returned as JSON.
func (r flexibleResult) stringValue() string {
	var value string
	if err := json.Unmarshal(r.Value, &value); err == nil {
		return value
	}

	return string(r.Value)
}

var _ = Describe("byob-remote pipeline", Ordered, func() {
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
		name, image = submitPipelineRun("byob-remote", gitURL, revision)
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- provisionPlatformSecret(ctx, name)
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

	It("succeeds at checkout, build, and pack", func() {
		expectTaskRunsSucceeded(name, "checkout", "build", "pack")
	})

	It("builds with run-build-remote", func() {
		ref := mustKubectl(
			"get", "taskruns", "-n", namespace,
			"-l", "tekton.dev/pipelineRun="+name+",tekton.dev/pipelineTask=build",
			"-o", "jsonpath={.items[0].spec.taskRef.name}",
		)
		Expect(strings.TrimSpace(ref)).To(Equal("run-build-remote"))
	})

	It("runs the build on the host selected for linux/arm64", func() {
		steps := buildTaskSteps(name)
		mode := stepResult(steps, "select-platform", "MODE")
		Expect(mode).To(BeElementOf("local", "remote"))
		if mode == "local" {
			Expect(stepReason(steps, "build-local")).To(Equal("Completed"))
			Expect(stepReason(steps, "build-remote")).To(Equal("Skipped"))
			return
		}

		Expect(stepReason(steps, "build-remote")).To(Equal("Completed"))
		Expect(stepReason(steps, "build-local")).To(Equal("Skipped"))
	})

	It("follows the platform secret written for the build", func() {
		taskRun, err := taskRunName(name, "build")
		Expect(err).NotTo(HaveOccurred())
		Expect(taskRun).NotTo(BeEmpty())
		encoded := strings.TrimSpace(mustKubectl(
			"get", "secret", "-n", namespace, "multi-platform-ssh-"+taskRun,
			"-o", "jsonpath={.data.host}",
		))
		host, err := base64.StdEncoding.DecodeString(encoded)
		Expect(err).NotTo(HaveOccurred())
		mode := stepResult(buildTaskSteps(name), "select-platform", "MODE")
		if string(host) == "localhost" {
			Expect(mode).To(Equal("local"))
			return
		}

		Expect(string(host)).NotTo(BeEmpty())
		Expect(mode).To(Equal("remote"))
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

// buildTaskSteps returns the build TaskRun's step statuses.
func buildTaskSteps(pipelineRun string) []taskStep {
	GinkgoHelper()
	raw := mustKubectl(
		"get", "taskruns", "-n", namespace,
		"-l", "tekton.dev/pipelineRun="+pipelineRun+",tekton.dev/pipelineTask=build",
		"-o", "json",
	)
	var body struct {
		Items []struct {
			Status struct {
				Steps []taskStep `json:"steps"`
			} `json:"status"`
		} `json:"items"`
	}
	Expect(json.Unmarshal([]byte(raw), &body)).To(Succeed())
	Expect(body.Items).To(HaveLen(1))
	return body.Items[0].Status.Steps
}

// stepResult returns one named result from a named step.
func stepResult(steps []taskStep, stepName, resultName string) string {
	GinkgoHelper()
	step := findStep(steps, stepName)
	for _, result := range step.Results {
		if result.Name == resultName {
			return result.stringValue()
		}
	}

	Fail("step " + stepName + " has no result " + resultName)
	return ""
}

// stepReason returns the Tekton termination reason for a named step.
func stepReason(steps []taskStep, stepName string) string {
	GinkgoHelper()
	step := findStep(steps, stepName)
	if step.TerminationReason != "" {
		return step.TerminationReason
	}

	if step.Terminated != nil {
		return step.Terminated.Reason
	}

	return ""
}

// findStep returns the named step or fails the spec.
func findStep(steps []taskStep, name string) taskStep {
	GinkgoHelper()
	for _, step := range steps {
		if step.Name == name {
			return step
		}
	}

	Fail("step " + name + " not found")
	return taskStep{}
}
