package main

import (
	"strings"
	"testing"
)

func fixture() map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": "run-build"},
		"spec": map[string]any{
			"params": []any{}, "results": []any{}, "volumes": []any{},
			"steps": []any{
				map[string]any{"name": "prepare", "image": "trusted@sha256:abc"},
				map[string]any{"name": "build", "image": "$(params.image)", "command": []any{"/bin/sh"}, "args": []any{"./build.sh"}, "volumeMounts": []any{map[string]any{"name": "workdir", "mountPath": "/var/workdir/source", "subPath": "source"}}},
				map[string]any{"name": "validate-output", "env": []any{}},
			},
		},
	}
}

func TestMissingBuildFailsInsteadOfEmittingNonRemoteTask(t *testing.T) {
	task := fixture()
	task["spec"].(map[string]any)["steps"] = []any{}
	err := convert(task, "build", "run-build-remote", "select", "remote", "record")
	if err == nil || !strings.Contains(err.Error(), "build") {
		t.Fatalf("expected missing build step error, got %v", err)
	}
}

func TestRoutingKeepsSecretOffDeveloperImage(t *testing.T) {
	task := fixture()
	if err := convert(task, "build", "run-build-remote", "select", "remote", "record"); err != nil {
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
	if found["build-remote"]["script"] != "remote" {
		t.Fatal("remote helper not installed")
	}
	if task["metadata"].(map[string]any)["name"] != "run-build-remote" {
		t.Fatal("wrong task name")
	}
}
