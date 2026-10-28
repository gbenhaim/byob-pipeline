package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const requestedImage = "registry.example:5000/builder@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const observedImage = "registry.example:5000/builder@sha256:0000000000000000000000000000000000000000000000000000000000000000"

func TestSelectPlatform(t *testing.T) {
	for _, tt := range []struct{ name, host, mode string }{
		{"localhost needs no key", "localhost\n", "local"},
		{"controller remote", "builder@remote.example\n", "remote"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, "host"), tt.host)
			result := filepath.Join(root, "mode")
			if err := SelectPlatform(root, result); err != nil {
				t.Fatal(err)
			}
			assertFile(t, result, tt.mode)
		})
	}
}

func TestSelectPlatformRejectsControllerErrorsAndMissingHost(t *testing.T) {
	for _, tt := range []struct{ name, host, controllerError string }{
		{"empty host", "\n", ""},
		{"controller error overrides localhost", "localhost", "allocation failed"},
		{"missing host", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.host != "" {
				write(t, filepath.Join(root, "host"), tt.host)
			}
			if tt.controllerError != "" {
				write(t, filepath.Join(root, "error"), tt.controllerError)
			}
			result := filepath.Join(root, "mode")
			err := SelectPlatform(root, result)
			if err == nil {
				t.Fatal("controller failure was accepted")
			}
			if tt.controllerError != "" && !strings.Contains(err.Error(), tt.controllerError) {
				t.Fatalf("controller error missing from %v", err)
			}
			assertMissing(t, result)
		})
	}
}

func TestRecordImage(t *testing.T) {
	for _, mode := range []string{"local", "remote"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, "builder-image"), observedImage)
			result := filepath.Join(root, "result")
			if err := RecordImage(mode, requestedImage, root, result); err != nil {
				t.Fatal(err)
			}
			var got map[string]string
			if err := json.Unmarshal(read(t, result), &got); err != nil {
				t.Fatal(err)
			}
			wantDigest := "sha256:" + strings.Repeat("a", 64)
			if mode == "remote" {
				wantDigest = "sha256:" + strings.Repeat("0", 64)
			}
			if got["uri"] != "registry.example:5000/builder" || got["digest"] != wantDigest || len(got) != 2 {
				t.Fatalf("incorrect image provenance: %#v", got)
			}
		})
	}
}

func TestRecordImageRejectsMutableOrMalformedImage(t *testing.T) {
	for _, image := range []string{"builder:latest", "builder@sha256:abc", "@sha256:" + strings.Repeat("a", 64), "builder@sha256:" + strings.Repeat("A", 64)} {
		root := t.TempDir()
		result := filepath.Join(root, "result")
		if err := RecordImage("local", image, root, result); err == nil {
			t.Errorf("accepted %q", image)
		}
		assertMissing(t, result)
	}
	root := t.TempDir()
	if err := RecordImage("remote", requestedImage, root, filepath.Join(root, "result")); err == nil {
		t.Fatal("remote mode accepted a missing observed image")
	}
}

