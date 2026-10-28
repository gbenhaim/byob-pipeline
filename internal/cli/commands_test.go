package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestPrepareStagesLauncher verifies that preparation accepts image-provided
// executables and stages an unchanged, executable copy outside the checkout.
func TestPrepareStagesLauncher(t *testing.T) {
	source := t.TempDir()
	tools := filepath.Join(t.TempDir(), "tools")
	if err := runCLI(t, "prepare", "--source="+source, "--tools="+tools); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(tools, "byob")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0555 {
		t.Fatalf("launcher mode = %v, want regular file with mode 0555", info.Mode())
	}
	original, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	originalData, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	installedData, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(installedData) != sha256.Sum256(originalData) {
		t.Fatal("installed launcher differs from the running executable")
	}
	entries, err := os.ReadDir(source)
	if err != nil || len(entries) != 0 {
		t.Fatalf("preparation changed checkout: entries=%v error=%v", entries, err)
	}
}

// TestPrepareRejectsInvalidSourceBeforeInstalling prevents launcher staging when
// source validation has failed.
func TestPrepareRejectsInvalidSourceBeforeInstalling(t *testing.T) {
	tools := filepath.Join(t.TempDir(), "tools")
	if err := runCLI(t, "prepare", "--source="+filepath.Join(t.TempDir(), "missing"), "--tools="+tools); err == nil {
		t.Fatal("accepted missing source directory")
	}
	if _, err := os.Stat(tools); !os.IsNotExist(err) {
		t.Fatalf("launcher directory was created for invalid source: %v", err)
	}
}

// TestPrepareWithoutTools validates source without requiring a checkout script or
// a launcher installation directory.
func TestPrepareWithoutTools(t *testing.T) {
	if err := runCLI(t, "prepare", "--source="+t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

// TestCommandHelp checks that users can discover each command and its argv
// boundary without running a Task operation.
func TestCommandHelp(t *testing.T) {
	for _, scenario := range []struct {
		args []string
		want string
	}{
		{[]string{"--help"}, "exec-build"},
		{[]string{"prepare", "--help"}, "--tools"},
		{[]string{"exec-build", "--help"}, "COMMAND"},
		{[]string{"build-remote", "--help"}, "COMMAND"},
	} {
		var stdout, stderr bytes.Buffer
		if err := Run(context.Background(), scenario.args, &stdout, &stderr); err != nil {
			t.Fatalf("%v: %v", scenario.args, err)
		}
		if !strings.Contains(stdout.String(), scenario.want) || stderr.Len() != 0 {
			t.Fatalf("%v: stdout=%q stderr=%q", scenario.args, stdout.String(), stderr.String())
		}
	}
}

// TestBuildCommandsRequireArgv rejects omitted commands, empty executable names,
// and ambiguous argv without the separator before any operation runs.
func TestBuildCommandsRequireArgv(t *testing.T) {
	for _, command := range []string{"exec-build", "build-remote"} {
		for _, suffix := range [][]string{nil, {"--"}, {"--", ""}, {"./build"}} {
			args := append([]string{command}, suffix...)
			if err := runCLI(t, args...); err == nil || !strings.Contains(err.Error(), "command") {
				t.Fatalf("missing or ambiguous command %q did not report a command error: %v", args, err)
			}
		}
	}
	if err := runCLI(t, "prepare", "--script=build.sh"); err == nil {
		t.Fatal("accepted removed script flag")
	}
}

// TestNoCommandReportsMissingSubcommand ensures an explicitly empty invocation
// cannot fall back to arguments from the process embedding the CLI.
func TestNoCommandReportsMissingSubcommand(t *testing.T) {
	originalArgs := os.Args
	os.Args = []string{"embedding-process", "--help"}
	t.Cleanup(func() { os.Args = originalArgs })
	for _, args := range [][]string{nil, {}} {
		err := runCLI(t, args...)
		if err == nil || !strings.Contains(err.Error(), "subcommand is required") {
			t.Fatalf("empty args %v returned %v", args, err)
		}
	}
}

type buildProbe struct {
	Args []string
	Home string
	CWD  string
}

// TestExecBuildPreservesArgvAndExit executes the test binary without a shebang or
// a shell and checks explicit, relative, and PATH-based executable selection.
func TestExecBuildPreservesArgvAndExit(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range []string{"absolute", "relative", "path"} {
		t.Run(selection, func(t *testing.T) {
			source := t.TempDir()
			home := filepath.Join(t.TempDir(), "home")
			bin := t.TempDir()
			command := executable
			switch selection {
			case "relative":
				if err := os.Symlink(executable, filepath.Join(source, "builder")); err != nil {
					t.Fatal(err)
				}
				command = "./builder"
			case "path":
				if err := os.Symlink(executable, filepath.Join(bin, "builder")); err != nil {
					t.Fatal(err)
				}
				command = "builder"
			}
			injected := filepath.Join(source, "injected")
			wantArgs := []string{"two words", "", "--source=/unexpected", "; touch " + injected, "$(touch " + injected + ")", "'quoted'", "a|b", "*"}
			args := []string{"-test.run=^TestCLIHelper$", "--", "cli", "exec-build", "--source=" + source, "--home=" + home, "--", command, "-test.run=^TestCLIHelper$", "--", "probe"}
			cmd := exec.Command(executable, append(args, wantArgs...)...)
			cmd.Env = append(os.Environ(), "BYOB_CLI_HELPER=1", "PATH="+bin)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 17 {
				t.Fatalf("exit=%v, want 17; output=%s", err, output)
			}
			var probe buildProbe
			if err := json.Unmarshal(output, &probe); err != nil {
				t.Fatalf("decode probe: %v; output=%s", err, output)
			}
			if !reflect.DeepEqual(probe.Args, wantArgs) || probe.CWD != source || probe.Home != home {
				t.Fatalf("probe=%+v, want args=%q cwd=%q home=%q", probe, wantArgs, source, home)
			}
			if info, err := os.Stat(home); err != nil || !info.IsDir() {
				t.Fatalf("build home was not created: %v", err)
			}
			if _, err := os.Stat(injected); !os.IsNotExist(err) {
				t.Fatalf("shell syntax was executed: %v", err)
			}
		})
	}
}

// TestCLIHelper isolates process replacement from the test runner and emits the
// environment observed by the directly executed build command.
func TestCLIHelper(t *testing.T) {
	if os.Getenv("BYOB_CLI_HELPER") != "1" {
		return
	}
	var args []string
	for index, value := range os.Args {
		if value == "--" {
			args = os.Args[index+1:]
			break
		}
	}
	if len(args) == 0 {
		t.Fatal("helper mode is missing")
	}
	switch args[0] {
	case "cli":
		if err := Run(context.Background(), args[1:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case "probe":
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(buildProbe{Args: args[1:], Home: os.Getenv("HOME"), CWD: cwd}); err != nil {
			t.Fatal(err)
		}
		os.Exit(17)
	default:
		t.Fatalf("unknown helper mode %q", args[0])
	}
}
