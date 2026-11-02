//go:build system

package system

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type resultValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// submitPipelineRun creates the byob PipelineRun and returns its name and output image.
func submitPipelineRun(gitURL, revision string) (string, string) {
	GinkgoHelper()
	name := fmt.Sprintf("byob-system-%d", time.Now().Unix())
	image := fmt.Sprintf("%s/%s/output:%s", registryHost, namespace, name)
	storage := fmt.Sprintf("%s/%s/ta", registryHost, namespace)
	manifest := fmt.Sprintf(`apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: %s
  namespace: %s
spec:
  taskRunTemplate:
    serviceAccountName: byob-build
  pipelineRef:
    name: byob
  params:
  - name: git-url
    value: %q
  - name: revision
    value: %q
  - name: output-image
    value: %q
  - name: oci-storage
    value: %q
`, name, namespace, gitURL, revision, image, storage)
	mustKubectlStdin(manifest, "create", "-f", "-")
	return name, image
}

// waitSucceeded polls until the PipelineRun succeeds or stops early on failure.
func waitSucceeded(name string) {
	GinkgoHelper()
	Eventually(func() error {
		phase, message := pipelineStatus(name)
		if phase == "True" {
			return nil
		}

		if phase == "False" {
			return StopTrying(fmt.Sprintf("PipelineRun %s failed: %s\n%s", name, message, diagnostics(name)))
		}

		return fmt.Errorf("PipelineRun %s status %q: %s\n%s", name, phase, message, runningPodLogs(name))
	}).WithTimeout(15 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())
}

// pipelineStatus returns the Succeeded condition status and message.
func pipelineStatus(name string) (string, string) {
	raw := mustKubectl("get", "pipelinerun", "-n", namespace, name, "-o", "json")
	var body struct {
		Status struct {
			Conditions []condition `json:"conditions"`
		} `json:"status"`
	}
	Expect(json.Unmarshal([]byte(raw), &body)).To(Succeed())
	for _, item := range body.Status.Conditions {
		if item.Type == "Succeeded" {
			return item.Status, item.Message
		}
	}

	return "", "Succeeded condition is absent"
}

// pipelineResults returns the PipelineRun result map.
func pipelineResults(name string) map[string]string {
	GinkgoHelper()
	raw := mustKubectl("get", "pipelinerun", "-n", namespace, name, "-o", "json")
	var body struct {
		Status struct {
			Results []resultValue `json:"results"`
		} `json:"status"`
	}
	Expect(json.Unmarshal([]byte(raw), &body)).To(Succeed())
	results := map[string]string{}
	for _, item := range body.Status.Results {
		results[item.Name] = item.Value
	}

	return results
}

