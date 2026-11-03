package system

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	policyFailureCode = "policy.artifact_pack"
	policySourceCode  = "policy.artifact_source"
)

// installPipelinePolicy installs the Conforma policy and its integration scenario.
func installPipelinePolicy() {
	GinkgoHelper()
	expectIntegrationReady()

	root := repoRoot()
	manifest := mustKubectl(
		"create", "configmap", "byob-pipeline-policy", "-n", namespace,
		"--from-file=policy.yaml="+filepath.Join(root, "policy", "policy.yaml"),
		"--from-file=pipeline.rego="+filepath.Join(root, "policy", "rules", "pipeline.rego"),
		"--dry-run=client", "-o", "yaml",
	)
	mustKubectlStdin(manifest, "apply", "-f", "-")
	mustKubectl(
		"apply", "-n", namespace,
		"-f", filepath.Join(root, "examples", "integration", "byob-pipeline-policy.yaml"),
	)

	scenario, err := os.ReadFile(filepath.Join(root, "examples", "integration", "byob-slsa.yaml"))
	Expect(err).NotTo(HaveOccurred())
	mustKubectlStdin(strings.ReplaceAll(string(scenario), "__NAMESPACE__", namespace), "apply", "-n", namespace, "-f", "-")
}

// expectIntegrationReady checks that the integration service can create test PipelineRuns.
func expectIntegrationReady() {
	GinkgoHelper()
	replicas := strings.TrimSpace(mustKubectl(
		"get", "deploy", "-n", "integration-service", "integration-service-controller-manager",
		"-o", "jsonpath={.status.readyReplicas}",
	))
	Expect(replicas).NotTo(Or(BeEmpty(), Equal("0")), "integration-service is not ready")
}

// submitPolicySnapshot creates a Snapshot for an image that triggers the policy scenario.
func submitPolicySnapshot(image string) string {
	GinkgoHelper()
	name := fmt.Sprintf("byob-slsa-%d", time.Now().UnixNano())
	manifest := fmt.Sprintf(`apiVersion: appstudio.redhat.com/v1alpha1
kind: Snapshot
metadata:
  name: %s
  namespace: %s
  labels:
    appstudio.openshift.io/application: byob-slsa
spec:
  application: byob-slsa
  components:
  - name: byob
    containerImage: %s
`, name, namespace, image)
	mustKubectlStdin(manifest, "create", "-f", "-")
	return name
}

// waitPolicyResult waits until the scenario accepts the attestation or fails with failureCode.
// An empty failureCode requires TEST_OUTPUT result SUCCESS. A failure code requires
// result FAILURE and that code in the task logs.
func waitPolicyResult(snapshot, failureCode string) {
	GinkgoHelper()
	Eventually(func() error {
		name, status, message := policyRunStatus(snapshot)
		if name == "" {
			return fmt.Errorf("waiting for the integration PipelineRun\n%s", message)
		}

		result, resultErr := policyResult(name)
		if status == "True" && failureCode == "" {
			if resultErr != nil {
				return resultErr
			}

			if result != "SUCCESS" {
				return StopTrying(fmt.Sprintf("TEST_OUTPUT result %q\n%s", result, policyLogs(name)))
			}

			return nil
		}

		if status == "True" {
			return StopTrying(fmt.Sprintf("policy accepted a pipeline that should fail\n%s", diagnostics(name)))
		}

		if status == "False" && failureCode == "" {
			return StopTrying(fmt.Sprintf("PipelineRun %s failed: %s\n%s", name, message, diagnostics(name)))
		}

		if status == "False" {
			logs := policyLogs(name)
			if resultErr != nil {
				return fmt.Errorf("read TEST_OUTPUT: %w\n%s", resultErr, logs)
			}

			if result == "FAILURE" && strings.Contains(logs, failureCode) {
				return nil
			}

			return StopTrying(fmt.Sprintf("policy result %q failed without %s\n%s\n%s", result, failureCode, message, logs))
		}

		return fmt.Errorf("PipelineRun %s status %q: %s\n%s", name, status, message, policyLogs(name))
	}).WithTimeout(10 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
}

// policyRunStatus returns the integration PipelineRun name, Succeeded status, and message.
func policyRunStatus(snapshot string) (string, string, string) {
	stdout, stderr, err := kubectl(
		"get", "pipelineruns", "-n", namespace,
		"-l", "appstudio.openshift.io/snapshot="+snapshot+",pipelines.appstudio.openshift.io/type=test",
		"-o", "json",
	)
	if err != nil {
		return "", "", fmt.Sprintf("list integration PipelineRuns: %v\n%s%s", err, stdout, stderr)
	}

	var body struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Conditions []condition `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(stdout), &body); err != nil {
		return "", "", err.Error()
	}

	if len(body.Items) == 0 {
		return "", "", "integration PipelineRun has not been created"
	}

	item := body.Items[0]
	for _, itemCondition := range item.Status.Conditions {
		if itemCondition.Type == "Succeeded" {
			return item.Metadata.Name, itemCondition.Status, itemCondition.Message
		}
	}

	return item.Metadata.Name, "", "Succeeded condition is absent"
}

// policyResult returns the TEST_OUTPUT result field from the integration PipelineRun.
func policyResult(name string) (string, error) {
	raw, stderr, err := kubectl("get", "pipelinerun", "-n", namespace, name, "-o", "json")
	if err != nil {
		return "", fmt.Errorf("read PipelineRun: %w\n%s%s", err, raw, stderr)
	}

	var body struct {
		Status struct {
			Results []resultValue `json:"results"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return "", fmt.Errorf("decode PipelineRun: %w", err)
	}

	for _, item := range body.Status.Results {
		if item.Name != "TEST_OUTPUT" {
			continue
		}

		var report struct {
			Result string `json:"result"`
		}
		if err := json.Unmarshal([]byte(item.Value), &report); err != nil {
			return "", fmt.Errorf("decode TEST_OUTPUT: %w", err)
		}

		if report.Result == "" {
			return "", fmt.Errorf("TEST_OUTPUT has no result")
		}

		return report.Result, nil
	}

	return "", fmt.Errorf("TEST_OUTPUT is absent")
}

