package system

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	defaultNamespace   = "byob-system"
	defaultApplication = "sample-component"
	// registryHost matches the Kind registry certificate. That certificate is
	// not valid for the svc.cluster.local form of this name.
	registryHost = "registry-service.kind-registry"
	caPath       = "/etc/pki/tls/certs/ca-custom-bundle.crt"
)

// namespace is the namespace the suite uses. useTestNamespace sets it.
var namespace = defaultNamespace

// clusterMode returns existing, which is the only mode the test accepts.
func clusterMode() (string, error) {
	switch os.Getenv("BYOB_CLUSTER") {
	case "", "existing":
		return "existing", nil
	case "install":
		return "", errors.New("BYOB_CLUSTER=install does not install a cluster. Run the Konflux Kind setup, then rerun with BYOB_CLUSTER=existing")
	default:
		return "", fmt.Errorf("BYOB_CLUSTER must be existing or install, got %q", os.Getenv("BYOB_CLUSTER"))
	}
}

// kindClusterName returns BYOB_KIND_CLUSTER or the only Kind cluster.
func kindClusterName() (string, error) {
	if name := strings.TrimSpace(os.Getenv("BYOB_KIND_CLUSTER")); name != "" {
		return name, nil
	}

	cmd := exec.Command("kind", "get", "clusters")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("kind get clusters: %w", err)
	}

	var clusters []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			clusters = append(clusters, line)
		}
	}

	if len(clusters) != 1 {
		return "", fmt.Errorf("set BYOB_KIND_CLUSTER; kind get clusters returned %q", clusters)
	}

	return clusters[0], nil
}

// useTestNamespace selects BYOB_NAMESPACE, or the default namespace the suite creates.
func useTestNamespace() {
	if name := strings.TrimSpace(os.Getenv("BYOB_NAMESPACE")); name != "" {
		namespace = name
	}
}

// providedNamespace reports whether the caller chose an existing namespace.
func providedNamespace() bool {
	return strings.TrimSpace(os.Getenv("BYOB_NAMESPACE")) != ""
}

// applicationName returns the Konflux application and component label value.
func applicationName() string {
	if name := strings.TrimSpace(os.Getenv("BYOB_APPLICATION")); name != "" {
		return name
	}

	return defaultApplication
}

// useKindKubeconfig points kubectl at the selected Kind cluster.
func useKindKubeconfig() {
	GinkgoHelper()
	name, err := kindClusterName()
	Expect(err).NotTo(HaveOccurred())

	cmd := exec.Command("kind", "get", "kubeconfig", "--name", name)
	out, err := cmd.Output()
	Expect(err).NotTo(HaveOccurred(), "kind get kubeconfig --name %s failed", name)
	path := filepath.Join(GinkgoT().TempDir(), "kubeconfig")
	Expect(os.WriteFile(path, out, 0600)).To(Succeed())
	GinkgoT().Setenv("KUBECONFIG", path)
	GinkgoT().Setenv("BYOB_KIND_CLUSTER", name)
}

