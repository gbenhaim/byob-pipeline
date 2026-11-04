package remote

import (
	"archive/tar"
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
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gbenhaim/byob-pipeline/internal/artifact"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const requestedImage = "registry.example:5000/builder@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const observedImage = "registry.example:5000/builder@sha256:0000000000000000000000000000000000000000000000000000000000000000"

// TestRemote runs the remote package's Ginkgo specs.
func TestRemote(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Remote Suite")
}

var _ = Describe("build command files", func() {
	It("appends build arguments to the recorded executable", func() {
		path := filepath.Join(GinkgoT().TempDir(), "build-cmd")
		Expect(WriteBuildCommand(path, []string{"./build.sh", "--config"})).To(Succeed())

		got, err := LoadBuildCommand(path, []string{"release"})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]string{"./build.sh", "--config", "release"}))
	})

	It("rejects an empty command when arguments are present", func() {
		dir := GinkgoT().TempDir()
		_, err := LoadBuildCommand(filepath.Join(dir, "missing"), []string{"./build.sh"})
		Expect(err).To(HaveOccurred())

		empty := filepath.Join(dir, "empty")
		Expect(os.WriteFile(empty, []byte("[]\n"), 0o600)).To(Succeed())
		_, err = LoadBuildCommand(empty, []string{"./build.sh"})
		Expect(err).To(HaveOccurred())

		blank := filepath.Join(dir, "blank")
		Expect(os.WriteFile(blank, []byte("[\"\"]\n"), 0o600)).To(Succeed())
		_, err = LoadBuildCommand(blank, []string{"./build.sh"})
		Expect(err).To(HaveOccurred())
		Expect(WriteBuildCommand(blank, []string{""})).To(HaveOccurred())
	})
})