// policyLogs returns the integration PipelineRun logs.
func policyLogs(name string) string {
	stdout, stderr, err := kubectl(
		"logs", "-n", namespace,
		"-l", "tekton.dev/pipelineRun="+name,
		"--all-containers", "--tail=200",
	)
	if err != nil {
		return fmt.Sprintf("%s%s%v", stdout, stderr, err)
	}

	return stdout
}

// submitAttestedRun starts a byob run that overrides task and step resources.
// It returns the signed image reference, including the digest.
func submitAttestedRun(gitURL, revision string) string {
	GinkgoHelper()
	name := fmt.Sprintf("byob-policy-%d", time.Now().Unix())
	image := fmt.Sprintf("%s/%s/output:%s", registryHost, namespace, name)
	storage := fmt.Sprintf("%s/%s/ta", registryHost, namespace)
	manifest := fmt.Sprintf(`apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: %s
  namespace: %s
  labels:
    appstudio.openshift.io/application: %s
    appstudio.openshift.io/component: %s
spec:
  taskRunTemplate:
    serviceAccountName: byob-build
  pipelineRef:
    name: byob
  taskRunSpecs:
  - pipelineTaskName: build
    computeResources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: "1"
        memory: 512Mi
  params:
  - name: git-url
    value: %q
  - name: revision
    value: %q
  - name: output-image
    value: %q
  - name: oci-storage
    value: %q
`, name, namespace, applicationName(), applicationName(), gitURL, revision, image, storage)
	mustKubectlStdin(manifest, "create", "-f", "-")
	waitSucceeded(name)
	waitSigned(name)
	return attestedImage(name)
}

