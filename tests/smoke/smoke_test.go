// Package smoke exercises real containers without contacting a Kubernetes cluster.
package smoke

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type taskStep struct {
	Name         string                         `yaml:"name"`
	Image        string                         `yaml:"image"`
	Command      []string                       `yaml:"command"`
	Args         []string                       `yaml:"args"`
	WorkingDir   string                         `yaml:"workingDir"`
	Env          []struct{ Name, Value string } `yaml:"env"`
	VolumeMounts []struct {
		Name      string `yaml:"name"`
		MountPath string `yaml:"mountPath"`
		ReadOnly  bool   `yaml:"readOnly"`
	} `yaml:"volumeMounts"`
}

// readSteps loads Task commands and parameter defaults for container smoke execution.
func readSteps(t *testing.T, root, name string) (map[string]taskStep, map[string]any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "task", name, "0.1", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var object struct {
		Spec struct {
			Steps  []taskStep `yaml:"steps"`
			Params []struct {
				Name    string `yaml:"name"`
				Default any    `yaml:"default"`
			} `yaml:"params"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	steps := map[string]taskStep{}
	for _, step := range object.Spec.Steps {
		steps[step.Name] = step
	}
	defaults := map[string]any{}
	for _, param := range object.Spec.Params {
		defaults[param.Name] = param.Default
	}
	return steps, defaults
}

// execute runs a smoke-test command and reports captured output on failure.
func execute(t *testing.T, env []string, command ...string) string {
	t.Helper()
	cmd := exec.Command(command[0], command[1:]...)
	if env != nil {
		cmd.Env = env
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", command[0], err, output)
	}
	if len(output) > 0 {
		t.Log(strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

// write writes a fixture file with the required permissions.
func write(t *testing.T, path, value string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), mode); err != nil {
		t.Fatal(err)
	}
}

// mkdir creates a fixture directory and any missing parents.
func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
}

// read reads a fixture or generated result file as text.
func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// requireEntries checks a directory's immediate entries against the expected names.
func requireEntries(t *testing.T, path string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s entries=%v want=%v", path, got, want)
	}
}

// requireMissing checks that a rejected or isolated path was not created.
func requireMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("unexpected path %s: %v", path, err)
	}
}

// TestPipeline exercises real artifact handoffs, CLI packaging, and local/remote builders using disposable containers.
func TestPipeline(t *testing.T) {
	if os.Getenv("BYOB_SMOKE") != "1" {
		t.Skip("set BYOB_SMOKE=1 to run rootless Podman integration")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	local, defaults := readSteps(t, root, "run-build")
	pack, _ := readSteps(t, root, "pack")
	cliImage := os.Getenv("BYOB_CLI_IMAGE")
	if cliImage == "" {
		cliImage = "localhost/byob-cli:devel"
	}
	builder := defaults["image"].(string)
	trusted := local["restore"].Image
	network := fmt.Sprintf("byob-smoke-%d", time.Now().UnixNano())
	registry := network + "-registry"
	execute(t, nil, "podman", "network", "create", network)
	t.Cleanup(func() {
		for _, args := range [][]string{{"rm", "-f", registry}, {"network", "rm", network}} {
			if output, err := exec.Command("podman", args...).CombinedOutput(); err != nil {
				t.Errorf("cleanup %v: %v\n%s", args, err, output)
			}
		}
	})
	execute(t, nil, "podman", "run", "-d", "--name", registry, "--network", network, "--network-alias", "registry", "-p", "127.0.0.1::5000", "docker.io/library/registry:2")
	hostRegistry := execute(t, nil, "podman", "port", registry, "5000/tcp")
	work := t.TempDir()
	toolsDir := t.TempDir()
	if err := os.CopyFS(filepath.Join(work, "source"), os.DirFS(filepath.Join(root, "examples/component"))); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(work, "source/private-source.txt"), "source must never be republished", 0644)
	mkdir(t, filepath.Join(work, "inputs"))
	for _, image := range []string{builder, trusted} {
		execute(t, nil, "podman", "pull", "--quiet", image)
	}
	common := func(readonly bool) []string {
		volume := work + ":/var/workdir:Z"
		if readonly {
			volume = work + ":/var/workdir:ro,Z"
		}
		return []string{"podman", "--default-mounts-file=/dev/null", "run", "--rm", "--network", network, "--userns", "keep-id:uid=1000,gid=0", "--user", "1000:0", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-v", volume}
	}
	artifact := func(args ...string) string {
		return execute(t, nil, append(append(common(false), "-e", "ORAS_OPTIONS=--plain-http", trusted), args...)...)
	}
	replacements := map[string]string{
		"$(params.image)": builder, "$(params.layout)": "flat",
		"$(params.IMAGE)": "registry:5000/byob:output", "$(params.artifactType)": "application/vnd.konflux.build-output.v1",
		"$(params.ociStorage)": "registry:5000/byob", "$(params.ociArtifactExpiresAfter)": "",
	}
	buildCommand := []string{"./build.sh"}
	replace := func(value string) string {
		for from, to := range replacements {
			value = strings.ReplaceAll(value, from, to)
		}
		return value
	}
	runStep := func(step taskStep, refs []string, readonly bool, extra []string) {
		cmd := append(common(readonly), extra...)
		for _, mount := range step.VolumeMounts {
			if mount.Name == "build-tools" {
				options := ":Z"
				if mount.ReadOnly {
					options = ":ro,Z"
				}
				cmd = append(cmd, "-v", toolsDir+":"+mount.MountPath+options)
			}
		}
		if step.WorkingDir != "" {
			cmd = append(cmd, "--workdir", step.WorkingDir)
		}
		for _, env := range step.Env {
			cmd = append(cmd, "-e", env.Name+"="+replace(env.Value))
		}
		cmd = append(cmd, "-e", "ORAS_OPTIONS=--plain-http")
		image := replace(step.Image)
		if image == "localhost/byob-cli:devel" {
			image = cliImage
		}
		if len(step.Command) > 0 {
			cmd = append(cmd, "--entrypoint", step.Command[0])
		}
		cmd = append(cmd, image)
		if len(step.Command) > 1 {
			cmd = append(cmd, step.Command[1:]...)
		}
		for _, arg := range step.Args {
			if arg == "$(params.BUILD_ARTIFACTS[*])" {
				cmd = append(cmd, refs...)
			} else if arg == "$(params.build-cmd[*])" {
				cmd = append(cmd, buildCommand...)
			} else {
				cmd = append(cmd, replace(arg))
			}
		}
		execute(t, nil, cmd...)
	}
	artifact("create", "--store", "registry:5000/byob:checkout", "/var/workdir/checkout.ref=/var/workdir/source")
	checkout := read(t, filepath.Join(work, "checkout.ref"))
	buildScript := func(path string) {
		buildCommand = []string{"./" + path}
		runStep(local["prepare"], nil, true, nil)
		runStep(local["build"], nil, false, nil)
	}
	exportOutputs := func(name string) string {
		staged := filepath.Join(work, name)
		mkdir(t, staged)
		runStep(local["validate-output"], nil, true, []string{"-v", staged + ":/var/build-output:Z"})
		requireEntries(t, staged, "out")
		replacements["$(context.taskRun.name)"] = name
		replacements["$(step.results.BUILD_ARTIFACT.path)"] = "/var/workdir/" + name + ".ref"
		runStep(local["export"], nil, false, []string{"-v", staged + ":/var/build-output:ro,Z"})
		return read(t, filepath.Join(work, name+".ref"))
	}
	t.Log("Testing empty and multiple artifact inputs with a fresh checkout")
	runStep(local["restore-inputs"], nil, false, nil)
	requireEntries(t, filepath.Join(work, "inputs"))
	buildScript("scripts/generate.sh")
	codegen := exportOutputs("codegen")
	write(t, filepath.Join(work, "source/out/generated.txt"), "Generated by another build\n", 0644)
	second := exportOutputs("second-codegen")
	if err := os.RemoveAll(filepath.Join(work, "source")); err != nil {
		t.Fatal(err)
	}
	artifact("use", checkout+"=/var/workdir/source")
	requireMissing(t, filepath.Join(work, "source/out"))
	runStep(local["restore-inputs"], []string{codegen, second}, false, nil)
	requireEntries(t, filepath.Join(work, "inputs"), "0", "1")
	for index, want := range []string{"Generated from reviewed source\n", "Generated by another build\n"} {
		path := filepath.Join(work, "inputs", fmt.Sprint(index))
		requireEntries(t, path, "out")
		if got := read(t, filepath.Join(path, "out/generated.txt")); got != want {
			t.Fatalf("input %d=%q", index, got)
		}
	}
	buildScript("build.sh")
	built := exportOutputs("build")
	// Separate Task input volumes are represented by separate directories here.
	packRestore := pack["restore"]
	packRestore.Args = slices.Clone(packRestore.Args)
	for i, arg := range packRestore.Args {
		packRestore.Args[i] = strings.ReplaceAll(arg, "/var/workdir/inputs", "/var/workdir/pack-inputs")
	}
	runStep(packRestore, []string{built}, false, nil)
	requireEntries(t, filepath.Join(work, "pack-inputs/0"), "out")
	packPrepare := pack["prepare"]
	packPrepare.Args = slices.Clone(packPrepare.Args)
	for i, arg := range packPrepare.Args {
		packPrepare.Args[i] = strings.ReplaceAll(arg, "/var/workdir/inputs", "/var/workdir/pack-inputs")
	}
	runStep(packPrepare, nil, false, nil)
	runStep(pack["sbom"], nil, false, nil)
	for _, name := range []string{"IMAGE_URL", "IMAGE_DIGEST", "SBOM_BLOB_URL"} {
		replacements["$(step.results."+name+".path)"] = "/var/workdir/" + name
	}
	publish := pack["publish"]
	publish.Args = append(slices.Clone(publish.Args), "--plain-http")
	runStep(publish, nil, false, nil)
	digest := read(t, filepath.Join(work, "IMAGE_DIGEST"))
	manifest := execute(t, nil, append(common(false), "--entrypoint", "oras", cliImage, "manifest", "fetch", "--plain-http", "registry:5000/byob@"+digest)...)
	var object map[string]any
	if err := json.Unmarshal([]byte(manifest), &object); err != nil {
		t.Fatal(err)
	}
	if object["artifactType"] != "application/vnd.konflux.build-output.v1" {
		t.Fatalf("manifest=%v", object)
	}
	payload, err := os.Open(filepath.Join(work, "payload.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Close()
	compressed, err := gzip.NewReader(payload)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	contents := map[string]string{}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
		contents[header.Name] = string(data)
	}
	if len(contents) != 2 || contents["generated.txt"] != "Generated from reviewed source\n" || !strings.HasPrefix(contents["hello.txt"], "Built on ") {
		t.Fatalf("unexpected payload=%v", contents)
	}
	t.Log("Testing the Go remote CLI with rootless Podman and a local SSH transport fixture")
	remoteRoot, sshDir, binDir := filepath.Join(work, "vm"), filepath.Join(work, "ssh"), filepath.Join(work, "bin")
	for _, path := range []string{remoteRoot, sshDir, binDir} {
		mkdir(t, path)
	}
	write(t, filepath.Join(sshDir, "host"), "builder@fixture.invalid", 0600)
	write(t, filepath.Join(sshDir, "id_rsa"), "transport-fixture-key", 0600)
	write(t, filepath.Join(sshDir, "user-dir"), remoteRoot, 0600)
	write(t, filepath.Join(binDir, "ssh"), "#!/bin/bash\nset -eu\nwhile [[ $1 != builder@fixture.invalid ]]; do shift; done\nshift\nexec bash -c \"$1\"\n", 0755)
	scriptPath := filepath.Join(work, "source/build.sh")
	write(t, scriptPath, read(t, scriptPath)+"test \"$(cat \"$BUILD_INPUTS/1/out/generated.txt\")\" = 'Generated by another build'\nprintf changed > private-source.txt\nprintf extra > remote-source-change.txt\n", 0755)
	remoteEnv := append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	remoteBase := []string{filepath.Join(root, "dist/byob"), "build-remote", "--ssh-dir=" + sshDir, "--source=" + filepath.Join(work, "source"), "--inputs=" + filepath.Join(work, "inputs"), "--control=" + filepath.Join(work, "control")}
	remoteCommand := append(slices.Clone(remoteBase), "--image="+builder, "--", "./build.sh")
	execute(t, remoteEnv, remoteCommand...)
	if !strings.HasPrefix(read(t, filepath.Join(work, "source/out/hello.txt")), "Built on ") {
		t.Fatal("missing remote output")
	}
	if read(t, filepath.Join(work, "source/out/generated.txt")) != "Generated from reviewed source\n" {
		t.Fatal("remote input zero was not used")
	}
	if read(t, filepath.Join(work, "source/private-source.txt")) != "source must never be republished" {
		t.Fatal("remote source overwrote checkout")
	}
	requireMissing(t, filepath.Join(work, "source/remote-source-change.txt"))
	if !strings.Contains(read(t, filepath.Join(work, "control/builder-image")), "@sha256:") {
		t.Fatal("remote builder identity not recorded")
	}
	requireEntries(t, remoteRoot)
	exportOutputs("remote-build")
	for _, scenario := range []struct{ name, script, want string }{{"symlinked output", "#!/bin/sh\nrm -rf out\nln -s . out\n", "out/ must be a real directory"}, {"failed build", "#!/bin/sh\nexit 17\n", ""}} {
		t.Run(scenario.name, func(t *testing.T) {
			write(t, scriptPath, scenario.script, 0755)
			cmd := exec.Command(remoteCommand[0], remoteCommand[1:]...)
			cmd.Env = remoteEnv
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("invalid build succeeded: %s", output)
			}
			if !bytes.Contains(output, []byte(scenario.want)) {
				t.Fatalf("failure=%s, expected %q", output, scenario.want)
			}
			requireEntries(t, remoteRoot)
		})
	}
	t.Run("shell-free-image", func(t *testing.T) {
		image := shellFreeImage(t, root, hostRegistry, network)
		replacements["$(params.image)"] = image
		podman, err := exec.LookPath("podman")
		if err != nil {
			t.Fatal(err)
		}
		// The real Podman worker only uses HTTP for this disposable test registry.
		write(t, filepath.Join(binDir, "podman"), `#!/bin/bash
set -eu
global=$1
operation=$2
shift 2
if [[ "$operation" == pull && "${*: -1}" == "$BYOB_SMOKE_REGISTRY/"* ]]; then
    exec "$BYOB_REAL_PODMAN" "$global" "$operation" --tls-verify=false "$@"
fi
exec "$BYOB_REAL_PODMAN" "$global" "$operation" "$@"
`, 0755)
		fixtureEnv := append(slices.Clone(remoteEnv), "BYOB_REAL_PODMAN="+podman, "BYOB_SMOKE_REGISTRY="+hostRegistry)
		for _, args := range [][]string{{}, {"with spaces", "; touch injected", "$(touch injected)", "--leading-dash", "quote'\""}} {
			buildCommand = append([]string{"/fixture-builder"}, args...)
			expected, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(work, "source/expected-arguments.json"), string(expected), 0644)
			runStep(local["prepare"], nil, true, nil)
			runStep(local["build"], nil, false, nil)
			if read(t, filepath.Join(work, "source/out/arguments.json")) != string(expected) {
				t.Fatal("local argv changed")
			}
			remoteArgs := append(slices.Clone(remoteBase), "--image="+image, "--")
			remoteArgs = append(remoteArgs, buildCommand...)
			execute(t, fixtureEnv, remoteArgs...)
			if read(t, filepath.Join(work, "source/out/arguments.json")) != string(expected) {
				t.Fatal("remote argv changed")
			}
			requireEntries(t, remoteRoot)
			requireMissing(t, filepath.Join(work, "source/injected"))
		}
	})
}

// shellFreeImage builds and publishes a native fixture with no shell and a conflicting image CMD.
func shellFreeImage(t *testing.T, root, registry, suffix string) string {
	t.Helper()
	buildDir := t.TempDir()
	execute(t, append(os.Environ(), "CGO_ENABLED=0"), "go", "build", "-trimpath", "-o", filepath.Join(buildDir, "builder"), filepath.Join(root, "tests/smoke/testdata/builder"))
	write(t, filepath.Join(buildDir, "Containerfile"), "FROM scratch\nCOPY --chown=1000:0 builder /fixture-builder\nENTRYPOINT [\"/fixture-builder\"]\nCMD [\"unexpected-image-default\"]\nUSER 1000:0\n", 0644)
	tag := registry + "/shell-free:" + suffix
	execute(t, nil, "podman", "build", "-t", tag, "-f", filepath.Join(buildDir, "Containerfile"), buildDir)
	t.Cleanup(func() {
		if output, err := exec.Command("podman", "rmi", "--ignore", tag).CombinedOutput(); err != nil {
			t.Errorf("remove shell-free image: %v\n%s", err, output)
		}
	})
	digestPath := filepath.Join(buildDir, "digest")
	execute(t, nil, "podman", "push", "--tls-verify=false", "--digestfile", digestPath, tag)
	image := registry + "/shell-free@" + strings.TrimSpace(read(t, digestPath))
	execute(t, nil, "podman", "pull", "--tls-verify=false", image)
	return image
}
