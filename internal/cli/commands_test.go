package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CLI commands", func() {
	Describe("prepare", func() {
		It("creates the build home without changing the checkout", func() {
			source := GinkgoT().TempDir()
			home := filepath.Join(GinkgoT().TempDir(), "home")
			Expect(runCLI("prepare", "--source="+source, "--home="+home)).To(Succeed())

			info, err := os.Stat(home)
			Expect(err).NotTo(HaveOccurred())
			if info != nil {
				Expect(info.IsDir()).To(BeTrue())
			}

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
				{args: []string{"build-remote", "--help"}, want: "COMMAND"},
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

	Describe("build-remote arguments", func() {
		It("requires an unambiguous executable command", func() {
			for _, suffix := range [][]string{nil, {"--"}, {"--", ""}, {"./build"}} {
				args := append([]string{"build-remote"}, suffix...)
				err := runCLI(args...)
				Expect(err).To(HaveOccurred(), "arguments %q should report a command error", args)
				if err != nil {
					Expect(err.Error()).To(ContainSubstring("command"), "arguments %q", args)
				}
			}
		})

		It("rejects the removed script flag", func() {
			Expect(runCLI("prepare", "--script=build.sh")).To(HaveOccurred())
		})
	})

	It("reports a missing subcommand for an empty argument slice", func() {
		originalArgs := os.Args
		os.Args = []string{"embedding-process", "--help"}
		DeferCleanup(func() {
			os.Args = originalArgs
		})

		for _, args := range [][]string{nil, {}} {
			err := runCLI(args...)
			Expect(err).To(HaveOccurred())
			if err != nil {
				Expect(err.Error()).To(ContainSubstring("subcommand is required"))
			}
		}
	})
})
