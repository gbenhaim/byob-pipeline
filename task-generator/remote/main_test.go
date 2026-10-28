package main

import (
	"reflect"
	"strings"
	"testing"
)

// fixture supplies a minimal local Task for generator transformation tests.
func fixture() map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": "run-build"},
		"spec": map[string]any{
			"params": []any{}, "results": []any{}, "volumes": []any{},
			"steps": []any{
				map[string]any{"name": "prepare", "image": "trusted@sha256:abc"},
				map[string]any{"name": "build", "image": "$(params.image)", "command": []any{"/var/build-tools/byob"}, "args": []any{"exec-build", "--", "$(params.build-cmd[*])"}, "workingDir": "/var/workdir", "volumeMounts": []any{map[string]any{"name": "workdir", "mountPath": "/var/workdir"}}},
				map[string]any{"name": "validate-output", "command": []any{"/usr/local/bin/byob"}, "args": []any{"stage-output", "--source=/var/workdir/source", "--output=/var/build-output"}},
			},
		},
	}
}

// TestMissingBuildFailsInsteadOfEmittingNonRemoteTask rejects incomplete source Tasks.
func TestMissingBuildFailsInsteadOfEmittingNonRemoteTask(t *testing.T) {
	task := fixture()
	task["spec"].(map[string]any)["steps"] = []any{}
	err := convert(task, "build", "run-build-remote")
	if err == nil || !strings.Contains(err.Error(), "build") {
		t.Fatalf("expected missing build step error, got %v", err)
	}
}

// TestRoutingKeepsSecretOffDeveloperImage preserves the SSH credential boundary.
func TestRoutingKeepsSecretOffDeveloperImage(t *testing.T) {
	task := fixture()
	if err := convert(task, "build", "run-build-remote"); err != nil {
		t.Fatal(err)
	}
	steps := task["spec"].(map[string]any)["steps"].([]any)
	found := map[string]map[string]any{}
	for _, item := range steps {
		s := item.(map[string]any)
		found[s["name"].(string)] = s
	}
	if found["build-local"]["image"] != "$(params.image)" {
		t.Fatal("localhost must run developer image")
	}
	if found["build-remote"]["image"] != "trusted@sha256:abc" {
		t.Fatal("SSH must run trusted image")
	}
	for _, item := range found["build-local"]["volumeMounts"].([]any) {
		if item.(map[string]any)["name"] == "ssh" {
			t.Fatal("developer can read SSH secret")
		}
	}
	if task["metadata"].(map[string]any)["name"] != "run-build-remote" {
		t.Fatal("wrong task name")
	}
}

// TestGeneratedStepsCallTrustedCLIWithParameterArguments checks generated CLI wiring.
func TestGeneratedStepsCallTrustedCLIWithParameterArguments(t *testing.T) {
	task := fixture()
	if err := convert(task, "build", "run-build-remote"); err != nil {
		t.Fatal(err)
	}
	want := map[string][]any{
		"select-platform": {"select-platform", "--ssh-dir=/ssh", "--result=$(step.results.MODE.path)"},
		"build-remote":    {"build-remote", "--ssh-dir=/ssh", "--source=/var/workdir/source", "--inputs=/var/workdir/inputs", "--control=/var/workdir/control", "--image=$(params.image)", "--", "$(params.build-cmd[*])"},
		"record-image":    {"record-image", "--mode=$(steps.select-platform.results.MODE)", "--image=$(params.image)", "--control=/var/workdir/control", "--result=$(step.results.BUILD_IMAGE_ARTIFACT_INPUTS.path)"},
	}
	for _, item := range items(task["spec"].(map[string]any), "steps") {
		step := item.(map[string]any)
		name := step["name"].(string)
		args, ok := want[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			if !reflect.DeepEqual(step["command"], []any{"/usr/local/bin/byob"}) {
				t.Errorf("command = %v; want trusted CLI", step["command"])
			}
			if !reflect.DeepEqual(step["args"], args) {
				t.Errorf("args = %v; want %v", step["args"], args)
			}
			if step["image"] != "trusted@sha256:abc" {
				t.Errorf("image = %v; want prepare step's trusted image", step["image"])
			}
			for _, field := range []string{"script", "env"} {
				if _, ok := step[field]; ok {
					t.Errorf("generated CLI step unexpectedly contains %s", field)
				}
			}
		})
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("missing CLI steps: %v", want)
	}
}

// TestPlatformIsPassedToOutputValidationAsArgument preserves trusted matrix metadata.
func TestPlatformIsPassedToOutputValidationAsArgument(t *testing.T) {
	task := fixture()
	if err := convert(task, "build", "run-build-remote"); err != nil {
		t.Fatal(err)
	}
	for _, item := range items(task["spec"].(map[string]any), "steps") {
		step := item.(map[string]any)
		if step["name"] == "validate-output" {
			want := []any{"stage-output", "--source=/var/workdir/source", "--output=/var/build-output", "--platform=$(params.PLATFORM)"}
			if !reflect.DeepEqual(step["args"], want) {
				t.Fatalf("args = %v; want %v", step["args"], want)
			}
			return
		}
	}
	t.Fatal("missing output validation step")
}
