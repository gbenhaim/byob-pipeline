package tests

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

// TestCatalog runs the catalog validation specs.
func TestCatalog(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Catalog Suite")
}

// TestMain provides the native build subprocess fixture used by catalog specs.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__fixture_build__" {
		data, err := json.Marshal(os.Args[2:])
		if err != nil || os.WriteFile("arguments.json", data, 0644) != nil {
			os.Exit(98)
		}

		if info, err := os.Stat(os.Getenv("HOME")); err != nil || !info.IsDir() {
			os.Exit(99)
		}

		os.Exit(17)
	}

	os.Exit(m.Run())
}

type resource struct {
	Kind       string         `yaml:"kind"`
	Data       map[string]any `yaml:"data"`
	StringData map[string]any `yaml:"stringData"`
	Spec       spec           `yaml:"spec"`
}

type spec struct {
	Params          []param        `yaml:"params"`
	Results         []result       `yaml:"results"`
	Steps           []step         `yaml:"steps"`
	Volumes         []volume       `yaml:"volumes"`
	Workspaces      []workspace    `yaml:"workspaces"`
	Sidecars        []any          `yaml:"sidecars"`
	Tasks           []pipelineTask `yaml:"tasks"`
	PipelineSpec    *spec          `yaml:"pipelineSpec"`
	PodTemplate     map[string]any `yaml:"podTemplate"`
	TaskRunSpecs    []any          `yaml:"taskRunSpecs"`
	TaskRunTemplate struct {
		ServiceAccountName string `yaml:"serviceAccountName"`
	} `yaml:"taskRunTemplate"`
	StepTemplate struct {
		VolumeMounts    []mount `yaml:"volumeMounts"`
		SecurityContext struct {
			RunAsNonRoot             bool  `yaml:"runAsNonRoot"`
			AllowPrivilegeEscalation *bool `yaml:"allowPrivilegeEscalation"`
			Capabilities             struct {
				Drop []string `yaml:"drop"`
			} `yaml:"capabilities"`
		} `yaml:"securityContext"`
	} `yaml:"stepTemplate"`
}

type workspace struct {
	Name      string         `yaml:"name"`
	Optional  bool           `yaml:"optional"`
	Workspace string         `yaml:"workspace"`
	Secret    *secretBinding `yaml:"secret"`
}

type secretBinding struct {
	SecretName string `yaml:"secretName"`
}

type param struct {
	Name    string `yaml:"name"`
	Type    string `yaml:"type"`
	Value   any    `yaml:"value"`
	Default any    `yaml:"default"`
}

type result struct {
	Name  string `yaml:"name"`
	Type  string `yaml:"type"`
	Value string `yaml:"value"`
}

type volume struct {
	Name   string         `yaml:"name"`
	Secret map[string]any `yaml:"secret"`
}

type mount struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
	SubPath   string `yaml:"subPath"`
	ReadOnly  bool   `yaml:"readOnly"`
}

type step struct {
	Name         string   `yaml:"name"`
	Image        string   `yaml:"image"`
	Script       string   `yaml:"script"`
	Command      []string `yaml:"command"`
	Args         []string `yaml:"args"`
	WorkingDir   string   `yaml:"workingDir"`
	VolumeMounts []mount  `yaml:"volumeMounts"`
	When         []struct {
		Input    string   `yaml:"input"`
		Operator string   `yaml:"operator"`
		Values   []string `yaml:"values"`
	} `yaml:"when"`
}

type pipelineTask struct {
	Name       string         `yaml:"name"`
	Params     []param        `yaml:"params"`
	TaskSpec   map[string]any `yaml:"taskSpec"`
	Workspaces []workspace    `yaml:"workspaces"`
	TaskRef    struct {
		Name     string  `yaml:"name"`
		Resolver string  `yaml:"resolver"`
		Params   []param `yaml:"params"`
	} `yaml:"taskRef"`
	Matrix struct {
		Params []param `yaml:"params"`
	} `yaml:"matrix"`
}

// readResource loads a catalog resource relative to the repository root.
func readResource(path string) resource {
	GinkgoHelper()
	data, err := os.ReadFile(filepath.Join("..", path))
	Expect(err).NotTo(HaveOccurred())

	return parseResource(data)
}