// kubectl runs kubectl and returns its output.
func kubectl(args ...string) (string, string, error) {
	global := []string{}
	if contextName := os.Getenv("BYOB_KUBE_CONTEXT"); contextName != "" {
		global = append(global, "--context", contextName)
	}

	cmd := exec.Command("kubectl", append(global, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// mustKubectl runs kubectl and fails the spec when the command fails.
func mustKubectl(args ...string) string {
	GinkgoHelper()
	stdout, stderr, err := kubectl(args...)
	Expect(err).NotTo(HaveOccurred(), "kubectl %s\n%s%s", strings.Join(args, " "), stdout, stderr)
	return stdout
}

// kubectlStdin runs kubectl with manifest YAML on standard input.
func kubectlStdin(manifest string, args ...string) (string, string, error) {
	global := []string{}
	if contextName := os.Getenv("BYOB_KUBE_CONTEXT"); contextName != "" {
		global = append(global, "--context", contextName)
	}

	cmd := exec.Command("kubectl", append(global, args...)...)
	cmd.Stdin = strings.NewReader(manifest)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// mustKubectlStdin runs kubectl with manifest YAML and fails the spec on error.
func mustKubectlStdin(manifest string, args ...string) string {
	GinkgoHelper()
	stdout, stderr, err := kubectlStdin(manifest, args...)
	Expect(err).NotTo(HaveOccurred(), "kubectl %s\n%s%s", strings.Join(args, " "), stdout, stderr)
	return stdout
}

// preflight checks that Konflux, Tekton, and the internal registry are ready.
func preflight() {
	GinkgoHelper()
	ready := mustKubectl("get", "konflux", "konflux", "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
	Expect(strings.TrimSpace(ready)).To(Equal("True"), "Konflux is not Ready")

	for _, deploy := range []string{"tekton-pipelines-controller", "tekton-pipelines-remote-resolvers"} {
		replicas := strings.TrimSpace(mustKubectl(
			"get", "deploy", "-n", "tekton-pipelines", deploy,
			"-o", "jsonpath={.status.readyReplicas}",
		))
		Expect(replicas).NotTo(Or(BeEmpty(), Equal("0")), "deployment %s is not ready", deploy)
	}

	mustKubectl("get", "svc", "-n", "kind-registry", "registry-service")
}

// registryCA returns the Kind registry CA bundle in PEM form.
func registryCA() string {
	GinkgoHelper()
	encoded := strings.TrimSpace(mustKubectl(
		"get", "secret", "-n", "kind-registry", "local-registry-tls",
		"-o", `jsonpath={.data.ca\.crt}`,
	))
	Expect(encoded).NotTo(BeEmpty(), "secret kind-registry/local-registry-tls has no ca.crt")
	pem, err := base64.StdEncoding.DecodeString(encoded)
	Expect(err).NotTo(HaveOccurred(), "registry CA is not base64")
	Expect(string(pem)).To(ContainSubstring("BEGIN CERTIFICATE"))
	return string(pem)
}

// repoRoot returns the catalog directory that contains the byob pipeline.
func repoRoot() string {
	GinkgoHelper()
	dir, err := os.Getwd()
	Expect(err).NotTo(HaveOccurred())
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "pipelines", "byob.yaml")); statErr == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		Expect(parent).NotTo(Equal(dir), "pipelines/byob.yaml not found from %s", dir)
		dir = parent
	}
}

// prepareNamespace installs the registry CA and catalog objects.
// A caller-provided namespace is left in place and must already exist.
func prepareNamespace(caPEM string) {
	GinkgoHelper()
	if providedNamespace() {
		mustKubectl("get", "namespace", namespace)
	} else {
		_, _, err := kubectl("delete", "namespace", namespace, "--wait=true", "--ignore-not-found=true")
		Expect(err).NotTo(HaveOccurred())
		mustKubectl("create", "namespace", namespace)
	}

	applyTrustedCA(namespace, caPEM)
	applyTrustedCA("tekton-pipelines", caPEM)

	root := repoRoot()
	mustKubectl(
		"apply", "-n", namespace,
		"-f", filepath.Join(root, "examples/platform/serviceaccounts.yaml"),
		"-f", filepath.Join(root, "task/run-build/0.1/run-build.yaml"),
		"-f", filepath.Join(root, "task/pack/0.1/pack.yaml"),
		"-f", filepath.Join(root, "task/run-build-remote/0.1/run-build-remote.yaml"),
		"-f", filepath.Join(root, "pipelines/byob.yaml"),
		"-f", filepath.Join(root, "pipelines/byob-chained.yaml"),
		"-f", filepath.Join(root, "pipelines/byob-remote.yaml"),
		"-f", filepath.Join(root, "pipelines/byob-matrix.yaml"),
	)
	installRegistryAuth()
}

// applyTrustedCA merges the registry CA into trusted-ca in targetNamespace.
// Trust-manager creates that ConfigMap when a namespace appears, so the first
// apply can lose the create race. A missing ConfigMap outside the test
// namespace is left alone, because replacing the controller bundle with only
// the registry CA would drop the public roots Chains uses.
func applyTrustedCA(targetNamespace, caPEM string) {
	GinkgoHelper()
	marker := strings.TrimSpace(caPEM)
	var lastErr error

	for attempt := 0; attempt < 15; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Second)
		}

		bundle, found := readTrustedCA(targetNamespace)
		if !found && targetNamespace != namespace {
			return
		}

		if !found {
			bundle = caPEM
		} else {
			bundle = mergeTrustedCA(bundle, caPEM)
		}

		caFile := filepath.Join(GinkgoT().TempDir(), "ca.crt")
		if err := os.WriteFile(caFile, []byte(bundle), 0644); err != nil {
			lastErr = err
			continue
		}

		manifest, stderr, err := kubectl(
			"create", "configmap", "trusted-ca", "-n", targetNamespace,
			"--from-file=ca-bundle.crt="+caFile,
			"--dry-run=client", "-o", "yaml",
		)
		if err != nil {
			Expect(err).NotTo(HaveOccurred(), "render trusted-ca: %s", stderr)
		}

		_, applyStderr, applyErr := kubectlStdin(manifest, "apply", "-f", "-")
		if applyErr != nil {
			if strings.Contains(applyStderr, "AlreadyExists") {
				lastErr = fmt.Errorf("apply trusted-ca in %s: %s", targetNamespace, applyStderr)
				continue
			}

			Expect(applyErr).NotTo(HaveOccurred(), "apply trusted-ca in %s\n%s", targetNamespace, applyStderr)
		}

		live, liveFound := readTrustedCA(targetNamespace)
		if liveFound && strings.Contains(live, marker) {
			return
		}

		lastErr = fmt.Errorf("trusted-ca in %s does not contain the registry CA", targetNamespace)
	}

	Expect(lastErr).NotTo(HaveOccurred())
}