// waitSigned waits until Chains has signed the PipelineRun.
// A failed annotation is terminal, so the wait stops instead of polling it.
func waitSigned(name string) {
	GinkgoHelper()
	Eventually(func() error {
		signed, stderr, err := kubectl(
			"get", "pipelinerun", "-n", namespace, name,
			"-o", "jsonpath={.metadata.annotations.chains\\.tekton\\.dev/signed}",
		)
		if err != nil {
			return fmt.Errorf("read chains signature: %w\n%s", err, stderr)
		}

		switch strings.TrimSpace(signed) {
		case "true":
			return nil
		case "failed":
			return StopTrying("Chains failed to sign PipelineRun " + name + "\n" + chainsDiagnostics(name))
		default:
			return fmt.Errorf("chains.tekton.dev/signed=%q", strings.TrimSpace(signed))
		}
	}).WithTimeout(3 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
}

// chainsDiagnostics returns Chains annotations and the controller log tail.
func chainsDiagnostics(name string) string {
	var builder strings.Builder
	annotations, stderr, err := kubectl(
		"get", "pipelinerun", "-n", namespace, name,
		"-o", "jsonpath={.metadata.annotations}",
	)
	fmt.Fprintf(&builder, "annotations: %s%s\n", annotations, stderr)
	if err != nil {
		fmt.Fprintf(&builder, "read annotations: %v\n", err)
	}

	logs, logStderr, logErr := kubectl(
		"logs", "-n", "tekton-pipelines", "deploy/tekton-chains-controller", "--tail=80",
	)
	fmt.Fprintf(&builder, "chains controller:\n%s%s\n", logs, logStderr)
	if logErr != nil {
		fmt.Fprintf(&builder, "read chains logs: %v\n", logErr)
	}

	return builder.String()
}

// attestedImage returns the digest reference Chains signed for the PipelineRun.
func attestedImage(name string) string {
	GinkgoHelper()
	results := pipelineResults(name)
	url := results["IMAGE_URL"]
	digest := results["IMAGE_DIGEST"]
	repo := url
	if colon := strings.LastIndex(url, ":"); colon > strings.LastIndex(url, "/") {
		repo = url[:colon]
	}

	return repo + "@" + digest
}

// pipelineTaskResult returns one result from the TaskRun for a PipelineTask.
func pipelineTaskResult(pipelineRun, task, resultName string) string {
	GinkgoHelper()
	raw := mustKubectl(
		"get", "taskruns", "-n", namespace,
		"-l", "tekton.dev/pipelineRun="+pipelineRun+",tekton.dev/pipelineTask="+task,
		"-o", "json",
	)
	var body struct {
		Items []struct {
			Status struct {
				Results []flexibleResult `json:"results"`
			} `json:"status"`
		} `json:"items"`
	}
	Expect(json.Unmarshal([]byte(raw), &body)).To(Succeed())
	Expect(body.Items).To(HaveLen(1), "TaskRuns for %s/%s", pipelineRun, task)
	for _, result := range body.Items[0].Status.Results {
		if result.Name == resultName {
			return result.stringValue()
		}
	}

	Fail("TaskRun " + task + " has no result " + resultName)
	return ""
}

// applyForeignSource points the byob build at a trusted artifact from another run.
func applyForeignSource(reference string) {
	GinkgoHelper()
	original, err := os.ReadFile(filepath.Join(repoRoot(), "pipelines", "byob.yaml"))
	Expect(err).NotTo(HaveOccurred())
	needle := "      value: $(tasks.checkout.results.SOURCE_ARTIFACT)\n"
	replacement := fmt.Sprintf("      value: %q\n", reference)
	Expect(strings.Count(string(original), needle)).To(Equal(1))
	mustKubectlStdin(strings.Replace(string(original), needle, replacement, 1), "apply", "-n", namespace, "-f", "-")
}

// applyUnsafeArtifactType changes the pack artifact type in the byob pipeline.
func applyUnsafeArtifactType() {
	GinkgoHelper()
	original, err := os.ReadFile(filepath.Join(repoRoot(), "pipelines", "byob.yaml"))
	Expect(err).NotTo(HaveOccurred())
	unsafe := strings.ReplaceAll(
		string(original),
		"application/vnd.konflux.build-output.v1",
		"application/vnd.konflux.build-output.unsafe",
	)
	Expect(unsafe).NotTo(Equal(string(original)))
	mustKubectlStdin(unsafe, "apply", "-n", namespace, "-f", "-")
}

// restoreCatalogPipeline returns the byob pipeline to the catalog definition.
// A missing namespace is left alone. The suite deletes that namespace while
// it is cleaning up, and applying the pipeline there cannot succeed.
func restoreCatalogPipeline() {
	stdout, stderr, err := kubectl(
		"apply", "-n", namespace,
		"-f", filepath.Join(repoRoot(), "pipelines", "byob.yaml"),
	)
	if err != nil && strings.Contains(stderr, "not found") {
		return
	}

	Expect(err).NotTo(HaveOccurred(), "kubectl apply -n %s -f pipelines/byob.yaml\n%s%s", namespace, stdout, stderr)
}
