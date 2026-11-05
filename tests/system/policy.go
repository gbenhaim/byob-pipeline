package system

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	installChainsPublicKey()
	mustKubectl(
		"apply", "-n", namespace,
		"-f", filepath.Join(root, "examples", "integration", "byob-pipeline-policy.yaml"),
	)

	scenario, err := os.ReadFile(filepath.Join(root, "examples", "integration", "byob-slsa.yaml"))
	Expect(err).NotTo(HaveOccurred())
	mustKubectlStdin(strings.ReplaceAll(string(scenario), "__NAMESPACE__", namespace), "apply", "-n", namespace, "-f", "-")
}

// installChainsPublicKey copies the Chains public key into the test namespace.
// The policy task verifies signatures with this key and does not receive the
// signing private key.
func installChainsPublicKey() {
	GinkgoHelper()
	encoded := strings.TrimSpace(mustKubectl(
		"get", "secret", "-n", "openshift-pipelines", "public-key",
		"-o", `jsonpath={.data.cosign\.pub}`,
	))
	Expect(encoded).NotTo(BeEmpty(), "openshift-pipelines/public-key has no cosign.pub")
	pem, err := base64.StdEncoding.DecodeString(encoded)
	Expect(err).NotTo(HaveOccurred(), "Chains public key is not base64")
	Expect(string(pem)).To(ContainSubstring("BEGIN PUBLIC KEY"))

	path := filepath.Join(GinkgoT().TempDir(), "cosign.pub")
	Expect(os.WriteFile(path, pem, 0600)).To(Succeed())
	manifest := mustKubectl(
		"create", "secret", "generic", "chains-public-key", "-n", namespace,
		"--from-file=cosign.pub="+path,
		"--dry-run=client", "-o", "yaml",
	)
	mustKubectlStdin(manifest, "apply", "-f", "-")
}

// pushUnsignedImage publishes an image with no signature or attestation.
// It returns the digest reference.
func pushUnsignedImage(tag string) string {
	GinkgoHelper()
	script := `
set -eu
if [ -s /var/run/byob-registry-auth/config.json ]; then
  export DOCKER_CONFIG=/var/run/byob-registry-auth
fi
printf 'unsigned\n' > unsigned.txt
out=$(oras push "$REPO:$TAG" unsigned.txt 2>&1)
printf '%s\n' "$out" >&2
digest=$(printf '%s\n' "$out" | awk '/^Digest: / {print $2}')
test -n "$digest"
printf 'BYOB_DIGEST %s\n' "$digest"
`
	logs := runRegistryScript("byob-reg-unsigned", script, map[string]string{
		"REPO": registryRepository("unsigned"),
		"TAG":  tag,
	})
	return registryRepository("unsigned") + "@" + digestFromLogs(logs)
}

// pushImageWithBadAttestation publishes an image whose attestation signature
// does not verify. It returns the digest reference.
func pushImageWithBadAttestation(tag string) string {
	GinkgoHelper()
	script := `
set -eu
if [ -s /var/run/byob-registry-auth/config.json ]; then
  export DOCKER_CONFIG=/var/run/byob-registry-auth
fi
printf 'bad-signature\n' > payload.txt
out=$(oras push "$REPO:$TAG" payload.txt 2>&1)
printf '%s\n' "$out" >&2
digest=$(printf '%s\n' "$out" | awk '/^Digest: / {print $2}')
test -n "$digest"
payload=$(printf '%s' '{"subject":[{"name":"untrusted"}]}' | base64 -w 0)
jq -n --arg payload "$payload" \
  '{payloadType:"application/vnd.in-toto+json",payload:$payload,signatures:[{keyid:"untrusted",sig:"AAAA"}]}' \
  > envelope.json
envelope_digest=sha256:$(sha256sum envelope.json | awk '{print $1}')
envelope_size=$(wc -c < envelope.json | tr -d ' ')
oras blob push "$REPO@$envelope_digest" envelope.json
printf '{}' > empty.json
empty_digest=sha256:$(sha256sum empty.json | awk '{print $1}')
empty_size=$(wc -c < empty.json | tr -d ' ')
oras blob push "$REPO@$empty_digest" empty.json
jq -n \
  --arg config "$empty_digest" --argjson configSize "$empty_size" \
  --arg layer "$envelope_digest" --argjson layerSize "$envelope_size" \
  '{
    schemaVersion: 2,
    mediaType: "application/vnd.oci.image.manifest.v1+json",
    config: {mediaType: "application/vnd.oci.empty.v1+json", digest: $config, size: $configSize},
    layers: [{mediaType: "application/vnd.dsse.envelope.v1+json", digest: $layer, size: $layerSize}]
  }' > manifest.json
hex=${digest#sha256:}
oras manifest push "$REPO:sha256-${hex}.att" manifest.json
printf 'BYOB_DIGEST %s\n' "$digest"
`
	logs := runRegistryScript("byob-reg-bad-signature", script, map[string]string{
		"REPO": registryRepository("bad-signature"),
		"TAG":  tag,
	})
	return registryRepository("bad-signature") + "@" + digestFromLogs(logs)
}

