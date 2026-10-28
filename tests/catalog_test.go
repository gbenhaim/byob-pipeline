package tests

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gbenhaim/byob-pipeline/internal/cli"
	"gopkg.in/yaml.v3"
)

// TestMain provides native CLI and build subprocesses for the Task execution test.
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
	if os.Getenv("BYOB_CATALOG_CLI") == "1" {
		if err := cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type resource struct {
	Kind string `yaml:"kind"`
	Spec spec   `yaml:"spec"`
}

type spec struct {
	Params       []param        `yaml:"params"`
	Results      []result       `yaml:"results"`
	Steps        []step         `yaml:"steps"`
	Volumes      []volume       `yaml:"volumes"`
	Workspaces   []any          `yaml:"workspaces"`
	Sidecars     []any          `yaml:"sidecars"`
	Tasks        []pipelineTask `yaml:"tasks"`
	PipelineSpec *spec          `yaml:"pipelineSpec"`
	PodTemplate  map[string]any `yaml:"podTemplate"`
	TaskRunSpecs []any          `yaml:"taskRunSpecs"`
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
	Name     string         `yaml:"name"`
	Params   []param        `yaml:"params"`
	TaskSpec map[string]any `yaml:"taskSpec"`
	TaskRef  struct {
		Name     string  `yaml:"name"`
		Resolver string  `yaml:"resolver"`
		Params   []param `yaml:"params"`
	} `yaml:"taskRef"`
	Matrix struct {
		Params []param `yaml:"params"`
	} `yaml:"matrix"`
}

// readResource loads a catalog resource relative to the repository root.
func readResource(t *testing.T, path string) resource {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", path))
	if err != nil {
		t.Fatal(err)
	}
	return parseResource(t, data)
}

// parseResource decodes the resource fields used by catalog checks.
func parseResource(t *testing.T, data []byte) resource {
	t.Helper()
	var value resource
	if err := yaml.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

// taskSpec loads the authored or generated Task specification by name.
func taskSpec(t *testing.T, name string) spec {
	t.Helper()
	return readResource(t, filepath.Join("task", name, "0.1", name+".yaml")).Spec
}

// findStep finds a named Task step or fails the calling test.
func findStep(t *testing.T, task spec, name string) step {
	t.Helper()
	for _, value := range task.Steps {
		if value.Name == name {
			return value
		}
	}
	t.Fatalf("missing step %s", name)
	return step{}
}

// findTask finds a named Pipeline task or fails the calling test.
func findTask(t *testing.T, pipeline spec, name string) pipelineTask {
	t.Helper()
	for _, value := range pipeline.Tasks {
		if value.Name == name {
			return value
		}
	}
	t.Fatalf("missing pipeline task %s", name)
	return pipelineTask{}
}

// findParam finds a declared or supplied parameter by name.
func findParam(t *testing.T, params []param, name string) param {
	t.Helper()
	for _, value := range params {
		if value.Name == name {
			return value
		}
	}
	t.Fatalf("missing parameter %s", name)
	return param{}
}

// assertEqual compares structured catalog values and reports their differences.
func assertEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v; want %#v", got, want)
	}
}

// hasMount reports whether a step mounts the named volume.
func hasMount(mounts []mount, name string) bool {
	return slices.ContainsFunc(mounts, func(value mount) bool { return value.Name == name })
}