func TestBuildReturnsOnlyOutputsAndPreservesLinksAndIndexedInputs(t *testing.T) {
	f := newBuildFixture(t)
	write(t, filepath.Join(f.options.SourceRoot, "out", "stale.txt"), "previous output")
	var stdout, stderr bytes.Buffer
	f.options.Stdout, f.options.Stderr = &stdout, &stderr
	if err := Build(context.Background(), f.options); err != nil {
		t.Fatalf("build: %v\n%s", err, stderr.String())
	}
	assertFile(t, filepath.Join(f.options.SourceRoot, "out", "output.txt"), "expected output")
	assertFile(t, filepath.Join(f.options.SourceRoot, "out", "first.txt"), "first build output")
	assertFile(t, filepath.Join(f.options.SourceRoot, "out", "second.txt"), "second build output")
	assertFile(t, filepath.Join(f.options.SourceRoot, "private-source.txt"), "must not change")
	assertFile(t, filepath.Join(f.options.ControlRoot, "builder-image"), observedImage)
	assertMissing(t, filepath.Join(f.options.SourceRoot, "out", "stale.txt"))
	assertMissing(t, filepath.Join(f.options.SourceRoot, "remote-only.txt"))
	for name, want := range map[string]string{"same-link": "./output.txt", "sub/parent-link": "../output.txt"} {
		got, err := os.Readlink(filepath.Join(f.options.SourceRoot, "out", name))
		if err != nil || got != want {
			t.Fatalf("symlink %s: %q, %v; want %q", name, got, err, want)
		}
	}
	if !strings.Contains(stdout.String(), "developer output") || !strings.Contains(stderr.String(), "developer error stream") {
		t.Fatalf("developer streams lost: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	f.assertCleaned(t, true)
}

func TestBuildUsesStrictKnownHostsAndPrivateKey(t *testing.T) {
	f := newBuildFixture(t)
	write(t, filepath.Join(f.options.SSHDir, "known_hosts"), "fixture.invalid ssh-ed25519 fixture\n")
	if err := Build(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	config := string(read(t, filepath.Join(f.root, "ssh-config")))
	for _, required := range []string{"stricthostkeychecking true\n", "batchmode yes\n", "identitiesonly yes\n"} {
		if !strings.Contains(config, required) {
			t.Errorf("OpenSSH did not apply %q: %s", required, config)
		}
	}
	assertFile(t, filepath.Join(f.root, "key-mode"), "600\n")
	assertFile(t, filepath.Join(f.root, "key-copy"), "fixture key")
	assertFile(t, filepath.Join(f.root, "known-hosts-copy"), "fixture.invalid ssh-ed25519 fixture\n")
	f.assertCleaned(t, true)
}

func TestBuildReturnsOutputAcrossSeparateControlAndSourceFilesystems(t *testing.T) {
	f := newBuildFixture(t)
	var controlStat, alternateStat syscall.Stat_t
	if err := syscall.Stat(f.root, &controlStat); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Stat("/dev/shm", &alternateStat); err != nil || controlStat.Dev == alternateStat.Dev {
		t.Skip("a separate /dev/shm filesystem is unavailable")
	}
	source, err := os.MkdirTemp("/dev/shm", "byob-source-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(source) })
	if output, err := exec.Command("cp", "-a", f.options.SourceRoot+"/.", source).CombinedOutput(); err != nil {
		t.Fatalf("prepare source fixture: %v: %s", err, output)
	}
	f.options.SourceRoot = source
	if err := Build(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(source, "out", "output.txt"), "expected output")
	f.assertCleaned(t, true)
}

func TestBuildQuotesScriptAsOneArgument(t *testing.T) {
	f := newBuildFixture(t)
	f.options.ScriptPath = "build'; touch " + filepath.Join(f.root, "injected") + "; #\n$(touch " + filepath.Join(f.root, "injected") + ")"
	if err := Build(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	args := bytes.Split(bytes.TrimSuffix(read(t, filepath.Join(f.root, "create-args")), []byte{0}), []byte{0})
	if string(args[len(args)-1]) != f.options.ScriptPath {
		t.Fatalf("script was not passed intact: %q", args[len(args)-1])
	}
	assertMissing(t, filepath.Join(f.root, "injected"))
}

func TestBuildRejectsUnsafeControllerDataBeforeSSH(t *testing.T) {
	for _, tt := range []struct{ name, file, value string }{
		{"host option", "host", "-oProxyCommand=touch /tmp/injected"},
		{"host command", "host", "fixture.invalid; true"},
		{"relative user directory", "user-dir", "relative"},
		{"parent directory", "user-dir", "/tmp/../elsewhere"},
		{"shell user directory", "user-dir", "/tmp/$(whoami)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newBuildFixture(t)
			write(t, filepath.Join(f.options.SSHDir, tt.file), tt.value)
			if err := Build(context.Background(), f.options); err == nil {
				t.Fatal("unsafe controller data accepted")
			}
			assertMissing(t, filepath.Join(f.root, "ssh-config"))
			f.assertCleaned(t, false)
		})
	}
}

func TestBuildFailureCleansUpAndKeepsLocalSource(t *testing.T) {
	for _, mode := range []string{"start-failure", "container-failure", "symlink-out", "missing-out", "not-rootless", "bad-digest", "return-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newBuildFixture(t)
			t.Setenv("FIXTURE_MODE", mode)
			write(t, filepath.Join(f.options.SourceRoot, "out", "existing.txt"), "keep existing output")
			if err := Build(context.Background(), f.options); err == nil {
				t.Fatal("failed remote build was accepted")
			}
			assertFile(t, filepath.Join(f.options.SourceRoot, "private-source.txt"), "must not change")
			assertFile(t, filepath.Join(f.options.SourceRoot, "out", "existing.txt"), "keep existing output")
			f.assertCleaned(t, mode != "not-rootless" && mode != "bad-digest")
		})
	}
}

func TestBuildCancellationCleansRemoteContainerAndCredentials(t *testing.T) {
	for _, mode := range []string{"wait", "create-wait"} {
		t.Run(mode, func(t *testing.T) { testBuildCancellation(t, mode) })
	}
}

func testBuildCancellation(t *testing.T, mode string) {
	t.Helper()
	f := newBuildFixture(t)
	t.Setenv("FIXTURE_MODE", mode)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Build(ctx, f.options) }()
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(filepath.Join(f.root, "started")); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("build exited before cancellation: %v", err)
		case <-deadline:
			t.Fatal("build did not start")
		case <-tick.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled build succeeded")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled build lost its context error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled build did not finish promptly")
	}
	f.assertCleaned(t, true)
}

