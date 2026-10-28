package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// TestRemoteTaskGenerator runs the remote Task generator specs.
func TestRemoteTaskGenerator(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Remote Task Generator Suite")
}

// fixture supplies a minimal local Task for generator transformation specs.
func fixture() map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": "run-build"},
		"spec": map[string]any{
			"params": []any{}, "results": []any{}, "volumes": []any{},
			"steps": []any{
				map[string]any{"name": "prepare", "image": "trusted@sha256:abc"},
				map[string]any{
					"name": "build", "image": "$(params.image)",
					"command":    []any{"$(params.build-cmd[*])"},
					"args":       []any{"$(params.build-args[*])"},
					"workingDir": "/var/source",
					"volumeMounts": []any{
						map[string]any{"name": "source", "mountPath": "/var/source"},
						map[string]any{"name": "workdir", "mountPath": "/var/workdir"},
					},
				},
				map[string]any{
					"name": "validate-output", "command": []any{"/usr/local/bin/byob"},
					"args": []any{"stage-output", "--source=/var/source", "--output=/var/build-output"},
				},
			},
		},
	}
}

var _ = Describe("remote Task generator", func() {
	It("rejects a missing build step", func() {
		task := fixture()
		task["spec"].(map[string]any)["steps"] = []any{}

		err := convert(task, "build", "run-build-remote")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("build"))
	})

	It("keeps the SSH secret off the developer image", func() {
		task := fixture()
		Expect(convert(task, "build", "run-build-remote")).To(Succeed())

		steps := task["spec"].(map[string]any)["steps"].([]any)
		found := map[string]map[string]any{}
		for _, item := range steps {
			step := item.(map[string]any)
			found[step["name"].(string)] = step
		}

		Expect(found["build-local"]["image"]).To(Equal("$(params.image)"))
		Expect(found["build-remote"]["image"]).To(Equal("trusted@sha256:abc"))
		Expect(found["build-local"]["command"]).To(Equal([]any{"$(params.build-cmd[*])"}))
		Expect(found["build-local"]["args"]).To(Equal([]any{"$(params.build-args[*])"}))
		Expect(found["build-local"]["workingDir"]).To(Equal("/var/source"))

		for _, item := range found["build-local"]["volumeMounts"].([]any) {
			Expect(item.(map[string]any)["name"]).NotTo(Equal("ssh"))
		}

		Expect(task["metadata"].(map[string]any)["name"]).To(Equal("run-build-remote"))
	})

	It("generates trusted CLI steps with parameter arguments", func() {
		task := fixture()
		Expect(convert(task, "build", "run-build-remote")).To(Succeed())

		want := map[string][]any{
			"select-platform": {"select-platform", "--ssh-dir=/ssh", "--result=$(step.results.MODE.path)"},
			"build-remote": {
				"build-remote",
				"--ssh-dir=/ssh",
				"--source=/var/source",
				"--inputs=/var/workdir/inputs",
				"--control=/var/workdir/control",
				"--image=$(params.image)",
				"--",
				"$(params.build-cmd[*])",
				"$(params.build-args[*])",
			},
			"record-image": {
				"record-image",
				"--mode=$(steps.select-platform.results.MODE)",
				"--image=$(params.image)",
				"--control=/var/workdir/control",
				"--result=$(step.results.BUILD_IMAGE_ARTIFACT_INPUTS.path)",
			},
		}

		for _, item := range items(task["spec"].(map[string]any), "steps") {
			step := item.(map[string]any)
			name := step["name"].(string)
			args, ok := want[name]
			if !ok {
				continue
			}

			By("checking generated step " + name)
			Expect(step["command"]).To(Equal([]any{"/usr/local/bin/byob"}))
			Expect(step["args"]).To(Equal(args))
			Expect(step["image"]).To(Equal("trusted@sha256:abc"))
			Expect(step).NotTo(HaveKey("script"))
			Expect(step).NotTo(HaveKey("env"))
			delete(want, name)
		}

		Expect(want).To(BeEmpty())
	})

	It("passes the selected platform to output validation", func() {
		task := fixture()
		Expect(convert(task, "build", "run-build-remote")).To(Succeed())

		for _, item := range items(task["spec"].(map[string]any), "steps") {
			step := item.(map[string]any)
			if step["name"] == "validate-output" {
				want := []any{"stage-output", "--source=/var/source", "--output=/var/build-output", "--platform=$(params.PLATFORM)"}
				Expect(step["args"]).To(Equal(want))
				return
			}
		}

		Fail("missing output validation step")
	})
})
