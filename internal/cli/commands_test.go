package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CLI commands", func() {
	Describe("prepare", func() {
		It("restores artifact inputs before preparing source and home", func() {
			fixtureCommand("use-archive", `destination=${1#*=}
mkdir -p "$destination/out"
printf restored > "$destination/out/input.txt"
`)

			source := GinkgoT().TempDir()
			inputs := filepath.Join(GinkgoT().TempDir(), "inputs")
			home := filepath.Join(GinkgoT().TempDir(), "home")
			reference := "oci:registry.example/build@sha256:" + strings.Repeat("a", 64)

			Expect(runCLI(
				"prepare",
				"--inputs="+inputs,
				"--source="+source,
				"--home="+home,
				"--",
				reference,
			)).To(Succeed())

			data, err := os.ReadFile(filepath.Join(inputs, "0", "out", "input.txt"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(Equal("restored"))

			info, err := os.Stat(home)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.IsDir()).To(BeTrue())
		})

		It("removes git credentials from the checkout", func() {
			source := GinkgoT().TempDir()
			home := filepath.Join(GinkgoT().TempDir(), "home")
			secret := "secret-token"
			Expect(os.MkdirAll(filepath.Join(source, ".git"), 0o755)).To(Succeed())
			config := "[remote \"origin\"]\n" +
				"\turl = https://user:" + secret + "@github.com/org/repo.git\n" +
				"\tfetch = +refs/heads/*:refs/remotes/origin/*\n" +
				"[http]\n" +
				"\textraheader = AUTHORIZATION: basic " + secret + "\n" +
				"[credential]\n" +
				"\thelper = store --file=/tmp/.git-credentials\n" +
				"[url \"https://user:" + secret + "@github.com/\"]\n" +
				"\tinsteadOf = https://github.com/\n" +
				"[remote \"mirror\"]\n" +
				"\turl = ssh://git@github.com/org/repo.git\n"
			Expect(os.WriteFile(filepath.Join(source, ".git", "config"), []byte(config), 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(source, ".git-credentials"), []byte("https://user:"+secret+"@github.com\n"), 0o600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(source, ".netrc"), []byte("password "+secret+"\n"), 0o600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(source, "README"), []byte("keep\n"), 0o644)).To(Succeed())

			Expect(runCLI("prepare", "--source="+source, "--home="+home)).To(Succeed())

			rewritten, err := os.ReadFile(filepath.Join(source, ".git", "config"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(rewritten)).NotTo(ContainSubstring(secret))
			Expect(string(rewritten)).To(ContainSubstring("https://github.com/org/repo.git"))
			Expect(string(rewritten)).To(ContainSubstring("fetch = +refs/heads/*:refs/remotes/origin/*"))
			Expect(string(rewritten)).To(ContainSubstring("ssh://git@github.com/org/repo.git"))
			Expect(string(rewritten)).NotTo(ContainSubstring("extraheader"))
			Expect(string(rewritten)).NotTo(ContainSubstring("helper"))
			_, err = os.Stat(filepath.Join(source, ".git-credentials"))
			Expect(os.IsNotExist(err)).To(BeTrue())
			_, err = os.Stat(filepath.Join(source, ".netrc"))
			Expect(os.IsNotExist(err)).To(BeTrue())
			readme, err := os.ReadFile(filepath.Join(source, "README"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(readme)).To(Equal("keep\n"))
		})

		It("creates the build home without changing the checkout", func() {
			source := GinkgoT().TempDir()
			home := filepath.Join(GinkgoT().TempDir(), "home")
			Expect(runCLI("prepare", "--source="+source, "--home="+home)).To(Succeed())

			info, err := os.Stat(home)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.IsDir()).To(BeTrue())

			entries, err := os.ReadDir(source)
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(BeEmpty())
		})

		It("rejects a missing source before creating the build home", func() {
			home := filepath.Join(GinkgoT().TempDir(), "home")
			missingSource := filepath.Join(GinkgoT().TempDir(), "missing")

			Expect(runCLI("prepare", "--source="+missingSource, "--home="+home)).To(HaveOccurred())

			_, err := os.Stat(home)
			Expect(os.IsNotExist(err)).To(BeTrue(), "build home should not exist: %v", err)
		})
	})

	Describe("help", func() {
		It("documents the root and command-specific arguments", func() {
			scenarios := []struct {
				args []string
				want string
			}{
				{args: []string{"--help"}, want: "build-remote"},
				{args: []string{"prepare", "--help"}, want: "--home"},
				{args: []string{"check-command", "--help"}, want: "--command-file"},
				{args: []string{"build-remote", "--help"}, want: "--command-file"},
			}

			for _, scenario := range scenarios {
				scenario := scenario
				var stdout, stderr bytes.Buffer
				Expect(Run(context.Background(), scenario.args, &stdout, &stderr)).To(Succeed(), "arguments %v", scenario.args)
				Expect(stdout.String()).To(ContainSubstring(scenario.want), "arguments %v", scenario.args)
				Expect(stderr.Len()).To(BeZero(), "arguments %v produced stderr: %s", scenario.args, stderr.String())
			}
		})
	})

	Describe("check-command", func() {
		It("rejects a missing or empty executable", func() {
			file := filepath.Join(GinkgoT().TempDir(), "build-cmd")
			for _, args := range [][]string{
				{"check-command", "--command-file=" + file},
				{"check-command", "--command-file=" + file, "--"},
				{"check-command", "--command-file=" + file, "--", ""},
			} {
				Expect(runCLI(args...)).To(HaveOccurred(), "arguments %q", args)
				_, err := os.Stat(file)
				Expect(os.IsNotExist(err)).To(BeTrue(), "arguments %q wrote %s", args, file)
			}
		})

		It("records the build argv", func() {
			file := filepath.Join(GinkgoT().TempDir(), "control", "build-cmd")
			Expect(runCLI("check-command", "--command-file="+file, "--", "./build.sh", "--flag", "two words")).To(Succeed())

			var got []string
			Expect(json.Unmarshal(mustReadFile(file), &got)).To(Succeed())
			Expect(got).To(Equal([]string{"./build.sh", "--flag", "two words"}))
		})
	})

	Describe("build-remote arguments", func() {
		It("does not accept build arguments as the executable", func() {
			err := runCLI("build-remote", "--", "./build.sh")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("build command"))

			err = runCLI("build-remote", "./build.sh")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("build arguments must follow --"))
		})

		It("rejects the removed script flag", func() {
			Expect(runCLI("prepare", "--script=build.sh")).To(HaveOccurred())
		})
	})

	It("reports a missing subcommand for an empty argument slice", func() {
		for _, args := range [][]string{nil, {}} {
			err := runCLI(args...)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("subcommand is required"))
		}
	})
})

func mustReadFile(path string) []byte {
	GinkgoHelper()
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	return data
}