func TestBuildExchangesOTPUsingControllerCA(t *testing.T) {
	f := newBuildFixture(t)
	var gotBody, gotMethod string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody, gotMethod = string(body), r.Method
		fmt.Fprint(w, "key from OTP")
	}))
	defer server.Close()
	write(t, filepath.Join(f.options.SSHDir, "otp"), "opaque\x00token\n")
	write(t, filepath.Join(f.options.SSHDir, "otp-server"), server.URL)
	write(t, filepath.Join(f.options.SSHDir, "otp-ca"), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})))
	if err := os.Remove(filepath.Join(f.options.SSHDir, "id_rsa")); err != nil {
		t.Fatal(err)
	}
	if err := Build(context.Background(), f.options); err != nil {
		t.Fatal(err)
	}
	if gotBody != "opaque\x00token\n" || gotMethod != http.MethodPost {
		t.Fatalf("OTP request: %q %q", gotMethod, gotBody)
	}
	assertFile(t, filepath.Join(f.root, "key-copy"), "key from OTP\n")
	f.assertCleaned(t, true)
}

func TestBuildRejectsFailedOTPExchangeWithoutStartingSSH(t *testing.T) {
	for _, mode := range []string{"untrusted-ca", "http-error", "plaintext", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			f := newBuildFixture(t)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, "http://example.invalid/key", http.StatusTemporaryRedirect)
					return
				}
				http.Error(w, "secret diagnostic", http.StatusForbidden)
			}))
			defer server.Close()
			write(t, filepath.Join(f.options.SSHDir, "otp"), "secret OTP")
			serverURL := server.URL
			if mode == "plaintext" {
				serverURL = "http://example.invalid/key"
			}
			write(t, filepath.Join(f.options.SSHDir, "otp-server"), serverURL)
			ca := server.Certificate().Raw
			if mode == "untrusted-ca" {
				public, private, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				template := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true,
					KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
				ca, err = x509.CreateCertificate(rand.Reader, template, template, public, private)
				if err != nil {
					t.Fatal(err)
				}
			}
			write(t, filepath.Join(f.options.SSHDir, "otp-ca"), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca})))
			err := Build(context.Background(), f.options)
			if err == nil {
				t.Fatal("failed or insecure OTP exchange accepted")
			}
			if strings.Contains(err.Error(), "secret OTP") || strings.Contains(err.Error(), "secret diagnostic") {
				t.Fatalf("error exposes credentials: %v", err)
			}
			assertMissing(t, filepath.Join(f.root, "ssh-config"))
			f.assertCleaned(t, false)
		})
	}
}

type buildFixture struct {
	root    string
	options BuildOptions
}