var _ = Describe("remote builds", func() {
	Describe("platform selection", func() {
		DescribeTable("selects the controller platform",
			func(host, wantMode string) {
				t := GinkgoT()
				root := t.TempDir()
				write(t, filepath.Join(root, "host"), host)
				result := filepath.Join(root, "mode")

				Expect(SelectPlatform(root, result)).To(Succeed())
				assertFile(t, result, wantMode)
			},
			Entry("localhost needs no key", "localhost\n", "local"),
			Entry("controller remote", "builder@remote.example\n", "remote"),
		)

		DescribeTable("rejects controller errors and missing hosts",
			func(host, controllerError string) {
				t := GinkgoT()
				root := t.TempDir()
				if host != "" {
					write(t, filepath.Join(root, "host"), host)
				}

				if controllerError != "" {
					write(t, filepath.Join(root, "error"), controllerError)
				}

				result := filepath.Join(root, "mode")
				err := SelectPlatform(root, result)
				Expect(err).To(HaveOccurred())
				if controllerError != "" && err != nil {
					Expect(err.Error()).To(ContainSubstring(controllerError))
				}

				assertMissing(t, result)
			},
			Entry("empty host", "\n", ""),
			Entry("controller error overrides localhost", "localhost", "allocation failed"),
			Entry("missing host", "", ""),
		)
	})

	Describe("image provenance", func() {
		DescribeTable("records the local or observed remote digest",
			func(mode string) {
				t := GinkgoT()
				root := t.TempDir()
				write(t, filepath.Join(root, "builder-image"), observedImage)
				result := filepath.Join(root, "result")

				Expect(RecordImage(mode, requestedImage, root, result)).To(Succeed())

				var got map[string]string
				Expect(json.Unmarshal(read(t, result), &got)).To(Succeed())
				wantDigest := "sha256:" + strings.Repeat("a", 64)
				if mode == "remote" {
					wantDigest = "sha256:" + strings.Repeat("0", 64)
				}

				Expect(got).To(HaveKeyWithValue("uri", "registry.example:5000/builder"))
				Expect(got).To(HaveKeyWithValue("digest", wantDigest))
				Expect(got).To(HaveLen(2))
			},
			Entry("local image", "local"),
			Entry("observed remote image", "remote"),
		)

		DescribeTable("rejects mutable and malformed image references",
			func(image string) {
				t := GinkgoT()
				root := t.TempDir()
				result := filepath.Join(root, "result")

				Expect(RecordImage("local", image, root, result)).To(HaveOccurred())
				assertMissing(t, result)
			},
			Entry("tag reference", "builder:latest"),
			Entry("short digest", "builder@sha256:abc"),
			Entry("missing image name", "@sha256:"+strings.Repeat("a", 64)),
			Entry("uppercase digest", "builder@sha256:"+strings.Repeat("A", 64)),
		)

		It("requires observed metadata for a remote image", func() {
			t := GinkgoT()
			root := t.TempDir()

			Expect(RecordImage("remote", requestedImage, root, filepath.Join(root, "result"))).To(HaveOccurred())
		})
	})

	Describe("Build", func() {
		It("returns only build outputs and preserves links, indexed inputs, and developer streams", func() {
			t := GinkgoT()
			f := newBuildFixture(t)
			write(t, filepath.Join(f.options.SourceRoot, "out", "stale.txt"), "previous output")
			var stdout, stderr bytes.Buffer
			f.options.Stdout, f.options.Stderr = &stdout, &stderr

			Expect(Build(context.Background(), f.options)).To(Succeed(), stderr.String())
			assertFile(t, filepath.Join(f.options.OutputRoot, "output.txt"), "expected output")
			assertFile(t, filepath.Join(f.options.OutputRoot, "first.txt"), "first build output")
			assertFile(t, filepath.Join(f.options.OutputRoot, "second.txt"), "second build output")
			assertFile(t, filepath.Join(f.options.SourceRoot, "private-source.txt"), "must not change")
			assertFile(t, filepath.Join(f.options.SourceRoot, "out", "stale.txt"), "previous output")
			assertFile(t, filepath.Join(f.options.ControlRoot, "builder-image"), observedImage)
			assertMissing(t, filepath.Join(f.options.OutputRoot, "stale.txt"))
			assertMissing(t, filepath.Join(f.options.SourceRoot, "remote-only.txt"))
			for name, want := range map[string]string{"same-link": "./output.txt", "sub/parent-link": "../output.txt"} {
				got, err := os.Readlink(filepath.Join(f.options.OutputRoot, name))
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal(want))
			}

			Expect(stdout.String()).To(ContainSubstring("developer output"))
			Expect(stderr.String()).To(ContainSubstring("developer error stream"))
			f.assertCleaned(t, true)
		})

		It("uses strict known hosts and the private key", func() {
			t := GinkgoT()
			f := newBuildFixture(t)
			write(t, filepath.Join(f.options.SSHDir, "known_hosts"), "fixture.invalid ssh-ed25519 fixture\n")

			Expect(Build(context.Background(), f.options)).To(Succeed())
			config := string(read(t, filepath.Join(f.root, "ssh-config")))
			Expect(config).To(ContainSubstring("stricthostkeychecking true\n"))
			Expect(config).To(ContainSubstring("batchmode yes\n"))
			Expect(config).To(ContainSubstring("identitiesonly yes\n"))
			assertFile(t, filepath.Join(f.root, "key-mode"), "600\n")
			assertFile(t, filepath.Join(f.root, "key-copy"), "fixture key")
			assertFile(t, filepath.Join(f.root, "known-hosts-copy"), "fixture.invalid ssh-ed25519 fixture\n")
			f.assertCleaned(t, true)
		})

		It("requires pinned known hosts before SSH", func() {
			t := GinkgoT()
			f := newBuildFixture(t)
			Expect(os.Remove(filepath.Join(f.options.SSHDir, "known_hosts"))).To(Succeed())

			err := Build(context.Background(), f.options)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("known_hosts"))
			assertMissing(t, filepath.Join(f.root, "ssh-config"))
		})

		It("does not transfer scrubbed git credentials", func() {
			t := GinkgoT()
			f := newBuildFixture(t)
			secret := "secret-token"
			Expect(os.MkdirAll(filepath.Join(f.options.SourceRoot, ".git"), 0o755)).To(Succeed())
			write(t, filepath.Join(f.options.SourceRoot, ".git", "config"), "url = https://user:"+secret+"@github.com/org/repo.git\n")
			write(t, filepath.Join(f.options.SourceRoot, ".git-credentials"), secret)
			Expect(artifact.ScrubCredentials(f.options.SourceRoot)).To(Succeed())

			Expect(Build(context.Background(), f.options)).To(Succeed())
			archives, err := filepath.Glob(filepath.Join(f.root, "copied-archive.*"))
			Expect(err).NotTo(HaveOccurred())
			Expect(archives).NotTo(BeEmpty())
			for _, archive := range archives {
				Expect(string(read(t, archive))).NotTo(ContainSubstring(secret))
			}

			transferred := findArchiveHeader(t, f.root, "var/source/.git/config")
			Expect(transferred.Name).To(Equal("var/source/.git/config"))
		})

		It("returns output onto a separate build-output filesystem", func() {
			t := GinkgoT()
			f := newBuildFixture(t)
			var controlStat, alternateStat syscall.Stat_t
			Expect(syscall.Stat(f.root, &controlStat)).To(Succeed())
			if err := syscall.Stat("/dev/shm", &alternateStat); err != nil || controlStat.Dev == alternateStat.Dev {
				Skip("a separate /dev/shm filesystem is unavailable")
			}

			outputRoot, err := os.MkdirTemp("/dev/shm", "byob-output-test-")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = os.RemoveAll(outputRoot) })
			f.options.OutputRoot = outputRoot

			Expect(Build(context.Background(), f.options)).To(Succeed())
			assertFile(t, filepath.Join(outputRoot, "output.txt"), "expected output")
			assertFile(t, filepath.Join(f.options.SourceRoot, "private-source.txt"), "must not change")
			f.assertCleaned(t, true)
		})

		DescribeTable("preserves command arguments",
			func(executable string) {
				t := GinkgoT()
				f := newBuildFixture(t)
				injection := "'; touch " + filepath.Join(f.root, "injected") + "; #\n$(touch " + filepath.Join(f.root, "injected") + ")"
				f.options.BuildCmd = []string{executable, "", "two words", "single'quote", `double"quote`, "line one\nline two", "--leading-dash", "&&", injection}

				Expect(Build(context.Background(), f.options)).To(Succeed())
				Expect(f.containerCommand(t)).To(Equal(f.options.BuildCmd))
				assertMissing(t, filepath.Join(f.root, "injected"))
			},
			Entry("space in executable", "/usr/bin/native build"),
			Entry("quotes in executable", `./literal['"executable`),
			Entry("JSON in executable", `["not", "an entrypoint array"]`),
		)

		It("uses an executable entrypoint without a shell", func() {
			t := GinkgoT()
			f := newBuildFixture(t)
			f.options.BuildCmd = []string{"/usr/bin/native-build"}

			Expect(Build(context.Background(), f.options)).To(Succeed())
			Expect(f.containerCommand(t)).To(Equal([]string{"/usr/bin/native-build"}))
		})

		DescribeTable("rejects an empty command before SSH",
			func(command []string) {
				t := GinkgoT()
				f := newBuildFixture(t)
				f.options.BuildCmd = command

				err := Build(context.Background(), f.options)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("build command"))
				assertMissing(t, filepath.Join(f.root, "ssh-config"))
				f.assertCleaned(t, false)
			},
			Entry("nil argv", []string(nil)),
			Entry("empty argv", []string{}),
			Entry("empty executable", []string{""}),
			Entry("empty executable with arguments", []string{"", "argument"}),
		)

		It("prepares a private HOME before starting the container", func() {
			t := GinkgoT()
			f := newBuildFixture(t)
			Expect(Build(context.Background(), f.options)).To(Succeed())

			home := findArchiveHeader(t, f.root, "var/workdir/home/")
			Expect(home.Typeflag).To(Equal(byte(tar.TypeDir)))
			Expect(home.Mode).To(Equal(int64(0o700)))
			Expect(home.Uid).To(Equal(1000))
			Expect(home.Gid).To(Equal(0))

			source := findArchiveHeader(t, f.root, "var/source/private-source.txt")
			Expect(source.Uid).To(Equal(1000))
			Expect(source.Gid).To(Equal(0))
			Expect(source.Mode).To(Equal(int64(0o600)))

			input := findArchiveHeader(t, f.root, "var/workdir/inputs/0/out/generated.txt")
			Expect(input.Uid).To(Equal(1000))
			Expect(input.Gid).To(Equal(0))
			Expect(input.Mode).To(Equal(int64(0o600)))

			parent := findArchiveHeader(t, f.root, "var/byob/")
			Expect(parent.Typeflag).To(Equal(byte(tar.TypeDir)))
			Expect(parent.Mode).To(Equal(int64(0o755)))
			Expect(parent.Uid).To(Equal(0))
			output := findArchiveHeader(t, f.root, "var/byob/output/")
			Expect(output.Typeflag).To(Equal(byte(tar.TypeDir)))
			Expect(output.Mode).To(Equal(int64(0o775)))
			Expect(output.Uid).To(Equal(1000))
			Expect(output.Gid).To(Equal(0))
			assertFile(t, filepath.Join(f.root, "home-before-start"), "")
			entries, err := os.ReadDir(filepath.Join(f.root, "received", "var", "workdir", "home"))
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(BeEmpty())
		})

		DescribeTable("rejects unsafe controller data before SSH",
			func(file, value string) {
				t := GinkgoT()
				f := newBuildFixture(t)
				write(t, filepath.Join(f.options.SSHDir, file), value)

				Expect(Build(context.Background(), f.options)).To(HaveOccurred())
				assertMissing(t, filepath.Join(f.root, "ssh-config"))
				f.assertCleaned(t, false)
			},
			Entry("host option", "host", "-oProxyCommand=touch /tmp/injected"),
			Entry("host command", "host", "fixture.invalid; true"),
			Entry("relative user directory", "user-dir", "relative"),
			Entry("parent directory", "user-dir", "/tmp/../elsewhere"),
			Entry("shell user directory", "user-dir", "/tmp/$(whoami)"),
		)

		DescribeTable("cleans up failures without changing the local source",
			func(mode string) {
				t := GinkgoT()
				f := newBuildFixture(t)
				t.Setenv("FIXTURE_MODE", mode)
				write(t, filepath.Join(f.options.SourceRoot, "out", "existing.txt"), "keep existing output")

				Expect(Build(context.Background(), f.options)).To(HaveOccurred())
				assertFile(t, filepath.Join(f.options.SourceRoot, "private-source.txt"), "must not change")
				assertFile(t, filepath.Join(f.options.SourceRoot, "out", "existing.txt"), "keep existing output")
				assertMissing(t, filepath.Join(f.options.ControlRoot, "builder-image"))
				f.assertCleaned(t, mode != "not-rootless" && mode != "bad-digest")
			},
			Entry("container start failure", "start-failure"),
			Entry("container execution failure", "container-failure"),
			Entry("home copy failure", "home-copy-failure"),
			Entry("output symlink", "symlink-out"),
			Entry("missing output directory", "missing-out"),
			Entry("not rootless", "not-rootless"),
			Entry("bad digest", "bad-digest"),
			Entry("returned output failure", "return-failure"),
		)

		DescribeTable("cancels and cleans up the remote container and credentials",
			func(mode string) { testBuildCancellation(GinkgoT(), mode) },
			Entry("during attached execution", "wait"),
			Entry("during container creation", "create-wait"),
		)

		It("exchanges an OTP using the controller CA", func() {
			t := GinkgoT()
			f := newBuildFixture(t)
			var gotBody, gotMethod string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				gotBody, gotMethod = string(body), r.Method
				fmt.Fprint(w, "key from OTP")
			}))
			DeferCleanup(server.Close)
			write(t, filepath.Join(f.options.SSHDir, "otp"), "opaque\x00token\n")
			write(t, filepath.Join(f.options.SSHDir, "otp-server"), server.URL)
			write(t, filepath.Join(f.options.SSHDir, "otp-ca"), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})))
			Expect(os.Remove(filepath.Join(f.options.SSHDir, "id_rsa"))).To(Succeed())

			Expect(Build(context.Background(), f.options)).To(Succeed())
			Expect(gotMethod).To(Equal(http.MethodPost))
			Expect(gotBody).To(Equal("opaque\x00token\n"))
			assertFile(t, filepath.Join(f.root, "key-copy"), "key from OTP\n")
			f.assertCleaned(t, true)
		})

		DescribeTable("rejects failed OTP exchanges before starting SSH",
			func(mode string) {
				t := GinkgoT()
				f := newBuildFixture(t)
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if mode == "redirect" {
						http.Redirect(w, r, "http://example.invalid/key", http.StatusTemporaryRedirect)
						return
					}

					http.Error(w, "secret diagnostic", http.StatusForbidden)
				}))
				DeferCleanup(server.Close)
				write(t, filepath.Join(f.options.SSHDir, "otp"), "secret OTP")
				serverURL := server.URL
				if mode == "plaintext" {
					serverURL = "http://example.invalid/key"
				}

				write(t, filepath.Join(f.options.SSHDir, "otp-server"), serverURL)
				ca := server.Certificate().Raw
				if mode == "untrusted-ca" {
					public, private, err := ed25519.GenerateKey(rand.Reader)
					Expect(err).NotTo(HaveOccurred())

					template := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true,
						KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
					ca, err = x509.CreateCertificate(rand.Reader, template, template, public, private)
					Expect(err).NotTo(HaveOccurred())
				}

				write(t, filepath.Join(f.options.SSHDir, "otp-ca"), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca})))
				err := Build(context.Background(), f.options)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).NotTo(ContainSubstring("secret OTP"))
				Expect(err.Error()).NotTo(ContainSubstring("secret diagnostic"))
				assertMissing(t, filepath.Join(f.root, "ssh-config"))
				f.assertCleaned(t, false)
			},
			Entry("untrusted CA", "untrusted-ca"),
			Entry("HTTP error", "http-error"),
			Entry("plaintext endpoint", "plaintext"),
			Entry("redirect", "redirect"),
		)
	})
})

