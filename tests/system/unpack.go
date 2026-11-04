package system

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// unpackBuildOutput runs the unpack-build-output StepAction and returns member contents.
// members are archive paths such as hello.txt or linux-amd64/hello.txt.
func unpackBuildOutput(reference string, members ...string) map[string]string {
	GinkgoHelper()
	Expect(members).NotTo(BeEmpty())
	for _, member := range members {
		checkArchiveMember(member)
	}

	name := fmt.Sprintf("unpack-%d", time.Now().UnixNano())
	mustKubectlStdin(unpackTaskRun(name, reference, members), "create", "-f", "-")
	waitTaskRun(name)

	parsed := parseUnpacked(taskRunResults(name)["FILES"])
	Expect(parsed).To(HaveLen(len(members)))
	for _, member := range members {
		Expect(parsed).To(HaveKey(member))
	}

	return parsed
}

// manifestReference drops the output tag from a published image.
// unpack accepts registry/repo@sha256:<digest> and rejects a tag.
func manifestReference(taggedImage, digest string) string {
	GinkgoHelper()
	Expect(digest).To(MatchRegexp(`^sha256:[a-f0-9]{64}$`))
	colon := strings.LastIndex(taggedImage, ":")
	Expect(colon).To(BeNumerically(">", 0))
	repository := taggedImage[:colon]
	tag := taggedImage[colon+1:]
	Expect(tag).NotTo(BeEmpty())
	Expect(tag).NotTo(ContainSubstring("/"))

	_, repo, ok := strings.Cut(repository, "/")
	Expect(ok).To(BeTrue(), "output image %q has no repository", taggedImage)
	Expect(repo).NotTo(ContainSubstring(":"))

	return repository + "@" + digest
}

// checkArchiveMember rejects paths that could escape the unpacked directory.
func checkArchiveMember(member string) {
	GinkgoHelper()
	Expect(member).NotTo(BeEmpty())
	Expect(path.Clean(member)).To(Equal(member))
	Expect(strings.HasPrefix(member, "/")).To(BeFalse())
	Expect(strings.Contains(member, "..")).To(BeFalse())
	Expect(strings.ContainsAny(member, " \t\r\n\"'`$\\")).To(BeFalse())
}

// unpackTaskRun is a TaskRun whose first step references unpack-build-output.
// The step template carries the destination, registry auth, and CA mounts.
// Tekton applies that template after it inlines the StepAction.
func unpackTaskRun(name, reference string, members []string) string {
	var memberArgs strings.Builder
	for _, member := range members {
		fmt.Fprintf(&memberArgs, "      - %q\n", member)
	}

	return fmt.Sprintf(`apiVersion: tekton.dev/v1
kind: TaskRun
metadata:
  name: %s
  namespace: %s
spec:
  serviceAccountName: byob-build
  timeout: 10m
  taskSpec:
    results:
    - name: FILES
      type: string
      description: One path=content line for each requested archive member.
      value: $(steps.show.results.FILES)
    stepTemplate:
      volumeMounts:
      - name: workdir
        mountPath: /var/workdir
      - name: registry-auth
        mountPath: /var/run/byob-registry-auth
        readOnly: true
      - name: trusted-ca
        mountPath: %s
        subPath: ca-bundle.crt
        readOnly: true
    steps:
    - name: unpack
      ref:
        name: unpack-build-output
      params:
      - name: image
        value: %q
      - name: destination
        value: /var/workdir/output
    - name: show
      image: localhost/byob-cli:devel
      imagePullPolicy: IfNotPresent
      command:
      - /bin/sh
      args:
      - -c
      - |
%s      - sh
%s      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
        seccompProfile:
          type: RuntimeDefault
      results:
      - name: FILES
        type: string
        description: One path=content line for each requested archive member.
    volumes:
    - name: workdir
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
        optional: true
        items:
        - key: ca-bundle.crt
          path: ca-bundle.crt
`, name, namespace, caPath, reference, indentBlock(unpackScript(), 8), memberArgs.String())
}