// TestTrustedTaskOperationsCallCLI checks that trusted Task operations invoke the CLI with explicit arguments.
func TestTrustedTaskOperationsCallCLI(t *testing.T) {
	operations := map[string]map[string]string{
		"run-build":        {"restore-inputs": "restore-inputs", "prepare": "prepare", "validate-output": "stage-output"},
		"run-build-remote": {"restore-inputs": "restore-inputs", "prepare": "prepare", "select-platform": "select-platform", "build-remote": "build-remote", "record-image": "record-image", "validate-output": "stage-output"},
		"pack":             {"restore": "restore-inputs", "prepare": "pack", "publish": "publish"},
	}
	for name, commands := range operations {
		t.Run(name, func(t *testing.T) {
			task := taskSpec(t, name)
			for _, step := range task.Steps {
				if step.Script != "" {
					t.Errorf("%s still embeds a script instead of calling the CLI", step.Name)
				}
			}
			for name, command := range commands {
				step := findStep(t, task, name)
				assertEqual(t, step.Command, []string{"/usr/local/bin/byob"})
				assertEqual(t, step.Image, "localhost/byob-cli:devel")
				if len(step.Args) == 0 || step.Args[0] != command {
					t.Errorf("%s args = %v; want CLI subcommand %s", name, step.Args, command)
				}
			}
		})
	}
}

// TestDeveloperStepsCannotMountCredentials verifies developer steps cannot access credential volumes.
func TestDeveloperStepsCannotMountCredentials(t *testing.T) {
	for taskName, stepName := range map[string]string{"run-build": "build", "run-build-remote": "build-local"} {
		t.Run(taskName, func(t *testing.T) {
			task := taskSpec(t, taskName)
			step := findStep(t, task, stepName)
			volumes := map[string]volume{}
			for _, volume := range task.Volumes {
				volumes[volume.Name] = volume
			}
			for _, mount := range append(task.StepTemplate.VolumeMounts, step.VolumeMounts...) {
				volume, ok := volumes[mount.Name]
				if !ok || volume.Secret != nil {
					t.Errorf("developer step mounts unknown or secret volume %s", mount.Name)
				}
			}
			assertEqual(t, step.Image, "$(params.image)")
			if len(step.Command) == 0 || len(task.Workspaces) != 0 || len(task.Sidecars) != 0 {
				t.Fatal("developer execution must use an explicit command without workspaces or sidecars")
			}
			security := task.StepTemplate.SecurityContext
			if !security.RunAsNonRoot || security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation || !slices.Contains(security.Capabilities.Drop, "ALL") {
				t.Fatalf("unsafe developer security context: %+v", security)
			}
		})
	}
}

// TestRemoteTaskRoutesLocalhostWithoutSSHSecret checks local and remote execution selection and SSH isolation.
func TestRemoteTaskRoutesLocalhostWithoutSSHSecret(t *testing.T) {
	task := taskSpec(t, "run-build-remote")
	for name, mode := range map[string]string{"build-local": "local", "build-remote": "remote"} {
		step := findStep(t, task, name)
		if len(step.When) != 1 {
			t.Fatalf("%s routing conditions = %v", name, step.When)
		}
		assertEqual(t, step.When[0].Input, "$(steps.select-platform.results.MODE)")
		assertEqual(t, step.When[0].Operator, "in")
		assertEqual(t, step.When[0].Values, []string{mode})
		assertEqual(t, hasMount(step.VolumeMounts, "ssh"), mode == "remote")
	}
	assertEqual(t, findStep(t, task, "build-remote").Image, findStep(t, task, "prepare").Image)
	for _, name := range []string{"select-platform", "build-remote"} {
		step := findStep(t, task, name)
		if !slices.ContainsFunc(step.VolumeMounts, func(m mount) bool {
			return m.Name == "ssh" && m.MountPath == "/ssh" && m.ReadOnly
		}) {
			t.Errorf("%s must mount controller credentials read-only at /ssh", name)
		}
	}
	for _, volume := range task.Volumes {
		if volume.Name == "ssh" {
			assertEqual(t, volume.Secret["secretName"], "multi-platform-ssh-$(context.taskRun.name)")
			return
		}
	}
	t.Fatal("missing controller SSH secret")
}