// testBuildCancellation cancels a fixture after its blocking operation starts and checks bounded cleanup.
func testBuildCancellation(t GinkgoTInterface, mode string) {
	GinkgoHelper()
	f := newBuildFixture(t)
	t.Setenv("FIXTURE_MODE", mode)
	ctx, cancel := context.WithCancel(context.Background())
	DeferCleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- Build(ctx, f.options) }()
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	DeferCleanup(tick.Stop)
	for {
		if _, err := os.Stat(filepath.Join(f.root, "started")); err == nil {
			break
		}

		select {
		case err := <-done:
			Fail(fmt.Sprintf("build exited before cancellation: %v", err))
			return
		case <-deadline:
			Fail("build did not start")
		case <-tick.C:
		}
	}

	cancel()
	select {
	case err := <-done:
		Expect(err).To(HaveOccurred(), "cancelled build succeeded")
		Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "cancelled build lost its context error: %v", err)
	case <-time.After(10 * time.Second):
		Fail("cancelled build did not finish promptly")
	}

	f.assertCleaned(t, true)
}

type buildFixture struct {
	root    string
	options BuildOptions
}

// newBuildFixture prepares source, indexed inputs, credentials, and local stand-ins for SSH and Podman.
func newBuildFixture(t GinkgoTInterface) buildFixture {
	GinkgoHelper()
	root := t.TempDir()
	f := buildFixture{root: root, options: BuildOptions{
		SSHDir: filepath.Join(root, "ssh"), SourceRoot: filepath.Join(root, "source with space"),
		InputRoot: filepath.Join(root, "inputs"), OutputRoot: filepath.Join(root, "build-output"),
		ControlRoot:  filepath.Join(root, "control"),
		BuilderImage: requestedImage, BuildCmd: []string{"./build.sh"}, Stdout: io.Discard, Stderr: io.Discard,
	}}
	for _, name := range []string{"ssh", "source with space", "inputs", "build-output", "remote", "private", "bin"} {
		Expect(os.MkdirAll(filepath.Join(root, name), 0o755)).To(Succeed())
	}

	write(t, filepath.Join(f.options.SSHDir, "host"), "builder@fixture.invalid\n")
	write(t, filepath.Join(f.options.SSHDir, "user-dir"), filepath.Join(root, "remote"))
	write(t, filepath.Join(f.options.SSHDir, "id_rsa"), "fixture key")
	write(t, filepath.Join(f.options.SSHDir, "known_hosts"), "fixture.invalid ssh-ed25519 fixture\n")
	write(t, filepath.Join(f.options.SourceRoot, "private-source.txt"), "must not change")
	write(t, filepath.Join(f.options.SourceRoot, "relative-links", "output.txt"), "expected output")
	Expect(os.MkdirAll(filepath.Join(f.options.SourceRoot, "relative-links", "sub"), 0o755)).To(Succeed())

	for name, target := range map[string]string{"same-link": "./output.txt", "sub/parent-link": "../output.txt"} {
		Expect(os.Symlink(target, filepath.Join(f.options.SourceRoot, "relative-links", name))).To(Succeed())
	}

	write(t, filepath.Join(f.options.InputRoot, "0", "out", "generated.txt"), "first build output")
	write(t, filepath.Join(f.options.InputRoot, "1", "out", "generated.txt"), "second build output")
	write(t, filepath.Join(root, "bin", "ssh"), sshFixture)
	write(t, filepath.Join(root, "bin", "podman"), podmanFixture)
	for _, name := range []string{"ssh", "podman"} {
		Expect(os.Chmod(filepath.Join(root, "bin", name), 0o755)).To(Succeed())
	}

	t.Setenv("PATH", filepath.Join(root, "bin")+":"+os.Getenv("PATH"))
	t.Setenv("TMPDIR", filepath.Join(root, "private"))
	t.Setenv("FIXTURE_ROOT", root)
	t.Setenv("FIXTURE_MODE", "success")
	return f
}