// parseResource decodes the resource fields used by catalog checks.
func parseResource(data []byte) resource {
	GinkgoHelper()
	var value resource
	Expect(yaml.Unmarshal(data, &value)).To(Succeed())

	return value
}

// taskSpec loads the authored or generated Task specification by name.
func taskSpec(name string) spec {
	GinkgoHelper()
	return readResource(filepath.Join("task", name, "0.1", name+".yaml")).Spec
}

// findStep finds a named Task step or fails the calling test.
func findStep(task spec, name string) step {
	GinkgoHelper()
	for _, value := range task.Steps {
		if value.Name == name {
			return value
		}
	}

	Fail("missing step " + name)
	return step{}
}

// findTask finds a named Pipeline task or fails the calling test.
func findTask(pipeline spec, name string) pipelineTask {
	GinkgoHelper()
	for _, value := range pipeline.Tasks {
		if value.Name == name {
			return value
		}
	}

	Fail("missing pipeline task " + name)
	return pipelineTask{}
}

// findWorkspace finds a named declared workspace or PipelineTask binding.
func findWorkspace(workspaces []workspace, name string) workspace {
	GinkgoHelper()
	for _, value := range workspaces {
		if value.Name == name {
			return value
		}
	}

	Fail("missing workspace " + name)
	return workspace{}
}

// findParam finds a declared or supplied parameter by name.
func findParam(params []param, name string) param {
	GinkgoHelper()
	for _, value := range params {
		if value.Name == name {
			return value
		}
	}

	Fail("missing parameter " + name)
	return param{}
}

// assertEqual compares structured catalog values and reports their differences.
func assertEqual(got, want any) {
	GinkgoHelper()
	Expect(got).To(Equal(want))
}

// hasMount reports whether a step mounts the named volume.
func hasMount(mounts []mount, name string) bool {
	return slices.ContainsFunc(mounts, func(value mount) bool { return value.Name == name })
}