// TestDeveloperContainerCanBeCreatedBeforeSourceRestore prevents kubelet from precreating the source directory as a root-owned working directory.
func TestDeveloperContainerCanBeCreatedBeforeSourceRestore(t *testing.T) {
	for taskName, stepName := range map[string]string{"run-build": "build", "run-build-remote": "build-local"} {
		t.Run(taskName, func(t *testing.T) {
			task := taskSpec(t, taskName)
			step := findStep(t, task, stepName)
			cwdMounted := false
			for _, mount := range append(task.StepTemplate.VolumeMounts, step.VolumeMounts...) {
				if mount.SubPath != "" {
					t.Error("subPath would be precreated as root before source restore")
				}
				cwdMounted = cwdMounted || mount.MountPath == step.WorkingDir
			}
			if !cwdMounted || step.WorkingDir == "" {
				t.Fatal("runtime cwd must exist before earlier steps execute")
			}
		})
	}
}

// TestOnlyPackDeclaresFinalSubject checks the boundary between intermediate artifacts and final image results.
func TestOnlyPackDeclaresFinalSubject(t *testing.T) {
	for _, name := range []string{"run-build", "run-build-remote"} {
		results := taskSpec(t, name).Results
		found := false
		for _, result := range results {
			if slices.Contains([]string{"IMAGE_URL", "IMAGE_DIGEST", "SOURCE_ARTIFACT"}, result.Name) {
				t.Errorf("%s exposes final/source result %s", name, result.Name)
			}
			if result.Name == "BUILD_ARTIFACT" {
				assertEqual(t, result.Type, "string")
				assertEqual(t, result.Value, "$(steps.export.results.BUILD_ARTIFACT)")
				found = true
			}
		}
		if !found {
			t.Errorf("%s has no scalar BUILD_ARTIFACT result", name)
		}
	}
	results := taskSpec(t, "pack").Results
	for _, name := range []string{"IMAGE_URL", "IMAGE_DIGEST", "SBOM_BLOB_URL"} {
		if !slices.ContainsFunc(results, func(result result) bool {
			return result.Name == name && result.Value == "$(steps.publish.results."+name+")"
		}) {
			t.Errorf("pack must expose publish's %s result", name)
		}
	}
}

// TestExportVolumeContainsOnlyStagedBuildOutput keeps source and developer code off the private export volume.
func TestExportVolumeContainsOnlyStagedBuildOutput(t *testing.T) {
	for _, name := range []string{"run-build", "run-build-remote"} {
		task := taskSpec(t, name)
		export := findStep(t, task, "export")
		if !slices.Contains(export.Args, "$(step.results.BUILD_ARTIFACT.path)=/var/build-output") {
			t.Errorf("%s does not export the staged output volume", name)
		}
		if hasMount(append(task.StepTemplate.VolumeMounts, export.VolumeMounts...), "workdir") {
			t.Errorf("%s exporter can access source/input workdir", name)
		}
		for _, step := range task.Steps {
			if step.Image == "$(params.image)" && hasMount(append(task.StepTemplate.VolumeMounts, step.VolumeMounts...), "build-output") {
				t.Errorf("%s developer step can modify the private export volume", name)
			}
		}
	}
}

// TestInputArraysReachCLIAsSeparateArguments preserves artifact array expansion at the CLI boundary.
func TestInputArraysReachCLIAsSeparateArguments(t *testing.T) {
	for _, name := range []string{"run-build", "run-build-remote", "pack"} {
		task := taskSpec(t, name)
		inputs := findParam(t, task.Params, "BUILD_ARTIFACTS")
		assertEqual(t, inputs.Type, "array")
		for _, param := range task.Params {
			if param.Name == "BUILD_ARTIFACT" {
				t.Errorf("%s must accept an input array, not a scalar BUILD_ARTIFACT", name)
			}
		}
		stepName := "restore-inputs"
		wantArgs := []string{"restore-inputs", "--root=/var/workdir/inputs", "--", "$(params.BUILD_ARTIFACTS[*])"}
		if name == "pack" {
			stepName = "restore"
			wantArgs = []string{"restore-inputs", "--root=/var/workdir/inputs", "--require", "--", "$(params.BUILD_ARTIFACTS[*])"}
		} else {
			assertEqual(t, inputs.Default, []any{})
		}
		assertEqual(t, findStep(t, task, stepName).Args, wantArgs)
	}
}