// containerCommand reads the command emitted at the Podman boundary, requiring a literal single-executable entrypoint.
func (f buildFixture) containerCommand(t GinkgoTInterface) []string {
	GinkgoHelper()
	args := strings.Split(strings.TrimSuffix(string(read(t, filepath.Join(f.root, "create-args"))), "\x00"), "\x00")
	var entrypoint []string
	for index, argument := range args {
		if argument == "--entrypoint" && index+1 < len(args) {
			Expect(json.Unmarshal([]byte(args[index+1]), &entrypoint)).To(Succeed(), "entrypoint JSON: %q", args[index+1])
			Expect(entrypoint).To(HaveLen(1), "entrypoint must contain only the executable")
		}

		if argument == requestedImage {
			Expect(entrypoint).NotTo(BeEmpty(), "container did not receive an explicit executable entrypoint")

			return append(entrypoint, args[index+1:]...)
		}
	}

	Fail("container create omitted the requested image")
	return nil
}

// assertCleaned checks that remote workspaces, private keys, temporary outputs, and any container were removed.
func (f buildFixture) assertCleaned(t GinkgoTInterface, container bool) {
	GinkgoHelper()
	for _, directory := range []string{"remote", "private"} {
		entries, err := os.ReadDir(filepath.Join(f.root, directory))
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty(), "%s not cleaned", directory)
	}

	if container {
		_, err := os.Stat(filepath.Join(f.root, "removed-container"))
		Expect(err).NotTo(HaveOccurred(), "container was not removed")
	}

	entries, err := os.ReadDir(f.options.ControlRoot)
	if os.IsNotExist(err) {
		entries = nil
	} else {
		Expect(err).NotTo(HaveOccurred())
	}
	controlEntries := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() != "builder-image" {
			controlEntries = append(controlEntries, entry.Name())
		}
	}
	Expect(controlEntries).To(BeEmpty(), "temporary control entries remain")

	entries, err = os.ReadDir(f.options.SourceRoot)
	Expect(err).NotTo(HaveOccurred())
	var temporaryOutputs []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".byob-returned-") {
			temporaryOutputs = append(temporaryOutputs, entry.Name())
		}
	}
	Expect(temporaryOutputs).To(BeEmpty(), "temporary output entries remain")
}

