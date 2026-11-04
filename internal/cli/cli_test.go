package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// TestCLI runs the CLI package's Ginkgo specs.
func TestCLI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CLI Suite")
}

// fixtureCommand installs a disposable helper executable at the front of PATH.
func fixtureCommand(name, script string) {
	t := GinkgoT()
	t.Helper()

	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nset -eu\n"+script), 0755)
	Expect(err).NotTo(HaveOccurred())

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// childSSLCertFile runs restore-inputs and returns the SSL_CERT_FILE seen by use-archive.
func childSSLCertFile(caFile, previous, ref string) string {
	GinkgoT().Setenv("CA_FILE", caFile)
	GinkgoT().Setenv("SSL_CERT_FILE", previous)
	seen := filepath.Join(GinkgoT().TempDir(), "seen")
	fixtureCommand("use-archive", fmt.Sprintf("printf '%%s' \"${SSL_CERT_FILE-}\" > %q\n", seen))
	Expect(runCLI("restore-inputs", "--root="+GinkgoT().TempDir(), "--", ref)).To(Succeed())

	data, err := os.ReadFile(seen)
	Expect(err).NotTo(HaveOccurred())
	return string(data)
}

// publishedSSLCertFile runs publish and returns the SSL_CERT_FILE seen by oras.
func publishedSSLCertFile(caFile, previous, digest string) string {
	GinkgoT().Setenv("CA_FILE", caFile)
	GinkgoT().Setenv("SSL_CERT_FILE", previous)
	root := GinkgoT().TempDir()
	seen := filepath.Join(root, "seen")
	script := fmt.Sprintf("printf '%%s' \"${SSL_CERT_FILE-}\" > %q\nprintf '%%s' '{\"digest\":\"%s\"}'\n", seen, digest)
	fixtureCommand("oras", script)
	Expect(os.WriteFile(filepath.Join(root, "payload.tar.gz"), []byte("payload"), 0644)).To(Succeed())
	Expect(runCLI(
		"publish",
		"--workdir="+root,
		"--image=localhost:5000/team/output:test",
		"--url-result="+filepath.Join(root, "url"),
		"--digest-result="+filepath.Join(root, "digest"),
		"--auth-dir="+filepath.Join(root, "no-auth"),
	)).To(Succeed())

	data, err := os.ReadFile(seen)
	Expect(err).NotTo(HaveOccurred())
	return string(data)
}

// runCLI invokes the public CLI and writes captured output when it fails.
func runCLI(args ...string) error {
	_, _, err := runCLIStreams(args...)
	return err
}

// runCLIStreams invokes the public CLI and returns its stdout and stderr.
func runCLIStreams(args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), args, &stdout, &stderr)
	if err != nil {
		GinkgoWriter.Printf("CLI output:\n%s%s", stdout.String(), stderr.String())
	}

	return stdout.String(), stderr.String(), err
}