// TestMatrixConsumesAllArtifactsWithPlatformOwnedConfiguration checks matrix aggregation and committed build settings.
func TestMatrixConsumesAllArtifactsWithPlatformOwnedConfiguration(t *testing.T) {
	pipeline := readResource(t, "pipelines/byob-matrix.yaml").Spec
	build := findTask(t, pipeline, "build")
	assertEqual(t, build.Matrix.Params, []param{{Name: "PLATFORM", Value: []any{"linux/amd64", "linux/arm64"}}})
	pack := findTask(t, pipeline, "pack")
	assertEqual(t, findParam(t, pack.Params, "BUILD_ARTIFACTS").Value, []any{"$(tasks.build.results.BUILD_ARTIFACT[*])"})
	assertEqual(t, findParam(t, pack.Params, "layout").Value, "platform")
	names := []string{}
	for _, param := range pipeline.Params {
		names = append(names, param.Name)
	}
	slices.Sort(names)
	assertEqual(t, names, []string{"git-url", "oci-storage", "output-image", "revision"})
	if len(pipeline.Workspaces) != 0 {
		t.Fatal("matrix pipeline must exchange artifacts without workspaces")
	}
	for _, task := range pipeline.Tasks {
		if task.TaskSpec != nil {
			t.Errorf("%s must reference a platform-owned Task", task.Name)
		}
	}
}

// TestChainedBuildKeepsOriginalCheckoutAndSeparateOutputs checks source identity and output-only chaining.
func TestChainedBuildKeepsOriginalCheckoutAndSeparateOutputs(t *testing.T) {
	pipeline := readResource(t, "pipelines/byob-chained.yaml").Spec
	build := findTask(t, pipeline, "build")
	assertEqual(t, findParam(t, build.Params, "SOURCE_ARTIFACT").Value, "$(tasks.checkout.results.SOURCE_ARTIFACT)")
	assertEqual(t, findParam(t, build.Params, "BUILD_ARTIFACTS").Value, []any{"$(tasks.codegen.results.BUILD_ARTIFACT)"})
}

// configuredPaC fills bundle digest placeholders for PaC example validation.
func configuredPaC(t *testing.T) resource {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "examples/pac/push.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	configured := string(data)
	for _, name := range []string{"RUN_BUILD_REMOTE", "PACK"} {
		configured = strings.ReplaceAll(configured, "REPLACE_WITH_"+name+"_BUNDLE_DIGEST", strings.Repeat("a", 64))
	}
	return parseResource(t, []byte(configured))
}

// TestPaCExampleUsesPinnedTasksAndLiteralMatrix checks bundle pins and committed matrix settings in the PaC example.
func TestPaCExampleUsesPinnedTasksAndLiteralMatrix(t *testing.T) {
	run := configuredPaC(t)
	assertEqual(t, run.Kind, "PipelineRun")
	if run.Spec.PodTemplate != nil || run.Spec.TaskRunSpecs != nil || run.Spec.PipelineSpec == nil {
		t.Fatal("PaC run must use a pipelineSpec without per-run pod overrides")
	}
	pipeline := *run.Spec.PipelineSpec
	for _, task := range pipeline.Tasks {
		if task.TaskSpec != nil {
			t.Errorf("%s unexpectedly embeds taskSpec", task.Name)
		}
		assertEqual(t, task.TaskRef.Resolver, "bundles")
		bundle, ok := findParam(t, task.TaskRef.Params, "bundle").Value.(string)
		if !ok || !regexp.MustCompile(`@sha256:[a-f0-9]{64}$`).MatchString(bundle) {
			t.Errorf("%s bundle is not digest pinned: %v", task.Name, bundle)
		}
	}
	assertEqual(t, findTask(t, pipeline, "build").Matrix.Params, []param{{Name: "PLATFORM", Value: []any{"linux/amd64", "linux/arm64"}}})
	assertEqual(t, findParam(t, findTask(t, pipeline, "pack").Params, "BUILD_ARTIFACTS").Value, []any{"$(tasks.build.results.BUILD_ARTIFACT[*])"})
}

