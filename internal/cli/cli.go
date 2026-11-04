// Package cli implements the trusted Task command line.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/gbenhaim/byob-pipeline/internal/artifact"
	"github.com/gbenhaim/byob-pipeline/internal/remote"
	"github.com/spf13/cobra"
)

// Run executes one Task operation with a fresh command tree and flag state.
// Developer command arguments are passed as argv, never interpreted as shell code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	root := &cobra.Command{
		Use:           "byob",
		Short:         "Run trusted build preparation, artifact, and remote operations",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("a subcommand is required (use --help)")
		},
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	root.SetArgs(append([]string{}, args...))
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.AddCommand(
		newPrepareCommand(), newStageOutputCommand(),
		newRestoreInputsCommand(), newPackCommand(), newPublishCommand(),
		newSelectPlatformCommand(), newRecordImageCommand(),
		newCheckCommandCommand(), newRemoteBuildCommand(),
	)
	return root.ExecuteContext(ctx)
}

// newPrepareCommand restores the checkout and build inputs, validates source,
// creates HOME, and records the build argv when a command file is requested.
func newPrepareCommand() *cobra.Command {
	var inputs, source, home, sourceArtifact, commandFile string
	command := &cobra.Command{
		Use:   "prepare [flags] -- [ARTIFACT...]",
		Short: "Restore the checkout and build inputs, then prepare the build home",
		Args:  cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			stderr := command.ErrOrStderr()
			stdout := command.OutOrStdout()
			buildCmd, artifacts, err := prepareArguments(commandFile, args)
			if err != nil {
				return err
			}

			if sourceArtifact != "" {
				if err := restoreSource(command.Context(), sourceArtifact, source, stdout, stderr); err != nil {
					return err
				}
			}

			if inputs != "" {
				if err := restoreInputs(
					command.Context(),
					inputs,
					artifacts,
					false,
					stdout,
					stderr,
				); err != nil {
					return err
				}
			} else if len(artifacts) > 0 {
				return fmt.Errorf("--inputs is required when artifact references are supplied")
			}

			report(stderr, "Preparing source %s", source)
			if err := artifact.Prepare(source); err != nil {
				return err
			}

			if err := os.MkdirAll(home, 0755); err != nil {
				return fmt.Errorf("create build home: %w", err)
			}

			report(stderr, "Prepared source %s and created build home %s", source, home)
			if commandFile == "" {
				return nil
			}

			report(stderr, "Recording build command %s", quotedArgs(buildCmd))
			if err := remote.WriteBuildCommand(commandFile, buildCmd); err != nil {
				return err
			}

			report(stderr, "Recorded build command in %s", commandFile)
			return nil
		},
	}

	command.Flags().StringVar(&inputs, "inputs", "", "artifact input restoration directory")
	command.Flags().StringVar(&source, "source", "/var/source", "restored source directory")
	command.Flags().StringVar(&home, "home", "/var/workdir/home", "build home directory to create")
	command.Flags().StringVar(&sourceArtifact, "source-artifact", "", "checkout artifact to restore into --source")
	command.Flags().StringVar(&commandFile, "command-file", "", "recorded build argv")
	return command
}

// newStageOutputCommand copies validated build outputs to the trusted export tree.
func newStageOutputCommand() *cobra.Command {
	var buildOutput, output, platform string
	command := &cobra.Command{
		Use:   "stage-output",
		Short: "Stage validated build outputs for export",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			stderr := command.ErrOrStderr()
			report(stderr, "Staging build output from %s into %s", buildOutput, output)
			if err := artifact.StageOutput(buildOutput, output, platform); err != nil {
				return err
			}

			if platform != "" {
				report(stderr, "Staged build output for %s into %s", platform, output)
				return nil
			}

			report(stderr, "Staged build output into %s", output)
			return nil
		},
	}

	command.Flags().StringVar(&buildOutput, "build-output", "/var/byob/output", "build output directory")
	command.Flags().StringVar(&output, "output", "/var/build-output", "private output staging directory")
	command.Flags().StringVar(&platform, "platform", "", "trusted platform metadata")
	return command
}

