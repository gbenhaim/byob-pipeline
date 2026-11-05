package tests

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

// demoCommand executes the real demo renderer without contacting a cluster.
func demoCommand(args ...string) ([]byte, error) {
	cmd := exec.Command("bash", append([]string{"../scripts/demo.sh"}, args...)...)
	cmd.Env = append(os.Environ(),
		"BYOB_NAMESPACE=byob-demo-test",
		"BYOB_REVISION=0123456789abcdef0123456789abcdef01234567",
		"BYOB_GIT_URL=https://github.com/example/component",
	)

	return cmd.CombinedOutput()
}

// decodeDemo converts a rendered resource into generic YAML for boundary assertions.
func decodeDemo(data []byte) map[string]any {
	GinkgoHelper()
	var body map[string]any
	Expect(yaml.Unmarshal(data, &body)).To(Succeed(), string(data))

	return body
}

var _ = Describe("presentation demo", func() {
	It("preserves the CLI build as the default make target", func() {
		data, err := exec.Command("make", "--dry-run", "-C", "..").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(data))
		Expect(string(data)).To(ContainSubstring("go build"))
		Expect(string(data)).NotTo(ContainSubstring("serve-presentation"))
	})

	DescribeTable("refuses to prepare another context over existing evidence before touching Kubernetes",
		func(action string) {
			Expect(os.MkdirAll("../dist/demo", 0755)).To(Succeed())
			dir, err := os.MkdirTemp("../dist/demo", "byob-demo-test-context-")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(os.RemoveAll, dir)
			Expect(os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"context":"kind-original"}`), 0600)).To(Succeed())
			fixtures := GinkgoT().TempDir()
			marker := filepath.Join(fixtures, "kubectl-called")
			fixture := "#!/bin/sh\nprintf called > \"$DEMO_KUBECTL_MARKER\"\nexit 41\n"
			Expect(os.WriteFile(filepath.Join(fixtures, "kubectl"), []byte(fixture), 0755)).To(Succeed())
			cmd := exec.Command("bash", "../scripts/demo.sh", action)
			cmd.Env = append(os.Environ(),
				"PATH="+fixtures+":"+os.Getenv("PATH"),
				"DEMO_KUBECTL_MARKER="+marker,
				"BYOB_NAMESPACE="+filepath.Base(dir),
				"BYOB_KIND_CLUSTER=different",
			)
			data, err := cmd.CombinedOutput()
			Expect(err).To(HaveOccurred())
			Expect(string(data)).To(ContainSubstring("evidence belongs to another context"))
			_, err = os.Stat(marker)
			Expect(os.IsNotExist(err)).To(BeTrue(), "kubectl was invoked before the context check")
			state, err := os.ReadFile(filepath.Join(dir, "state.json"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(state)).To(Equal(`{"context":"kind-original"}`))
		},
		Entry("prepare", "prepare"),
		Entry("all stages", "all"),
		Entry("UI access", "ui"),
	)

	DescribeTable("labels build and release runs for the Konflux application activity view",
		func(resource, runType string) {
			data, err := demoCommand("render", resource,
				"registry.example/team/output@sha256:"+strings.Repeat("a", 64))
			Expect(err).NotTo(HaveOccurred(), string(data))
			metadata := decodeDemo(data)["metadata"].(map[string]any)
			Expect(metadata["labels"]).To(HaveKeyWithValue("appstudio.openshift.io/application", "byob-slsa"))
			Expect(metadata["labels"]).To(HaveKeyWithValue("pipelines.appstudio.openshift.io/type", runType))
		},
		Entry("build", "build", "build"),
		Entry("release", "release", "release"),
	)

	It("grants only the configured UI user the namespace viewer role", func() {
		cmd := exec.Command("bash", "../scripts/demo.sh", "render", "ui-access")
		cmd.Env = append(os.Environ(), "BYOB_NAMESPACE=byob-demo-test", "BYOB_UI_USER=presenter@konflux.dev")
		data, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(data))
		body := decodeDemo(data)
		Expect(body["kind"]).To(Equal("RoleBinding"))
		Expect(body["metadata"]).To(HaveKeyWithValue("namespace", "byob-demo-test"))
		Expect(body["roleRef"]).To(Equal(map[string]any{
			"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "konflux-viewer-user-actions",
		}))
		Expect(body["subjects"]).To(Equal([]any{map[string]any{
			"apiGroup": "rbac.authorization.k8s.io", "kind": "User", "name": "presenter@konflux.dev",
		}}))
	})

	DescribeTable("connects only an owned demo namespace to the UI without starting new runs",
		func(owner string, allowed bool) {
			Expect(os.MkdirAll("../dist/demo", 0755)).To(Succeed())
			dir, err := os.MkdirTemp("../dist/demo", "byob-demo-test-ui-")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(os.RemoveAll, dir)
			state := `{"context":"kind-ui","good_build":"recorded-build","good_test":"recorded-test","good_release":"recorded-release"}`
			Expect(os.WriteFile(filepath.Join(dir, "state.json"), []byte(state), 0600)).To(Succeed())
			fixtures := GinkgoT().TempDir()
			calls := filepath.Join(fixtures, "calls")
			access := filepath.Join(fixtures, "access.json")
			fixture := `#!/bin/sh
printf '%s\n' "$*" >> "$DEMO_CALLS"
case "$*" in
  *" get namespace "*) printf '%s' "$DEMO_OWNER" ;;
  *" apply -f -") cat > "$DEMO_ACCESS" ;;
  *" label "*) ;;
  *) exit 42 ;;