func newBuildFixture(t *testing.T) buildFixture {
	t.Helper()
	root := t.TempDir()
	f := buildFixture{root: root, options: BuildOptions{
		SSHDir: filepath.Join(root, "ssh"), SourceRoot: filepath.Join(root, "source with space"),
		InputRoot: filepath.Join(root, "inputs"), ControlRoot: filepath.Join(root, "control"),
		BuilderImage: requestedImage, ScriptPath: "build.sh", Stdout: io.Discard, Stderr: io.Discard,
	}}
	for _, name := range []string{"ssh", "source with space", "inputs", "remote", "private", "bin"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(f.options.SSHDir, "host"), "builder@fixture.invalid\n")
	write(t, filepath.Join(f.options.SSHDir, "user-dir"), filepath.Join(root, "remote"))
	write(t, filepath.Join(f.options.SSHDir, "id_rsa"), "fixture key")
	write(t, filepath.Join(f.options.SourceRoot, "private-source.txt"), "must not change")
	write(t, filepath.Join(f.options.SourceRoot, "relative-links", "output.txt"), "expected output")
	if err := os.MkdirAll(filepath.Join(f.options.SourceRoot, "relative-links", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"same-link": "./output.txt", "sub/parent-link": "../output.txt"} {
		if err := os.Symlink(target, filepath.Join(f.options.SourceRoot, "relative-links", name)); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(f.options.InputRoot, "0", "out", "generated.txt"), "first build output")
	write(t, filepath.Join(f.options.InputRoot, "1", "out", "generated.txt"), "second build output")
	write(t, filepath.Join(root, "bin", "ssh"), sshFixture)
	write(t, filepath.Join(root, "bin", "podman"), podmanFixture)
	for _, name := range []string{"ssh", "podman"} {
		if err := os.Chmod(filepath.Join(root, "bin", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", filepath.Join(root, "bin")+":"+os.Getenv("PATH"))
	t.Setenv("TMPDIR", filepath.Join(root, "private"))
	t.Setenv("FIXTURE_ROOT", root)
	t.Setenv("FIXTURE_MODE", "success")
	return f
}

func (f buildFixture) assertCleaned(t *testing.T, container bool) {
	t.Helper()
	for _, directory := range []string{"remote", "private"} {
		entries, err := os.ReadDir(filepath.Join(f.root, directory))
		if err != nil || len(entries) != 0 {
			t.Errorf("%s not cleaned: %v %v", directory, entries, err)
		}
	}
	if container {
		if _, err := os.Stat(filepath.Join(f.root, "removed-container")); err != nil {
			t.Errorf("container not removed: %v", err)
		}
	}
	entries, err := os.ReadDir(f.options.ControlRoot)
	if err == nil {
		for _, entry := range entries {
			if entry.Name() != "builder-image" {
				t.Errorf("temporary control entry remains: %s", entry.Name())
			}
		}
	}
	entries, err = os.ReadDir(f.options.SourceRoot)
	if err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".byob-returned-") {
				t.Errorf("temporary output remains: %s", entry.Name())
			}
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	if got := string(read(t, path)); got != want {
		t.Errorf("%s = %q, want %q", path, got, want)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s should not exist: %v", path, err)
	}
}

const sshFixture = `#!/bin/bash
set -euo pipefail
options=()
key=''
known_hosts=''
while [[ $1 == -* ]]; do
    options+=("$1" "$2")
    if [[ $1 == -i ]]; then key=$2; fi
    if [[ $1 == -o && $2 == UserKnownHostsFile=* ]]; then known_hosts=${2#UserKnownHostsFile=}; fi
    shift 2
done
host=$1
shift
[[ $host == builder@fixture.invalid ]] || exit 90
/usr/bin/ssh -G "${options[@]}" "$host" > "$FIXTURE_ROOT/ssh-config" 2>/dev/null
stat -c %a "$key" > "$FIXTURE_ROOT/key-mode"
cp "$key" "$FIXTURE_ROOT/key-copy"
if [[ -f $known_hosts ]]; then cp "$known_hosts" "$FIXTURE_ROOT/known-hosts-copy"; fi
if [[ $FIXTURE_MODE == return-failure && $* == 'tar -C '* ]]; then bash -c "$*"; exit 31; fi
exec bash -c "$*"
`

const podmanFixture = `#!/bin/bash
set -euo pipefail
[[ $1 == --default-mounts-file=/dev/null ]] || exit 88
shift
case "$1" in
 info) if [[ $FIXTURE_MODE == not-rootless ]]; then printf false; else printf true; fi ;;
 pull) : ;;
 image) if [[ $FIXTURE_MODE == bad-digest ]]; then printf invalid; else printf 'sha256:%064d' 0; fi ;;
 create)
   printf '%s\0' "$@" > "$FIXTURE_ROOT/create-args"
   previous=''
   for argument in "$@"; do
       if [[ $previous == --cidfile ]]; then printf fixture-container > "$argument"; fi
       previous=$argument
   done
   if [[ $FIXTURE_MODE == create-wait ]]; then touch "$FIXTURE_ROOT/started"; sleep 60; fi
   printf fixture-container ;;
 cp)
   if [[ $2 == - ]]; then
       mkdir -p "$FIXTURE_ROOT/received"
       tar -xf - -C "$FIXTURE_ROOT/received"
   else
       cp -a "$FIXTURE_ROOT/received${2#fixture-container:}" "$3"
   fi ;;
 start)
   cat >/dev/null
   if [[ $FIXTURE_MODE == start-failure ]]; then exit 42; fi
   if [[ $FIXTURE_MODE == wait ]]; then touch "$FIXTURE_ROOT/started"; sleep 60; fi
   source=$FIXTURE_ROOT/received/var/workdir/source
   printf mutated > "$source/private-source.txt"
   printf private > "$source/remote-only.txt"
   rm -rf "$source/out"
   if [[ $FIXTURE_MODE == symlink-out ]]; then ln -s . "$source/out"; exit 0; fi
   if [[ $FIXTURE_MODE == missing-out ]]; then exit 0; fi
   mkdir -p "$source/out"
   cp -a "$source/relative-links/." "$source/out/"
   cp "$FIXTURE_ROOT/received/var/workdir/inputs/0/out/generated.txt" "$source/out/first.txt"
   cp "$FIXTURE_ROOT/received/var/workdir/inputs/1/out/generated.txt" "$source/out/second.txt"
   printf 'developer output\n'
   printf 'developer error stream\n' >&2 ;;
 inspect) if [[ $FIXTURE_MODE == container-failure ]]; then printf 7; else printf 0; fi ;;
 rm) touch "$FIXTURE_ROOT/removed-container" ;;
 *) exit 99 ;;
esac
`