// newRestoreInputsCommand restores each supplied artifact to an indexed directory.
func newRestoreInputsCommand() *cobra.Command {
	var root string
	var required bool
	command := &cobra.Command{
		Use:   "restore-inputs [flags] -- [ARTIFACT...]",
		Short: "Restore artifact inputs into separate directories",
		RunE: func(command *cobra.Command, args []string) error {
			return restoreInputs(command.Context(), root, args, required, command.OutOrStdout(), command.ErrOrStderr())
		},
	}

	command.Flags().StringVar(&root, "root", "/var/workdir/inputs", "input restoration directory")
	command.Flags().BoolVar(&required, "require", false, "require at least one artifact")
	return command
}

// newPackCommand assembles indexed outputs into a deterministic delivery archive.
func newPackCommand() *cobra.Command {
	var inputs, output, archive, layout string
	command := &cobra.Command{
		Use:   "pack",
		Short: "Package validated artifact outputs",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return packArchive(inputs, output, archive, layout, command.ErrOrStderr())
		},
	}

	command.Flags().StringVar(&inputs, "inputs", "/var/workdir/inputs", "indexed artifact directories")
	command.Flags().StringVar(&output, "package", "/var/workdir/package", "staged deliverables directory")
	command.Flags().StringVar(&archive, "archive", "/var/workdir/payload.tar.gz", "archive filename")
	command.Flags().StringVar(&layout, "layout", "flat", "flat, platform, or named")
	return command
}

// newPublishCommand publishes the payload before writing results.
func newPublishCommand() *cobra.Command {
	var opts publishOptions
	command := &cobra.Command{
		Use:   "publish [flags] -- [ARTIFACT...]",
		Short: "Restore, pack, and publish the payload",
		Args:  cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			opts.Artifacts = args
			return publish(command.Context(), opts, command.OutOrStdout(), command.ErrOrStderr())
		},
	}

	command.Flags().StringVar(&opts.Image, "image", "", "tagged output image")
	command.Flags().StringVar(
		&opts.ArtifactType,
		"artifact-type",
		"application/vnd.konflux.build-output.v1",
		"OCI artifact type",
	)
	command.Flags().StringVar(&opts.Workdir, "workdir", "/var/workdir", "payload directory")
	command.Flags().StringVar(
		&opts.AuthDir,
		"auth-dir",
		"/var/run/byob-registry-auth",
		"registry authentication directory",
	)
	command.Flags().StringVar(&opts.URLResult, "url-result", "", "IMAGE_URL result file")
	command.Flags().StringVar(&opts.DigestResult, "digest-result", "", "IMAGE_DIGEST result file")
	command.Flags().BoolVar(&opts.PlainHTTP, "plain-http", false, "use HTTP for a disposable test registry")
	command.Flags().StringVar(&opts.Inputs, "inputs", "", "indexed artifact directories to restore and pack")
	command.Flags().StringVar(&opts.Package, "package", "", "staged deliverables directory")
	command.Flags().StringVar(&opts.Archive, "archive", "", "archive filename")
	return command
}

// packArchive writes one delivery archive and logs its size.
func packArchive(inputs, output, archive, layout string, stderr io.Writer) error {
	report(stderr, "Packing %s layout from %s into %s", layout, inputs, archive)
	if err := artifact.Pack(inputs, output, archive, layout); err != nil {
		return err
	}

	info, err := os.Stat(archive)
	if err != nil {
		return err
	}

	report(stderr, "Packed %s (%d bytes) using %s layout", archive, info.Size(), layout)
	return nil
}

// prepareArguments splits a prepare invocation into the trusted build argv and
// artifact references. Without a command file, every positional argument is an
// artifact. With a command file, arguments before --artifacts are the build
// argv and arguments after it are artifacts.
func prepareArguments(commandFile string, args []string) ([]string, []string, error) {
	if commandFile == "" {
		return nil, args, nil
	}

	command := args
	var artifacts []string
	if index := slices.Index(args, "--artifacts"); index >= 0 {
		command = args[:index]
		artifacts = args[index+1:]
	}

	if len(command) == 0 || command[0] == "" {
		return nil, nil, fmt.Errorf("a build command is required after --")
	}

	return command, artifacts, nil
}

// newSelectPlatformCommand records whether the controller selected local or
// remote execution for the requested platform.
func newSelectPlatformCommand() *cobra.Command {
	var sshDir, result string
	command := &cobra.Command{
		Use:   "select-platform",
		Short: "Select local or remote execution from controller metadata",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return remote.SelectPlatform(sshDir, result)
		},
	}

	command.Flags().StringVar(&sshDir, "ssh-dir", "/ssh", "controller Secret directory")
	command.Flags().StringVar(&result, "result", "", "MODE result file")
	return command
}