esac
`
			Expect(os.WriteFile(filepath.Join(fixtures, "kubectl"), []byte(fixture), 0755)).To(Succeed())
			cmd := exec.Command("bash", "../scripts/demo.sh", "ui")
			cmd.Env = append(os.Environ(), "PATH="+fixtures+":"+os.Getenv("PATH"),
				"BYOB_NAMESPACE="+filepath.Base(dir), "BYOB_KIND_CLUSTER=ui", "BYOB_UI_USER=presenter@konflux.dev",
				"BYOB_UI_URL=https://localhost:9443", "DEMO_CALLS="+calls, "DEMO_ACCESS="+access, "DEMO_OWNER="+owner)
			data, err := cmd.CombinedOutput()
			if !allowed {
				Expect(err).To(HaveOccurred())
				Expect(string(data)).To(ContainSubstring("demo ownership label"))
			} else {
				Expect(err).NotTo(HaveOccurred(), string(data))
				Expect(string(data)).To(ContainSubstring("https://localhost:9443/ns/" + filepath.Base(dir) +
					"/applications/byob-slsa/pipelineruns/recorded-build"))
				Expect(string(data)).To(ContainSubstring("/pipelineruns/recorded-test"))
				Expect(string(data)).To(ContainSubstring("/pipelineruns/recorded-release"))
				binding, readErr := os.ReadFile(access)
				Expect(readErr).NotTo(HaveOccurred())
				Expect(decodeDemo(binding)["subjects"]).To(ContainElement(HaveKeyWithValue("name", "presenter@konflux.dev")))
			}
			data, err = os.ReadFile(calls)
			Expect(err).NotTo(HaveOccurred())
			commands := strings.Split(strings.TrimSpace(string(data)), "\n")
			for _, command := range commands {
				Expect(command).To(HavePrefix("--context kind-ui --namespace " + filepath.Base(dir) + " "))
				Expect(command).NotTo(ContainSubstring(" create "))
			}
			if allowed {
				Expect(string(data)).To(ContainSubstring("konflux-ci.dev/type=tenant"))
				Expect(string(data)).To(ContainSubstring("pipelines.appstudio.openshift.io/type=build"))
				Expect(string(data)).To(ContainSubstring("pipelines.appstudio.openshift.io/type=release"))
				for _, command := range commands {
					if strings.Contains(command, " label pipelineruns ") {
						Expect(command).To(ContainSubstring("byob.demo/owned=true"))
					}
				}
			} else {
				Expect(commands).To(HaveLen(1), "unowned namespaces must not be mutated")
			}
		},
		Entry("owned namespace", "true", true),
		Entry("unowned namespace", "", false),
	)

	It("runs the catalog pipeline at an exact source revision without execution overrides", func() {
		data, err := demoCommand("render", "build")
		Expect(err).NotTo(HaveOccurred(), string(data))
		body := decodeDemo(data)
		spec := body["spec"].(map[string]any)
		Expect(body["kind"]).To(Equal("PipelineRun"))
		Expect(spec["pipelineRef"]).To(Equal(map[string]any{"name": "byob"}))
		Expect(spec).NotTo(HaveKey("pipelineSpec"))
		Expect(spec).NotTo(HaveKey("taskRunSpecs"))
		Expect(spec["params"]).To(ContainElement(map[string]any{
			"name": "revision", "value": "0123456789abcdef0123456789abcdef01234567",
		}))
	})

	It("passes the same digest to the integration Snapshot and the release Pipeline", func() {
		image := "registry.example:5000/team/output@sha256:" + strings.Repeat("a", 64)
		data, err := demoCommand("render", "snapshot", image)
		Expect(err).NotTo(HaveOccurred(), string(data))
		snapshot := decodeDemo(data)["spec"].(map[string]any)
		components := snapshot["components"].([]any)
		Expect(components[0].(map[string]any)["containerImage"]).To(Equal(image))

		data, err = demoCommand("render", "release", image)
		Expect(err).NotTo(HaveOccurred(), string(data))
		release := decodeDemo(data)["spec"].(map[string]any)
		Expect(release["pipelineRef"]).To(Equal(map[string]any{"name": "byob-demo-release"}))
		Expect(release["params"]).To(ContainElement(map[string]any{"name": "image", "value": image}))
	})

	DescribeTable("rejects mutable or malformed release identities before contacting Kubernetes",
		func(image string) {
			for _, resource := range []string{"snapshot", "release"} {
				data, err := demoCommand("render", resource, image)
				Expect(err).To(HaveOccurred())
				Expect(string(data)).To(ContainSubstring("digest reference"))
			}
		},
		Entry("tag", "registry.example/team/output:latest"),
		Entry("short digest", "registry.example/team/output@sha256:abc"),
		Entry("tag and digest", "registry.example/team/output:tag@sha256:"+strings.Repeat("a", 64)),
		Entry("JSON injection", "registry.example/team/output\"@sha256:"+strings.Repeat("a", 64)),
	)

	It("gates unpacking on Conforma and verifies the same digest it consumes", func() {
		data, err := os.ReadFile("../examples/demo/release.yaml")
		Expect(err).NotTo(HaveOccurred())
		spec := decodeDemo(data)["spec"].(map[string]any)
		tasks := spec["tasks"].([]any)
		verify := tasks[0].(map[string]any)
		unpack := tasks[1].(map[string]any)
		Expect(verify["name"]).To(Equal("verify"))
		params := verify["params"].([]any)
		var snapshot map[string]any
		Expect(json.Unmarshal([]byte(params[0].(map[string]any)["value"].(string)), &snapshot)).To(Succeed())
		Expect(snapshot["components"].([]any)[0].(map[string]any)["containerImage"]).To(Equal("$(params.image)"))
		Expect(unpack["runAfter"]).To(Equal([]any{"verify"}))
		steps := unpack["taskSpec"].(map[string]any)["steps"].([]any)
		Expect(steps[0].(map[string]any)["ref"]).To(Equal(map[string]any{"name": "unpack-build-output"}))
		Expect(steps[0].(map[string]any)["params"]).To(ContainElement(map[string]any{"name": "image", "value": "$(params.image)"}))
	})

	It("converts a defaulted inline validator into a standalone Task accepted by Tekton", func() {
		cmd := exec.Command("bash", "../scripts/demo.sh", "render", "validator")
		cmd.Stdin = strings.NewReader(`{"spec":{"tasks":[{"taskSpec":{"metadata":{},"spec":null,"results":[{"name":"TEST_OUTPUT"}],"steps":[{"name":"assert","image":"example/validator","command":["jq","-e"]}]}}]}}`)
		data, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(data))
		body := decodeDemo(data)
		Expect(body["kind"]).To(Equal("Task"))
		spec := body["spec"].(map[string]any)
		Expect(spec).NotTo(HaveKey("metadata"))
		Expect(spec).NotTo(HaveKey("spec"))
		Expect(spec["params"]).To(ContainElement(map[string]any{"name": "SNAPSHOT", "type": "string"}))
		Expect(spec["steps"].([]any)[0].(map[string]any)["name"]).To(Equal("assert"))
	})
})