// readTrustedCA returns the trusted-ca bundle and whether the ConfigMap exists.
func readTrustedCA(targetNamespace string) (string, bool) {
	GinkgoHelper()
	stdout, stderr, err := kubectl(
		"get", "configmap", "-n", targetNamespace, "trusted-ca",
		"-o", `jsonpath={.data.ca-bundle\.crt}`,
	)
	if err != nil {
		Expect(stderr).To(ContainSubstring("NotFound"), "read trusted-ca: %s%s", stdout, stderr)
		return "", false
	}

	return stdout, true
}

// mergeTrustedCA appends caPEM when the bundle does not already contain it.
func mergeTrustedCA(bundle, caPEM string) string {
	if strings.TrimSpace(caPEM) == "" || strings.Contains(bundle, strings.TrimSpace(caPEM)) {
		return bundle
	}

	if bundle != "" && !strings.HasSuffix(bundle, "\n") {
		bundle += "\n"
	}

	return bundle + caPEM
}

// installRegistryAuth copies the Kind registry client credential into the test
// namespace and attaches it to byob-build. The local Zot registry requires
// that credential for Trusted Artifact pushes. An anonymous registry has no
// client Secret, and this function leaves the namespace without one.
func installRegistryAuth() {
	GinkgoHelper()
	stdout, stderr, err := kubectl(
		"get", "secret", "-n", "kind-registry", "internal-registry-client-auth", "-o", "json",
	)
	if err != nil {
		Expect(stderr).To(ContainSubstring("NotFound"), "read registry client auth: %s%s", stdout, stderr)
		return
	}

	var secret map[string]any
	Expect(json.Unmarshal([]byte(stdout), &secret)).To(Succeed())
	secret["metadata"] = map[string]any{
		"name":      "byob-registry-auth",
		"namespace": namespace,
	}
	delete(secret, "status")
	manifest, err := json.Marshal(secret)
	Expect(err).NotTo(HaveOccurred())

	applied, applyStderr, applyErr := kubectlStdin(string(manifest), "apply", "-f", "-")
	Expect(applyErr).NotTo(HaveOccurred(), "apply registry auth\n%s%s", applied, applyStderr)
	mustKubectl(
		"patch", "serviceaccount", "-n", namespace, "byob-build",
		"--type=merge",
		"-p", `{"secrets":[{"name":"byob-registry-auth"}],"imagePullSecrets":[{"name":"byob-registry-auth"}]}`,
	)
}

// deleteNamespace removes the suite-created namespace.
// BYOB_NAMESPACE and BYOB_SYSTEM_KEEP=1 both leave the namespace in place.
func deleteNamespace() {
	if os.Getenv("BYOB_SYSTEM_KEEP") == "1" || providedNamespace() {
		return
	}

	_, _, _ = kubectl("delete", "namespace", namespace, "--wait=true", "--ignore-not-found=true")
}