// pushImageWithForeignAttestation publishes an image and attaches a copy of
// source's signed attestation. source is a digest reference. The returned
// reference is the new image.
func pushImageWithForeignAttestation(tag, source string) string {
	GinkgoHelper()
	repository, digest := splitDigestReference(source)
	script := `
set -eu
if [ -s /var/run/byob-registry-auth/config.json ]; then
  export DOCKER_CONFIG=/var/run/byob-registry-auth
fi
printf 'other-image\n' > other.txt
out=$(oras push "$REPO:$TAG" other.txt 2>&1)
printf '%s\n' "$out" >&2
digest=$(printf '%s\n' "$out" | awk '/^Digest: / {print $2}')
test -n "$digest"
src_hex=${SOURCE_DIGEST#sha256:}
dst_hex=${digest#sha256:}
oras copy "$SOURCE_REPOSITORY:sha256-${src_hex}.att" "$REPO:sha256-${dst_hex}.att"
printf 'BYOB_DIGEST %s\n' "$digest"
`
	logs := runRegistryScript("byob-reg-foreign-att", script, map[string]string{
		"REPO":              registryRepository("foreign-att"),
		"TAG":               tag,
		"SOURCE_REPOSITORY": repository,
		"SOURCE_DIGEST":     digest,
	})
	return registryRepository("foreign-att") + "@" + digestFromLogs(logs)
}

// registryRepository returns the test repository for one registry fixture.
func registryRepository(name string) string {
	return fmt.Sprintf("%s/%s/%s", registryHost, namespace, name)
}

// digestFromLogs returns the digest printed by a registry fixture script.
func digestFromLogs(logs string) string {
	GinkgoHelper()
	var digest string
	for _, line := range strings.Split(logs, "\n") {
		if value, ok := strings.CutPrefix(line, "BYOB_DIGEST "); ok {
			digest = strings.TrimSpace(value)
		}
	}

	Expect(digest).To(MatchRegexp(`^sha256:[a-f0-9]{64}$`), "registry script logs:\n%s", logs)
	return digest
}

// splitDigestReference splits repository@sha256:digest.
func splitDigestReference(reference string) (string, string) {
	GinkgoHelper()
	repository, digest, ok := strings.Cut(reference, "@")
	Expect(ok).To(BeTrue(), "digest reference %q", reference)
	Expect(digest).To(MatchRegexp(`^sha256:[a-f0-9]{64}$`))
	Expect(repository).NotTo(BeEmpty())
	return repository, digest
}

// runRegistryScript runs script in a short-lived registry client and returns its logs.
func runRegistryScript(name, script string, env map[string]string) string {
	GinkgoHelper()
	var envYAML strings.Builder
	names := make([]string, 0, len(env))
	for key := range env {
		names = append(names, key)
	}
	slices.Sort(names)
	for _, key := range names {
		fmt.Fprintf(&envYAML, "    - name: %s\n      value: %q\n", key, env[key])
	}

	manifest := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
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
%s    - name: SSL_CERT_FILE
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
`, name, namespace, script, envYAML.String(), caPath, caPath)
	_, _, _ = kubectl("delete", "pod", "-n", namespace, name, "--ignore-not-found=true", "--wait=true")
	mustKubectlStdin(manifest, "apply", "-f", "-")

	Eventually(func() error {
		stdout, stderr, err := kubectl(
			"get", "pod", "-n", namespace, name,
			"-o", "jsonpath={.status.phase}",
		)
		if err != nil {
			return fmt.Errorf("read registry pod: %w\n%s%s", err, stdout, stderr)
		}

		phase := strings.TrimSpace(stdout)
		if phase == "Succeeded" {
			return nil
		}

		if phase == "Failed" {
			logs, _, _ := kubectl("logs", "-n", namespace, name)
			described, _, _ := kubectl("describe", "pod", "-n", namespace, name)
			return StopTrying(fmt.Sprintf("registry pod failed\n%s\n%s", described, logs))
		}

		return fmt.Errorf("registry pod phase %s", phase)
	}).WithTimeout(5 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

	return mustKubectl("logs", "-n", namespace, name)
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