// TestPipelineCallersUseDeclaredLocalTaskParameters catches mismatched Task parameter names, types, and required inputs.
func TestPipelineCallersUseDeclaredLocalTaskParameters(t *testing.T) {
	pipelines := map[string]spec{}
	for _, name := range []string{"byob", "byob-chained", "byob-remote", "byob-matrix"} {
		pipelines[name] = readResource(t, "pipelines/"+name+".yaml").Spec
	}
	run := configuredPaC(t)
	if run.Spec.PipelineSpec == nil {
		t.Fatal("missing PaC pipelineSpec")
	}
	pipelines["pac"] = *run.Spec.PipelineSpec
	for name, pipeline := range pipelines {
		t.Run(name, func(t *testing.T) {
			for _, call := range pipeline.Tasks {
				target := call.TaskRef.Name
				if target == "" {
					target, _ = findParam(t, call.TaskRef.Params, "name").Value.(string)
				}
				if !slices.Contains([]string{"run-build", "run-build-remote", "pack"}, target) {
					continue
				}
				declared := map[string]param{}
				for _, param := range taskSpec(t, target).Params {
					declared[param.Name] = param
				}
				provided := map[string]bool{}
				for _, param := range append(call.Params, call.Matrix.Params...) {
					if _, ok := declared[param.Name]; !ok {
						t.Errorf("%s supplies undeclared %s parameter to %s", call.Name, param.Name, target)
					}
					provided[param.Name] = true
				}
				for name, param := range declared {
					if param.Default == nil && !provided[name] {
						t.Errorf("%s omits required %s parameter for %s", call.Name, name, target)
					}
				}
			}
		})
	}
}

// TestDeveloperCommandExecutesArgvDirectly checks native execution, argument boundaries and exit status.
func TestDeveloperCommandExecutesArgvDirectly(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for taskName, stepName := range map[string]string{"run-build": "build", "run-build-remote": "build-local"} {
		t.Run(taskName, func(t *testing.T) {
			step := findStep(t, taskSpec(t, taskName), stepName)
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if err := os.Mkdir(source, 0755); err != nil {
				t.Fatal(err)
			}
			want := []string{"argument with spaces", "; touch injected.sh", "$(touch injected.sh)", "--leading-dash", "quote'\""}
			argv := append([]string{executable, "__fixture_build__"}, want...)
			command := []string{executable}
			for _, arg := range step.Args {
				if arg == "$(params.build-cmd[*])" {
					command = append(command, argv...)
				} else {
					arg = strings.ReplaceAll(arg, "/var/workdir/source", source)
					command = append(command, strings.ReplaceAll(arg, "/var/workdir/home", filepath.Join(root, "home")))
				}
			}
			if len(command) == 0 {
				t.Fatal("developer step has no command")
			}
			cmd := exec.Command(command[0], command[1:]...)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "BYOB_CATALOG_CLI=1")
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 17 {
				t.Fatalf("developer command should exit 17; got %v: %s", err, output)
			}
			if _, err := os.Stat(filepath.Join(source, "injected.sh")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("command arguments were executed as shell source")
			}
			data, err := os.ReadFile(filepath.Join(source, "arguments.json"))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			assertEqual(t, got, want)
			assertEqual(t, step.Command, []string{"/var/build-tools/byob"})
			tools := slices.IndexFunc(step.VolumeMounts, func(m mount) bool { return m.Name == "build-tools" })
			if tools < 0 || !step.VolumeMounts[tools].ReadOnly {
				t.Fatal("the staged launcher must be mounted read-only")
			}
		})
	}
}