// write creates a private fixture file together with its parent directories.
func write(t GinkgoTInterface, path, content string) {
	t.Helper()
	Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
}

// read loads a fixture file and fails the current test if it is unavailable.
func read(t GinkgoTInterface, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())

	return data
}

// assertFile checks the exact contents of a fixture output file.
func assertFile(t GinkgoTInterface, path, want string) {
	GinkgoHelper()
	Expect(string(read(t, path))).To(Equal(want), "contents of %s", path)
}

// findArchiveHeader returns one member from the worker's copied tar archives.
func findArchiveHeader(t GinkgoTInterface, root, name string) *tar.Header {
	GinkgoHelper()
	archives, err := filepath.Glob(filepath.Join(root, "copied-archive.*"))
	Expect(err).NotTo(HaveOccurred())
	for _, archive := range archives {
		reader := tar.NewReader(bytes.NewReader(read(t, archive)))
		for {
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}

			Expect(err).NotTo(HaveOccurred())
			if header.Name == name {
				return header
			}
		}
	}

	Fail("missing archive member " + name)
	return nil
}

// assertMissing checks that neither a regular path nor a dangling symlink exists.
func assertMissing(t GinkgoTInterface, path string) {
	GinkgoHelper()
	_, err := os.Lstat(path)
	Expect(err).To(HaveOccurred())
	Expect(os.IsNotExist(err)).To(BeTrue(), "%s should not exist: %v", path, err)
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
       archive=$(mktemp "$FIXTURE_ROOT/copied-archive.XXXXXXXX")
       cat > "$archive"
       tar -xf "$archive" -C "$FIXTURE_ROOT/received"
       if [[ $FIXTURE_MODE == home-copy-failure && -d $FIXTURE_ROOT/received/var/workdir/home ]]; then exit 43; fi
   else
       cp -a "$FIXTURE_ROOT/received${2#fixture-container:}" "$3"
   fi ;;
 start)
   cat >/dev/null
   if [[ -d $FIXTURE_ROOT/received/var/workdir/home ]]; then touch "$FIXTURE_ROOT/home-before-start"; fi
   if [[ $FIXTURE_MODE == start-failure ]]; then exit 42; fi
   if [[ $FIXTURE_MODE == wait ]]; then touch "$FIXTURE_ROOT/started"; sleep 60; fi
   source=$FIXTURE_ROOT/received/var/source
   output=$FIXTURE_ROOT/received/var/byob/output
   printf mutated > "$source/private-source.txt"
   printf private > "$source/remote-only.txt"
   rm -rf "$output"
   if [[ $FIXTURE_MODE == symlink-out ]]; then ln -s . "$output"; exit 0; fi
   if [[ $FIXTURE_MODE == missing-out ]]; then exit 0; fi
   mkdir -p "$output"
   cp -a "$source/relative-links/." "$output/"
   cp "$FIXTURE_ROOT/received/var/workdir/inputs/0/out/generated.txt" "$output/first.txt"
   cp "$FIXTURE_ROOT/received/var/workdir/inputs/1/out/generated.txt" "$output/second.txt"
   printf 'developer output\n'
   printf 'developer error stream\n' >&2 ;;
 inspect) if [[ $FIXTURE_MODE == container-failure ]]; then printf 7; else printf 0; fi ;;
 rm) touch "$FIXTURE_ROOT/removed-container" ;;
 *) exit 99 ;;
esac
`