// newRecordImageCommand writes trusted image metadata for the selected execution.
func newRecordImageCommand() *cobra.Command {
	var mode, image, control, result string
	command := &cobra.Command{
		Use:   "record-image",
		Short: "Record the builder image used by this build",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return remote.RecordImage(mode, image, control, result)
		},
	}

	command.Flags().StringVar(&mode, "mode", "", "local or remote execution")
	command.Flags().StringVar(&image, "image", "", "requested builder image")
	command.Flags().StringVar(&control, "control", "/var/workdir/control", "trusted control directory")
	command.Flags().StringVar(&result, "result", "", "BUILD_IMAGE_ARTIFACT_INPUTS result file")
	return command
}

// newCheckCommandCommand records the trusted build argv before either developer path.
func newCheckCommandCommand() *cobra.Command {
	var commandFile string
	command := &cobra.Command{
		Use:   "check-command [flags] -- COMMAND [ARG...]",
		Short: "Record the trusted build argv",
		Args:  requireBuildCommand,
		RunE: func(command *cobra.Command, args []string) error {
			stderr := command.ErrOrStderr()
			report(stderr, "Recording build command %s", quotedArgs(args))
			if err := remote.WriteBuildCommand(commandFile, args); err != nil {
				return err
			}

			report(stderr, "Recorded build command in %s", commandFile)
			return nil
		},
	}

	command.Flags().StringVar(&commandFile, "command-file", "/var/workdir/control/build-cmd", "recorded build argv")
	return command
}

// newRemoteBuildCommand runs the recorded build argv on the VM and appends only
// the arguments that follow --. Those arguments cannot replace the executable.
func newRemoteBuildCommand() *cobra.Command {
	var opts remote.BuildOptions
	var commandFile string
	command := &cobra.Command{
		Use:   "build-remote [flags] [-- BUILD_ARG...]",
		Short: "Run a recorded build command on the controller's remote VM",
		Args:  requireBuildArguments,
		RunE: func(command *cobra.Command, args []string) error {
			var err error
			opts.BuildCmd, err = remote.LoadBuildCommand(commandFile, args)
			if err != nil {
				return err
			}

			opts.Stdout, opts.Stderr = command.OutOrStdout(), command.ErrOrStderr()
			return remote.Build(command.Context(), opts)
		},
	}

	command.Flags().StringVar(&opts.SSHDir, "ssh-dir", "/ssh", "controller Secret directory")
	command.Flags().StringVar(&opts.SourceRoot, "source", "/var/source", "source directory")
	command.Flags().StringVar(&opts.InputRoot, "inputs", "/var/workdir/inputs", "indexed inputs directory")
	command.Flags().StringVar(&opts.OutputRoot, "output", "/var/byob/output", "build output directory")
	command.Flags().StringVar(&opts.ControlRoot, "control", "/var/workdir/control", "trusted control directory")
	command.Flags().StringVar(&opts.BuilderImage, "image", "", "digest-pinned developer image")
	command.Flags().StringVar(&commandFile, "command-file", "/var/workdir/control/build-cmd", "recorded build argv")
	return command
}

// report writes one progress line to the Task step log.
func report(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, format+"\n", args...)
}

// quotedArgs renders argv so a log line keeps arguments distinct.
func quotedArgs(args []string) string {
	quoted := make([]string, len(args))
	for index, arg := range args {
		quoted[index] = fmt.Sprintf("%q", arg)
	}

	return strings.Join(quoted, " ")
}

// requireBuildArguments keeps additional arguments behind the option terminator.
func requireBuildArguments(command *cobra.Command, args []string) error {
	if len(args) > 0 && command.ArgsLenAtDash() != 0 {
		return fmt.Errorf("build arguments must follow --")
	}

	return nil
}

// requireBuildCommand requires an explicit argv boundary so developer flags
// cannot be mistaken for options controlling the trusted CLI.
func requireBuildCommand(command *cobra.Command, args []string) error {
	if len(args) == 0 || command.ArgsLenAtDash() != 0 {
		return fmt.Errorf("a build command is required after --")
	}

	if args[0] == "" {
		return fmt.Errorf("build command executable must not be empty")
	}

	return nil
}
