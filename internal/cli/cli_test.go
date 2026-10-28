package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureCommand installs a disposable helper executable at the front of PATH.
func fixtureCommand(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nset -eu\n"+script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// runCLI invokes the public CLI and includes captured output when it fails.
func runCLI(t *testing.T, args ...string) error {
	t.Helper()
	var output bytes.Buffer
	err := Run(context.Background(), args, &output, &output)
	if err != nil {
		t.Log(output.String())
	}
	return err
}

// TestRestoreInputs covers reference validation, indexed restoration, empty
// optional inputs, and stopping after a failed artifact restore.
func TestRestoreInputs(t *testing.T) {
	ref := "oci:registry.example/build@sha256:" + strings.Repeat("a", 64)
	t.Run("empty", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "inputs")
		if err := runCLI(t, "restore-inputs", "--root="+root, "--"); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("entries=%v error=%v", entries, err)
		}
		if err := runCLI(t, "restore-inputs", "--require", "--root="+root, "--"); err == nil {
			t.Fatal("pack accepted no inputs")
		}
	})
	t.Run("collisions", func(t *testing.T) {
		fixtureCommand(t, "use-archive", `destination=${1#*=}
mkdir -p "$destination/out"
printf '%s' "${1%%=*}" > "$destination/out/shared.txt"
`)
		root := filepath.Join(t.TempDir(), "inputs")
		second := "oci:registry.example/other@sha256:" + strings.Repeat("b", 64)
		if err := runCLI(t, "restore-inputs", "--root="+root, "--", ref, second); err != nil {
			t.Fatal(err)
		}
		for index, want := range map[string]string{"0": ref, "1": second} {
			data, err := os.ReadFile(filepath.Join(root, index, "out/shared.txt"))
			if err != nil || string(data) != want {
				t.Fatalf("input %s = %s, %v", index, data, err)
			}
		}
	})
	t.Run("reject invalid", func(t *testing.T) {
		for _, value := range []string{"", "invalid", "oci:repo@sha256:short", "oci:repo=else@sha256:" + strings.Repeat("a", 64)} {
			if err := runCLI(t, "restore-inputs", "--root="+t.TempDir(), "--", value); err == nil {
				t.Fatalf("accepted %q", value)
			}
		}
	})
	t.Run("stop on failure", func(t *testing.T) {
		fixtureCommand(t, "use-archive", "exit 17\n")
		root := t.TempDir()
		err := runCLI(t, "restore-inputs", "--root="+root, "--", ref, ref)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 17 {
			t.Fatalf("lost restore error: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "1")); !os.IsNotExist(err) {
			t.Fatalf("attempted second restore: %v", err)
		}
	})
}

// TestPublish verifies successful publication results and ensures failed pushes,
// attachments, or invalid digests cannot produce trusted result files.
func TestPublish(t *testing.T) {
	for _, scenario := range []string{"success", "push failure", "attachment failure", "invalid digest"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			digest := "sha256:" + strings.Repeat("a", 64)
			script := "printf '%s' '{\"digest\":\"" + digest + "\"}'\n"
			if scenario == "push failure" {
				script = "exit 9\n"
			}
			if scenario == "invalid digest" {
				script = "printf '%s' '{\"digest\":\"invalid\"}'\n"
			}
			fixtureCommand(t, "oras", script)
			attach := "exit 0\n"
			if scenario == "attachment failure" {
				attach = "exit 7\n"
			}
			fixtureCommand(t, "cosign", attach)
			for name, data := range map[string]string{"payload.tar.gz": "payload", "sbom.spdx.json": `{"spdxVersion":"SPDX-2.3"}`} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0644); err != nil {
					t.Fatal(err)
				}
			}
			err := runCLI(t, "publish", "--workdir="+root, "--image=localhost:5000/team/output:test", "--url-result="+root+"/url", "--digest-result="+root+"/digest", "--sbom-result="+root+"/sbom", "--auth-dir="+root+"/no-auth")
			if scenario != "success" {
				if err == nil {
					t.Fatal("failure reported success")
				}
				for _, name := range []string{"url", "digest", "sbom"} {
					if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
						t.Fatalf("result %s was written before publication succeeded", name)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]string{"url": "localhost:5000/team/output:test", "digest": digest} {
				data, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || string(data) != want {
					t.Fatalf("%s=%s error=%v", name, data, err)
				}
			}
			data, err := os.ReadFile(filepath.Join(root, "sbom"))
			if err != nil || !strings.HasPrefix(string(data), "localhost:5000/team/output@sha256:") {
				t.Fatalf("sbom=%s error=%v", data, err)
			}
		})
	}
}

// TestInvalidCommandAndArguments rejects invalid invocations and keeps general
// help available without executing a Task operation.
func TestInvalidCommandAndArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"prepare", "--unknown"}, {"prepare", "unexpected"}, {"publish"}} {
		if err := runCLI(t, args...); err == nil {
			t.Fatalf("accepted invalid arguments %v", args)
		}
	}
	var output bytes.Buffer
	if err := Run(context.Background(), []string{"--help"}, &output, &output); err != nil || !strings.Contains(output.String(), "restore-inputs") {
		t.Fatalf("help=%q error=%v", output.String(), err)
	}
}