// unpackScript writes path=content lines for the archive members in "$@".
// Backticks keep the only Tekton substitution on the step result path.
func unpackScript() string {
	return strings.Join([]string{
		"set -eu",
		`out="$(step.results.FILES.path)"`,
		`: > "$out"`,
		`for member in "$@"; do`,
		"  value=`cat \"/var/workdir/output/$member\"` || exit 1",
		`  printf '%s=%s\n' "$member" "$value" >> "$out"`,
		"done",
	}, "\n")
}

// indentBlock prefixes every line so a YAML block scalar stays nested.
func indentBlock(text string, spaces int) string {
	pad := strings.Repeat(" ", spaces)
	var builder strings.Builder
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(&builder, "%s%s\n", pad, line)
	}

	return builder.String()
}

// waitTaskRun polls until the TaskRun succeeds.
func waitTaskRun(name string) {
	GinkgoHelper()
	Eventually(func() error {
		phase, message, err := taskRunStatus(name)
		if err != nil {
			return err
		}

		if phase == "True" {
			return nil
		}

		if phase == "False" {
			return StopTrying(fmt.Sprintf("TaskRun %s failed: %s\n%s", name, message, taskRunDiagnostics(name)))
		}

		return fmt.Errorf("TaskRun %s status %q: %s\n%s", name, phase, message, taskRunDiagnostics(name))
	}).WithTimeout(5 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
}

// taskRunStatus returns the Succeeded condition status and message.
func taskRunStatus(name string) (string, string, error) {
	raw, stderr, err := kubectl("get", "taskrun", "-n", namespace, name, "-o", "json")
	if err != nil {
		return "", "", fmt.Errorf("read TaskRun: %w\n%s%s", err, raw, stderr)
	}

	var body struct {
		Status struct {
			Conditions []condition `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return "", "", err
	}

	for _, item := range body.Status.Conditions {
		if item.Type == "Succeeded" {
			return item.Status, item.Message, nil
		}
	}

	return "", "Succeeded condition is absent", nil
}

// taskRunResults returns the TaskRun result map.
func taskRunResults(name string) map[string]string {
	GinkgoHelper()
	raw := mustKubectl("get", "taskrun", "-n", namespace, name, "-o", "json")
	var body struct {
		Status struct {
			Results []resultValue `json:"results"`
		} `json:"status"`
	}
	Expect(json.Unmarshal([]byte(raw), &body)).To(Succeed(), "TaskRun %s results\n%s", name, raw)

	results := map[string]string{}
	for _, item := range body.Status.Results {
		results[item.Name] = item.Value
	}

	return results
}

// parseUnpacked reads path=content lines emitted by the show step.
func parseUnpacked(raw string) map[string]string {
	GinkgoHelper()
	text := strings.TrimSpace(raw)
	Expect(text).NotTo(BeEmpty())

	parsed := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		member, content, ok := strings.Cut(line, "=")
		Expect(ok).To(BeTrue(), "unpack result line %q", line)
		_, exists := parsed[member]
		Expect(exists).To(BeFalse(), "duplicate unpack result %q", member)
		parsed[member] = content
	}

	return parsed
}

// taskRunDiagnostics returns TaskRun and Pod log text for a failure.
func taskRunDiagnostics(name string) string {
	var builder strings.Builder
	args := []string{"describe", "taskrun", "-n", namespace, name}
	stdout, stderr, err := kubectl(args...)
	fmt.Fprintf(&builder, "$ kubectl %s\n%s%s\n", strings.Join(args, " "), stdout, stderr)
	if err != nil {
		fmt.Fprintf(&builder, "error: %v\n", err)
	}

	pods, _, podErr := kubectl(
		"get", "pods", "-n", namespace,
		"-l", "tekton.dev/taskRun="+name,
		"-o", "jsonpath={.items[*].metadata.name}",
	)
	if podErr != nil {
		fmt.Fprintf(&builder, "list pods: %v\n", podErr)
		return builder.String()
	}

	for _, pod := range strings.Fields(pods) {
		logs, _, logErr := kubectl("logs", "-n", namespace, pod, "--all-containers", "--tail=80")
		fmt.Fprintf(&builder, "pod %s\n%s\n", pod, logs)
		if logErr != nil {
			fmt.Fprintf(&builder, "log error: %v\n", logErr)
		}
	}

	return builder.String()
}