// expectTaskRunsSucceeded requires checkout, build, and pack to succeed.
func expectTaskRunsSucceeded(name string) {
	GinkgoHelper()
	raw := mustKubectl(
		"get", "taskruns", "-n", namespace,
		"-l", "tekton.dev/pipelineRun="+name,
		"-o", "json",
	)
	var body struct {
		Items []struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Conditions []condition `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	Expect(json.Unmarshal([]byte(raw), &body)).To(Succeed())

	succeeded := map[string]bool{}
	for _, item := range body.Items {
		taskName := item.Metadata.Labels["tekton.dev/pipelineTask"]
		for _, itemCondition := range item.Status.Conditions {
			if itemCondition.Type == "Succeeded" && itemCondition.Status == "True" {
				succeeded[taskName] = true
			}
		}
	}

	for _, taskName := range []string{"checkout", "build", "pack"} {
		Expect(succeeded).To(HaveKey(taskName), "TaskRun %s did not succeed\n%s", taskName, diagnostics(name))
	}
}

// expectNoGitAuth checks that the PipelineRun binds no Git credential workspace.
func expectNoGitAuth(name string) {
	GinkgoHelper()
	workspaces := strings.TrimSpace(mustKubectl(
		"get", "pipelinerun", "-n", namespace, name,
		"-o", "jsonpath={.spec.workspaces}",
	))
	Expect(workspaces).To(BeEmpty())
}

// pullHello downloads the published artifact and returns hello.txt.
func pullHello(reference string) string {
	GinkgoHelper()
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: byob-pull
  namespace: %s
spec:
  restartPolicy: Never
  containers:
  - name: oras
    image: localhost/byob-cli:devel
    imagePullPolicy: IfNotPresent
    workingDir: /tmp
    command: ["/bin/sh", "-c"]
    args:
    - %q
    env:
    - name: REF
      value: %q
    - name: SSL_CERT_FILE
      value: %s
    securityContext:
      runAsNonRoot: true
      runAsUser: 1000
      allowPrivilegeEscalation: false
      capabilities:
        drop: ["ALL"]
      seccompProfile:
        type: RuntimeDefault
    volumeMounts:
    - name: work
      mountPath: /tmp
    - name: registry-auth
      mountPath: /var/run/byob-registry-auth
      readOnly: true
    - name: trusted-ca
      mountPath: %s
      subPath: ca-bundle.crt
      readOnly: true
  volumes:
  - name: work
    emptyDir: {}
  - name: registry-auth
    secret:
      secretName: byob-registry-auth
      optional: true
      items:
      - key: .dockerconfigjson
        path: config.json
  - name: trusted-ca
    configMap:
      name: trusted-ca
      items:
      - key: ca-bundle.crt
        path: ca-bundle.crt
`, namespace, `if [ -s /var/run/byob-registry-auth/config.json ]; then export DOCKER_CONFIG=/var/run/byob-registry-auth; fi; oras pull "$REF" >/tmp/oras.log 2>&1 || { cat /tmp/oras.log >&2; exit 1; }; tar -xOf payload.tar.gz hello.txt`, reference, caPath, caPath)
	mustKubectlStdin(manifest, "apply", "-f", "-")

	Eventually(func() error {
		stdout, stderr, err := kubectl(
			"get", "pod", "-n", namespace, "byob-pull",
			"-o", "jsonpath={.status.phase}",
		)
		if err != nil {
			return fmt.Errorf("read pull pod: %w\n%s%s", err, stdout, stderr)
		}

		phase := strings.TrimSpace(stdout)
		if phase == "Succeeded" {
			return nil
		}

		if phase == "Failed" {
			logs, _, _ := kubectl("logs", "-n", namespace, "byob-pull")
			described, _, _ := kubectl("describe", "pod", "-n", namespace, "byob-pull")
			return StopTrying(fmt.Sprintf("pull pod failed\n%s\n%s", described, logs))
		}

		return fmt.Errorf("pull pod phase %s", phase)
	}).WithTimeout(5 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

	return mustKubectl("logs", "-n", namespace, "byob-pull")
}

// runningPodLogs returns a short tail of each PipelineRun pod log.
func runningPodLogs(name string) string {
	stdout, _, err := kubectl(
		"get", "pods", "-n", namespace,
		"-l", "tekton.dev/pipelineRun="+name,
		"-o", "jsonpath={.items[*].metadata.name}",
	)
	if err != nil {
		return err.Error()
	}

	var builder strings.Builder
	for _, pod := range strings.Fields(stdout) {
		logs, _, logErr := kubectl("logs", "-n", namespace, pod, "--all-containers", "--tail=30")
		fmt.Fprintf(&builder, "pod %s\n%s\n", pod, logs)
		if logErr != nil {
			fmt.Fprintf(&builder, "log error: %v\n", logErr)
		}
	}

	return builder.String()
}

// diagnostics returns PipelineRun, TaskRun, and Pod log text for a failure.
func diagnostics(name string) string {
	var builder strings.Builder
	for _, args := range [][]string{
		{"describe", "pipelinerun", "-n", namespace, name},
		{"get", "taskruns", "-n", namespace, "-l", "tekton.dev/pipelineRun=" + name, "-o", "yaml"},
	} {
		stdout, stderr, err := kubectl(args...)
		fmt.Fprintf(&builder, "$ kubectl %s\n%s%s\n", strings.Join(args, " "), stdout, stderr)
		if err != nil {
			fmt.Fprintf(&builder, "error: %v\n", err)
		}
	}

	stdout, _, err := kubectl(
		"get", "pods", "-n", namespace,
		"-l", "tekton.dev/pipelineRun="+name,
		"-o", "jsonpath={.items[*].metadata.name}",
	)
	if err != nil {
		return builder.String()
	}

	for _, pod := range strings.Fields(stdout) {
		logs, _, logErr := kubectl("logs", "-n", namespace, pod, "--all-containers", "--tail=80")
		fmt.Fprintf(&builder, "pod %s\n%s\n", pod, logs)
		if logErr != nil {
			fmt.Fprintf(&builder, "log error: %v\n", logErr)
		}
	}

	return builder.String()
}