var _ = Describe("CLI", func() {
	Describe("restore-inputs", func() {
		ref := "oci:registry.example/build@sha256:" + strings.Repeat("a", 64)

		It("accepts empty optional inputs and rejects them when required", func() {
			root := filepath.Join(GinkgoT().TempDir(), "inputs")
			stdout, stderr, err := runCLIStreams("restore-inputs", "--root="+root, "--")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring("No build artifacts to restore into " + root))

			entries, err := os.ReadDir(root)
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(BeEmpty())

			Expect(runCLI("restore-inputs", "--require", "--root="+root, "--")).To(HaveOccurred())
		})

		It("restores inputs into distinct indexed directories", func() {
			fixtureCommand("use-archive", `destination=${1#*=}
mkdir -p "$destination/out"
printf '%s' "${1%%=*}" > "$destination/out/shared.txt"
`)
			root := filepath.Join(GinkgoT().TempDir(), "inputs")
			second := "oci:registry.example/other@sha256:" + strings.Repeat("b", 64)

			_, stderr, err := runCLIStreams("restore-inputs", "--root="+root, "--", ref, second)
			Expect(err).NotTo(HaveOccurred())
			Expect(stderr).To(ContainSubstring("Restoring 2 build artifact(s) into " + root))
			Expect(stderr).To(ContainSubstring("Restored build artifact 0 into " + filepath.Join(root, "0")))
			Expect(stderr).To(ContainSubstring("Restored build artifact 1 into " + filepath.Join(root, "1")))

			for index, want := range map[string]string{"0": ref, "1": second} {
				data, err := os.ReadFile(filepath.Join(root, index, "out/shared.txt"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(data)).To(Equal(want))
			}
		})

		It("restores named inputs into task directories", func() {
			fixtureCommand("use-archive", `destination=${1#*=}
mkdir -p "$destination/out"
printf '%s' "${1%%=*}" > "$destination/out/shared.txt"
`)
			root := filepath.Join(GinkgoT().TempDir(), "inputs")
			second := "oci:registry.example/other@sha256:" + strings.Repeat("b", 64)

			_, stderr, err := runCLIStreams(
				"restore-inputs", "--root="+root, "--",
				"codegen="+ref, "package="+second,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(stderr).To(ContainSubstring("Restored build artifact codegen into " + filepath.Join(root, "codegen")))
			Expect(stderr).To(ContainSubstring("Restored build artifact package into " + filepath.Join(root, "package")))

			for name, want := range map[string]string{"codegen": ref, "package": second} {
				data, err := os.ReadFile(filepath.Join(root, name, "out/shared.txt"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(data)).To(Equal(want))
			}
		})

		It("rejects duplicate, mixed, and unsafe artifact names", func() {
			root := GinkgoT().TempDir()
			second := "oci:registry.example/other@sha256:" + strings.Repeat("b", 64)
			rejected := []struct {
				args []string
				want string
			}{
				{[]string{"codegen=" + ref, "codegen=" + second}, "duplicate build artifact name"},
				{[]string{"codegen=" + ref, second}, "names must be set on every input"},
				{[]string{"../codegen=" + ref}, "invalid build artifact reference"},
				{[]string{"Codegen=" + ref}, "invalid build artifact reference"},
				{[]string{"=" + ref}, "invalid build artifact reference"},
			}
			for _, item := range rejected {
				_, stderr, err := runCLIStreams(append([]string{"restore-inputs", "--root=" + root, "--"}, item.args...)...)
				Expect(err).To(HaveOccurred())
				Expect(stderr + err.Error()).To(ContainSubstring(item.want))
			}
		})

		It("rejects invalid references", func() {
			invalidReferences := []string{
				"",
				"invalid",
				"oci:repo@sha256:short",
				"oci:repo=else@sha256:" + strings.Repeat("a", 64),
			}
			for _, value := range invalidReferences {
				Expect(runCLI("restore-inputs", "--root="+GinkgoT().TempDir(), "--", value)).To(HaveOccurred(), "reference %q should be rejected", value)
			}
		})

		It("gives use-archive a non-empty registry CA bundle", func() {
			ca := filepath.Join(GinkgoT().TempDir(), "ca.crt")
			Expect(os.WriteFile(ca, []byte("custom-ca"), 0644)).To(Succeed())
			Expect(childSSLCertFile(ca, "/system/certs.pem", ref)).To(Equal(ca))
		})

		It("leaves the system trust store alone when the CA bundle is missing or empty", func() {
			missing := filepath.Join(GinkgoT().TempDir(), "missing.crt")
			Expect(childSSLCertFile(missing, "/system/certs.pem", ref)).To(Equal("/system/certs.pem"))

			empty := filepath.Join(GinkgoT().TempDir(), "empty.crt")
			Expect(os.WriteFile(empty, nil, 0644)).To(Succeed())
			Expect(childSSLCertFile(empty, "/system/certs.pem", ref)).To(Equal("/system/certs.pem"))
		})

		It("stops restoring after an artifact restore fails", func() {
			fixtureCommand("use-archive", "exit 17\n")
			root := GinkgoT().TempDir()

			err := runCLI("restore-inputs", "--root="+root, "--", ref, ref)
			var exit *exec.ExitError
			Expect(errors.As(err, &exit)).To(BeTrue(), "expected an exit error, got %v", err)
			if exit != nil {
				Expect(exit.ExitCode()).To(Equal(17))
			}

			_, err = os.Stat(filepath.Join(root, "1"))
			Expect(os.IsNotExist(err)).To(BeTrue(), "the second restore should not run: %v", err)
		})
	})

	Describe("publish", func() {
		digest := "sha256:" + strings.Repeat("a", 64)

		for _, scenario := range []struct {
			name       string
			script     string
			shouldPass bool
		}{
			{
				name:       "success",
				script:     "printf '%s' '{\"digest\":\"" + digest + "\"}'\n",
				shouldPass: true,
			},
			{
				name:   "push failure",
				script: "exit 9\n",
			},
			{
				name:   "invalid digest",
				script: "printf '%s' '{\"digest\":\"invalid\"}'\n",
			},
		} {
			scenario := scenario
			It("handles "+scenario.name, func() {
				root := GinkgoT().TempDir()
				fixtureCommand("oras", scenario.script)
				Expect(os.WriteFile(filepath.Join(root, "payload.tar.gz"), []byte("payload"), 0644)).To(Succeed())

				stdout, stderr, err := runCLIStreams("publish", "--workdir="+root, "--image=localhost:5000/team/output:test", "--url-result="+root+"/url", "--digest-result="+root+"/digest", "--auth-dir="+root+"/no-auth")
				if !scenario.shouldPass {
					Expect(err).To(HaveOccurred())
					Expect(stderr).To(ContainSubstring("Publishing " + filepath.Join(root, "payload.tar.gz")))
					Expect(stderr).NotTo(ContainSubstring("Published "))
					for _, name := range []string{"url", "digest"} {
						_, statErr := os.Stat(filepath.Join(root, name))
						Expect(os.IsNotExist(statErr)).To(BeTrue(), "result %s was written before publication succeeded: %v", name, statErr)
					}
					return
				}

				Expect(err).NotTo(HaveOccurred())
				Expect(stdout).To(BeEmpty())
				Expect(stderr).To(ContainSubstring("Publishing " + filepath.Join(root, "payload.tar.gz") + " to localhost:5000/team/output:test as application/vnd.konflux.build-output.v1"))
				Expect(stderr).To(ContainSubstring("Published localhost:5000/team/output:test with digest " + digest))
				for name, want := range map[string]string{
					"url":    "localhost:5000/team/output:test",
					"digest": digest,
				} {
					data, readErr := os.ReadFile(filepath.Join(root, name))
					Expect(readErr).NotTo(HaveOccurred())
					Expect(string(data)).To(Equal(want))
				}
			})
		}

		It("selects the archive layout from artifact references", func() {
			ref := "oci:registry.example/build@sha256:" + strings.Repeat("b", 64)
			second := "oci:registry.example/other@sha256:" + strings.Repeat("c", 64)
			fixtureCommand("use-archive", `destination=${1#*=}
name=${destination##*/}
mkdir -p "$destination/out"
printf '%s' "$name" > "$destination/out/hello.txt"
if [ "$name" = 0 ]; then printf 'linux/amd64\n' > "$destination/.konflux-platform"; fi
if [ "$name" = 1 ]; then printf 'linux/arm64\n' > "$destination/.konflux-platform"; fi
`)
			fixtureCommand("oras", "printf '%s' '{\"digest\":\""+digest+"\"}'\n")

			for _, item := range []struct {
				layout string
				refs   []string
				files  map[string]string
			}{
				{layout: "flat", refs: []string{ref}, files: map[string]string{"hello.txt": "0"}},
				{layout: "platform", refs: []string{ref, second}, files: map[string]string{"linux-amd64/hello.txt": "0", "linux-arm64/hello.txt": "1"}},
				{layout: "named", refs: []string{"first=" + ref, "second=" + second}, files: map[string]string{"first/hello.txt": "first", "second/hello.txt": "second"}},
				{layout: "named", refs: []string{"build=" + ref}, files: map[string]string{"build/hello.txt": "build"}},
			} {
				root := GinkgoT().TempDir()
				archive := filepath.Join(root, "payload.tar.gz")
				_, stderr, err := runCLIStreams(append([]string{
					"publish",
					"--inputs=" + filepath.Join(root, "inputs"),
					"--package=" + filepath.Join(root, "package"),
					"--archive=" + archive,
					"--workdir=" + root,
					"--image=localhost:5000/team/output:test",
					"--url-result=" + filepath.Join(root, "url"),
					"--digest-result=" + filepath.Join(root, "digest"),
					"--auth-dir=" + filepath.Join(root, "no-auth"),
					"--",
				}, item.refs...)...)
				Expect(err).NotTo(HaveOccurred())
				Expect(stderr).To(ContainSubstring("Packing " + item.layout + " layout"))
				for path, want := range item.files {
					Expect(string(mustReadFile(filepath.Join(root, "package", path)))).To(Equal(want))
				}
			}
		})

		It("restores and packs inputs before publishing", func() {
			root := GinkgoT().TempDir()
			ref := "oci:registry.example/build@sha256:" + strings.Repeat("b", 64)
			fixtureCommand("use-archive", `destination=${1#*=}
mkdir -p "$destination/out"
printf hello > "$destination/out/hello.txt"
`)
			fixtureCommand("oras", "printf '%s' '{\"digest\":\""+digest+"\"}'\n")
			archive := filepath.Join(root, "payload.tar.gz")

			stdout, stderr, err := runCLIStreams(
				"publish",
				"--inputs="+filepath.Join(root, "inputs"),
				"--package="+filepath.Join(root, "package"),
				"--archive="+archive,
				"--workdir="+root,
				"--image=localhost:5000/team/output:test",
				"--url-result="+filepath.Join(root, "url"),
				"--digest-result="+filepath.Join(root, "digest"),
				"--auth-dir="+filepath.Join(root, "no-auth"),
				"--",
				ref,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring("Restored build artifact 0 into " + filepath.Join(root, "inputs", "0")))
			Expect(stderr).To(ContainSubstring("Packed " + archive))
			Expect(stderr).To(ContainSubstring("Published localhost:5000/team/output:test with digest " + digest))
			Expect(string(mustReadFile(filepath.Join(root, "digest")))).To(Equal(digest))
		})

		It("does not publish when input restoration fails", func() {
			root := GinkgoT().TempDir()
			ref := "oci:registry.example/build@sha256:" + strings.Repeat("c", 64)
			called := filepath.Join(root, "oras-called")
			fixtureCommand("use-archive", "exit 1\n")
			fixtureCommand("oras", fmt.Sprintf("touch %q\nprintf '%%s' '{\"digest\":\"%s\"}'\n", called, digest))

			_, stderr, err := runCLIStreams(
				"publish",
				"--inputs="+filepath.Join(root, "inputs"),
				"--archive="+filepath.Join(root, "payload.tar.gz"),
				"--workdir="+root,
				"--image=localhost:5000/team/output:test",
				"--url-result="+filepath.Join(root, "url"),
				"--digest-result="+filepath.Join(root, "digest"),
				"--auth-dir="+filepath.Join(root, "no-auth"),
				"--",
				ref,
			)
			Expect(err).To(HaveOccurred())
			Expect(stderr).NotTo(ContainSubstring("Published "))
			_, statErr := os.Stat(called)
			Expect(os.IsNotExist(statErr)).To(BeTrue(), "oras ran before restoration succeeded")
			_, statErr = os.Stat(filepath.Join(root, "digest"))
			Expect(os.IsNotExist(statErr)).To(BeTrue())
		})

		It("gives oras a non-empty registry CA bundle", func() {
			ca := filepath.Join(GinkgoT().TempDir(), "ca.crt")
			Expect(os.WriteFile(ca, []byte("custom-ca"), 0644)).To(Succeed())
			Expect(publishedSSLCertFile(ca, "/system/certs.pem", digest)).To(Equal(ca))
		})

		It("leaves the system trust store alone when publishing without a CA bundle", func() {
			missing := filepath.Join(GinkgoT().TempDir(), "missing.crt")
			Expect(publishedSSLCertFile(missing, "/system/certs.pem", digest)).To(Equal("/system/certs.pem"))

			empty := filepath.Join(GinkgoT().TempDir(), "empty.crt")
			Expect(os.WriteFile(empty, nil, 0644)).To(Succeed())
			Expect(publishedSSLCertFile(empty, "/system/certs.pem", digest)).To(Equal("/system/certs.pem"))
		})
	})

	Describe("command arguments", func() {
		It("rejects invalid invocations and keeps general help available", func() {
			invalidArguments := [][]string{
				nil,
				{"unknown"},
				{"prepare", "--unknown"},
				{"prepare", "unexpected"},
				{"publish"},
			}
			for _, args := range invalidArguments {
				Expect(runCLI(args...)).To(HaveOccurred(), "arguments %v should be rejected", args)
			}

			var output bytes.Buffer
			Expect(Run(context.Background(), []string{"--help"}, &output, &output)).To(Succeed())
			Expect(output.String()).To(ContainSubstring("restore-inputs"))
		})
	})
})