var _ = Describe("BYOB catalog", func() {
	It("routes trusted Task operations through the CLI", func() {
		operations := map[string]map[string]string{
			"run-build":        {"restore-inputs": "restore-inputs", "prepare": "prepare", "validate-output": "stage-output"},
			"run-build-remote": {"restore-inputs": "restore-inputs", "prepare": "prepare", "select-platform": "select-platform", "build-remote": "build-remote", "record-image": "record-image", "validate-output": "stage-output"},
			"pack":             {"restore": "restore-inputs", "prepare": "pack", "publish": "publish"},
		}

		for taskName, commands := range operations {
			By("checking " + taskName)
			task := taskSpec(taskName)
			for _, taskStep := range task.Steps {
				Expect(taskStep.Script).To(BeEmpty(), "%s still embeds a script", taskStep.Name)
			}

			for stepName, command := range commands {
				taskStep := findStep(task, stepName)
				Expect(taskStep.Command).To(Equal([]string{"/usr/local/bin/byob"}))
				Expect(taskStep.Image).To(Equal("localhost/byob-cli:devel"))
				Expect(taskStep.Args).NotTo(BeEmpty())
				Expect(taskStep.Args[0]).To(Equal(command), "%s args = %v", stepName, taskStep.Args)
			}
		}
	})

	It("keeps credentials away from developer steps", func() {
		for taskName, stepName := range map[string]string{"run-build": "build", "run-build-remote": "build-local"} {
			By("checking " + taskName)
			task := taskSpec(taskName)
			developerStep := findStep(task, stepName)
			volumes := map[string]volume{}
			for _, taskVolume := range task.Volumes {
				volumes[taskVolume.Name] = taskVolume
			}

			for _, mounted := range append(task.StepTemplate.VolumeMounts, developerStep.VolumeMounts...) {
				mountedVolume, ok := volumes[mounted.Name]
				Expect(ok).To(BeTrue(), "unknown mounted volume %s", mounted.Name)
				if ok {
					Expect(mountedVolume.Secret).To(BeNil(), "developer mounts secret volume %s", mounted.Name)
				}
			}

			Expect(developerStep.Image).To(Equal("$(params.image)"))
			Expect(developerStep.Command).NotTo(BeEmpty())
			Expect(task.Workspaces).To(BeEmpty())
			Expect(task.Sidecars).To(BeEmpty())

			security := task.StepTemplate.SecurityContext
			Expect(security.RunAsNonRoot).To(BeTrue())
			Expect(security.AllowPrivilegeEscalation).NotTo(BeNil())
			if security.AllowPrivilegeEscalation != nil {
				Expect(*security.AllowPrivilegeEscalation).To(BeFalse())
			}
			Expect(security.Capabilities.Drop).To(ContainElement("ALL"))
		}
	})

	It("routes remote execution without exposing SSH to localhost", func() {
		task := taskSpec("run-build-remote")
		for name, mode := range map[string]string{"build-local": "local", "build-remote": "remote"} {
			step := findStep(task, name)
			Expect(step.When).To(HaveLen(1), "%s routing conditions", name)
			Expect(step.When[0].Input).To(Equal("$(steps.select-platform.results.MODE)"))
			Expect(step.When[0].Operator).To(Equal("in"))
			Expect(step.When[0].Values).To(Equal([]string{mode}))
			Expect(hasMount(step.VolumeMounts, "ssh")).To(Equal(mode == "remote"))
		}

		Expect(findStep(task, "build-remote").Image).To(Equal(findStep(task, "prepare").Image))
		for _, name := range []string{"select-platform", "build-remote"} {
			step := findStep(task, name)
			Expect(slices.ContainsFunc(step.VolumeMounts, func(mounted mount) bool {
				return mounted.Name == "ssh" && mounted.MountPath == "/ssh" && mounted.ReadOnly
			})).To(BeTrue(), "%s must mount controller credentials read-only at /ssh", name)
		}

		sshVolumeFound := false
		for _, taskVolume := range task.Volumes {
			if taskVolume.Name == "ssh" {
				sshVolumeFound = true
				Expect(taskVolume.Secret["secretName"]).To(Equal("multi-platform-ssh-$(context.taskRun.name)"))
			}
		}
		Expect(sshVolumeFound).To(BeTrue(), "missing controller SSH secret")
	})

	It("mounts the developer working directory before source restore", func() {
		for taskName, stepName := range map[string]string{"run-build": "build", "run-build-remote": "build-local"} {
			By("checking " + taskName)
			task := taskSpec(taskName)
			step := findStep(task, stepName)
			cwdMounted := false
			for _, mounted := range append(task.StepTemplate.VolumeMounts, step.VolumeMounts...) {
				Expect(mounted.SubPath).To(BeEmpty(), "subPath is created before source restore")
				cwdMounted = cwdMounted || mounted.MountPath == step.WorkingDir
			}

			Expect(step.WorkingDir).NotTo(BeEmpty())
			Expect(cwdMounted).To(BeTrue(), "runtime working directory must be a direct volume mount")
		}
	})

	It("keeps final image results on pack and exposes build artifacts", func() {
		for _, name := range []string{"run-build", "run-build-remote"} {
			results := taskSpec(name).Results
			foundBuildArtifact := false
			for _, taskResult := range results {
				Expect([]string{"IMAGE_URL", "IMAGE_DIGEST", "SOURCE_ARTIFACT"}).NotTo(ContainElement(taskResult.Name), "%s exposes result %s", name, taskResult.Name)
				if taskResult.Name == "BUILD_ARTIFACT" {
					Expect(taskResult.Type).To(Equal("string"))
					Expect(taskResult.Value).To(Equal("$(steps.export.results.BUILD_ARTIFACT)"))
					foundBuildArtifact = true
				}
			}
			Expect(foundBuildArtifact).To(BeTrue(), "%s has no scalar BUILD_ARTIFACT result", name)
		}

		for _, name := range []string{"IMAGE_URL", "IMAGE_DIGEST"} {
			Expect(slices.ContainsFunc(taskSpec("pack").Results, func(taskResult result) bool {
				return taskResult.Name == name && taskResult.Value == "$(steps.publish.results."+name+")"
			})).To(BeTrue(), "pack must expose publish's %s result", name)
		}
	})

	It("exports only staged build output", func() {
		for _, name := range []string{"run-build", "run-build-remote"} {
			task := taskSpec(name)
			export := findStep(task, "export")
			Expect(export.Args).To(ContainElement("$(step.results.BUILD_ARTIFACT.path)=/var/build-output"), "%s does not export staged output", name)
			Expect(hasMount(append(task.StepTemplate.VolumeMounts, export.VolumeMounts...), "workdir")).To(BeFalse(), "%s exporter can access source/input workdir", name)

			for _, taskStep := range task.Steps {
				if taskStep.Image == "$(params.image)" {
					Expect(hasMount(append(task.StepTemplate.VolumeMounts, taskStep.VolumeMounts...), "build-output")).To(BeFalse(), "%s developer step can modify the private export volume", name)
				}
			}
		}
	})

	It("passes artifact arrays as separate CLI arguments", func() {
		for _, name := range []string{"run-build", "run-build-remote", "pack"} {
			task := taskSpec(name)
			inputs := findParam(task.Params, "BUILD_ARTIFACTS")
			Expect(inputs.Type).To(Equal("array"))
			for _, taskParam := range task.Params {
				Expect(taskParam.Name).NotTo(Equal("BUILD_ARTIFACT"), "%s declares a scalar BUILD_ARTIFACT input", name)
			}

			stepName := "restore-inputs"
			wantArgs := []string{"restore-inputs", "--root=/var/workdir/inputs", "--", "$(params.BUILD_ARTIFACTS[*])"}
			if name == "pack" {
				stepName = "restore"
				wantArgs = []string{"restore-inputs", "--root=/var/workdir/inputs", "--require", "--", "$(params.BUILD_ARTIFACTS[*])"}
			} else {
				Expect(inputs.Default).To(Equal([]any{}))
			}

			Expect(findStep(task, stepName).Args).To(Equal(wantArgs))
		}
	})

	It("combines matrix outputs with platform-owned settings", func() {
		pipeline := readResource("pipelines/byob-matrix.yaml").Spec
		build := findTask(pipeline, "build")
		Expect(build.Matrix.Params).To(Equal([]param{{Name: "PLATFORM", Value: []any{"linux/amd64", "linux/arm64"}}}))
		pack := findTask(pipeline, "pack")
		Expect(findParam(pack.Params, "BUILD_ARTIFACTS").Value).To(Equal([]any{"$(tasks.build.results.BUILD_ARTIFACT[*])"}))
		Expect(findParam(pack.Params, "layout").Value).To(Equal("platform"))

		names := []string{}
		for _, pipelineParam := range pipeline.Params {
			names = append(names, pipelineParam.Name)
		}
		slices.Sort(names)
		Expect(names).To(Equal([]string{"git-url", "oci-storage", "output-image", "revision"}))
		for _, task := range pipeline.Tasks {
			Expect(task.TaskSpec).To(BeNil(), "%s must use a platform-owned Task", task.Name)
		}
	})

	It("scopes optional git auth to checkout", func() {
		for _, name := range []string{"byob", "byob-remote", "byob-matrix", "byob-chained"} {
			By("checking " + name)
			pipeline := readResource(filepath.Join("pipelines", name+".yaml")).Spec
			gitAuth := findWorkspace(pipeline.Workspaces, "git-auth")
			Expect(gitAuth.Optional).To(BeTrue())

			checkout := findTask(pipeline, "checkout")
			Expect(checkout.Workspaces).To(ContainElement(workspace{
				Name:      "basic-auth",
				Workspace: "git-auth",
			}))
			for _, task := range pipeline.Tasks {
				if task.Name != "checkout" {
					Expect(task.Workspaces).To(BeEmpty(), "%s receives Git credentials", task.Name)
				}
			}
		}

		pac := configuredPaC()
		Expect(pac.Spec.PipelineSpec).NotTo(BeNil())
		if pac.Spec.PipelineSpec == nil {
			return
		}

		pipeline := *pac.Spec.PipelineSpec
		Expect(findWorkspace(pipeline.Workspaces, "git-auth").Optional).To(BeTrue())
		Expect(findTask(pipeline, "checkout").Workspaces).To(ContainElement(workspace{
			Name:      "basic-auth",
			Workspace: "git-auth",
		}))
		for _, task := range pipeline.Tasks {
			if task.Name != "checkout" {
				Expect(task.Workspaces).To(BeEmpty(), "%s receives Git credentials", task.Name)
			}
		}

		privateRun := readResource("examples/pipelineruns/byob-private.yaml")
		gitBinding := findWorkspace(privateRun.Spec.Workspaces, "git-auth")
		Expect(gitBinding.Secret).NotTo(BeNil())
		if gitBinding.Secret != nil {
			Expect(gitBinding.Secret.SecretName).To(Equal("git-source-credentials"))
		}
		Expect(privateRun.Spec.TaskRunTemplate.ServiceAccountName).To(Equal("byob-build"))
		Expect(privateRun.Spec.TaskRunSpecs).To(BeEmpty())
		Expect(privateRun.Data).To(BeEmpty())
		Expect(privateRun.StringData).To(BeEmpty())
	})

	It("keeps the original checkout and chains separate outputs", func() {
		pipeline := readResource("pipelines/byob-chained.yaml").Spec
		build := findTask(pipeline, "build")
		Expect(findParam(build.Params, "SOURCE_ARTIFACT").Value).To(Equal("$(tasks.checkout.results.SOURCE_ARTIFACT)"))
		Expect(findParam(build.Params, "BUILD_ARTIFACTS").Value).To(Equal([]any{"$(tasks.codegen.results.BUILD_ARTIFACT)"}))
	})

	It("uses pinned tasks and a literal matrix in the PaC example", func() {
		run := configuredPaC()
		Expect(run.Kind).To(Equal("PipelineRun"))
		Expect(run.Spec.PodTemplate).To(BeNil())
		Expect(run.Spec.TaskRunSpecs).To(BeNil())
		Expect(run.Spec.PipelineSpec).NotTo(BeNil())
		if run.Spec.PipelineSpec == nil {
			return
		}

		pipeline := *run.Spec.PipelineSpec
		for _, task := range pipeline.Tasks {
			Expect(task.TaskSpec).To(BeNil(), "%s unexpectedly embeds taskSpec", task.Name)
			Expect(task.TaskRef.Resolver).To(Equal("bundles"))
			bundle := findParam(task.TaskRef.Params, "bundle").Value
			Expect(bundle).To(BeAssignableToTypeOf(""))
			if bundleText, ok := bundle.(string); ok {
				Expect(bundleText).To(MatchRegexp(`@sha256:[a-f0-9]{64}$`), "%s bundle is not digest pinned", task.Name)
			}
		}

		Expect(findTask(pipeline, "build").Matrix.Params).To(Equal([]param{{Name: "PLATFORM", Value: []any{"linux/amd64", "linux/arm64"}}}))
		Expect(findParam(findTask(pipeline, "pack").Params, "BUILD_ARTIFACTS").Value).To(Equal([]any{"$(tasks.build.results.BUILD_ARTIFACT[*])"}))
	})

	It("checks pipeline callers against declared task parameters", func() {
		pipelines := map[string]spec{}
		for _, name := range []string{"byob", "byob-chained", "byob-remote", "byob-matrix"} {
			pipelines[name] = readResource("pipelines/" + name + ".yaml").Spec
		}

		run := configuredPaC()
		Expect(run.Spec.PipelineSpec).NotTo(BeNil())
		if run.Spec.PipelineSpec == nil {
			return
		}

		pipelines["pac"] = *run.Spec.PipelineSpec
		for name, pipeline := range pipelines {
			By("checking " + name)
			for _, call := range pipeline.Tasks {
				target := call.TaskRef.Name
				if target == "" {
					nameParam := findParam(call.TaskRef.Params, "name").Value
					Expect(nameParam).To(BeAssignableToTypeOf(""))
					var ok bool
					target, ok = nameParam.(string)
					if !ok {
						continue
					}
				}

				if !slices.Contains([]string{"run-build", "run-build-remote", "pack"}, target) {
					continue
				}

				declared := map[string]param{}
				for _, taskParam := range taskSpec(target).Params {
					declared[taskParam.Name] = taskParam
				}

				provided := map[string]bool{}
				for _, taskParam := range append(call.Params, call.Matrix.Params...) {
					_, exists := declared[taskParam.Name]
					Expect(exists).To(BeTrue(), "%s supplies undeclared %s parameter to %s", call.Name, taskParam.Name, target)
					provided[taskParam.Name] = true
				}

				for paramName, taskParam := range declared {
					if taskParam.Default == nil {
						Expect(provided[paramName]).To(BeTrue(), "%s omits required %s parameter for %s", call.Name, paramName, target)
					}
				}
			}
		}
	})

	It("executes the developer argv directly from the mounted source directory", func() {
		executable, err := os.Executable()
		Expect(err).NotTo(HaveOccurred())
		if err != nil {
			return
		}

		for taskName, stepName := range map[string]string{"run-build": "build", "run-build-remote": "build-local"} {
			By("checking " + taskName)
			developerTask := taskSpec(taskName)
			developerStep := findStep(developerTask, stepName)
			buildArgsParam := findParam(developerTask.Params, "build-args")
			Expect(buildArgsParam.Type).To(Equal("array"))
			Expect(buildArgsParam.Default).To(Equal([]any{}))

			root := GinkgoT().TempDir()
			source := filepath.Join(root, "source")
			Expect(os.Mkdir(source, 0755)).To(Succeed())
			home := filepath.Join(root, "home")
			Expect(os.Mkdir(home, 0755)).To(Succeed())

			want := []string{"argument with spaces", "; touch injected.sh", "$(touch injected.sh)", "--leading-dash", "quote'\""}
			buildCommand := []string{executable, "__fixture_build__"}
			command := []string{}
			for _, argument := range developerStep.Command {
				if argument == "$(params.build-cmd[*])" {
					command = append(command, buildCommand...)
				} else {
					argument = strings.ReplaceAll(argument, "/var/source", source)
					command = append(command, strings.ReplaceAll(argument, "/var/workdir/home", home))
				}
			}
			for _, argument := range developerStep.Args {
				if argument == "$(params.build-args[*])" {
					command = append(command, want...)
				} else {
					command = append(command, argument)
				}
			}

			Expect(command).NotTo(BeEmpty())
			if len(command) == 0 {
				return
			}

			cmd := exec.Command(command[0], command[1:]...)
			cmd.Dir = source
			cmd.Env = append(os.Environ(), "HOME="+home)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				Fail("developer command did not exit with status 17: " + string(output))
				return
			}
			Expect(exit.ExitCode()).To(Equal(17))

			_, err = os.Stat(filepath.Join(source, "injected.sh"))
			Expect(errors.Is(err, os.ErrNotExist)).To(BeTrue())
			data, err := os.ReadFile(filepath.Join(source, "arguments.json"))
			Expect(err).NotTo(HaveOccurred())
			var got []string
			Expect(json.Unmarshal(data, &got)).To(Succeed())
			Expect(got).To(Equal(want))
			Expect(developerStep.Command).To(Equal([]string{"$(params.build-cmd[*])"}))
			Expect(developerStep.Args).To(Equal([]string{"$(params.build-args[*])"}))
			Expect(developerStep.WorkingDir).To(Equal("/var/source"))
			Expect(hasMount(developerStep.VolumeMounts, "build-tools")).To(BeFalse())
		}
	})
})

// configuredPaC fills bundle digest placeholders for PaC example validation.
func configuredPaC() resource {
	GinkgoHelper()
	data, err := os.ReadFile(filepath.Join("..", "examples/pac/push.yaml"))
	Expect(err).NotTo(HaveOccurred())

	configured := string(data)
	for _, name := range []string{"RUN_BUILD_REMOTE", "PACK"} {
		configured = strings.ReplaceAll(configured, "REPLACE_WITH_"+name+"_BUNDLE_DIGEST", strings.Repeat("a", 64))
	}

	return parseResource([]byte(configured))
}
