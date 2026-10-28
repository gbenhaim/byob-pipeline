// Package cli implements the trusted Task command line.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"

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
		newSelectPlatformCommand(), newRecordImageCommand(), newRemoteBuildCommand(),
	)
	return root.ExecuteContext(ctx)
}

// newPrepareCommand validates restored source and creates the writable build home.
func newPrepareCommand() *cobra.Command {
	var source, home string
	command := &cobra.Command{
		Use:   "prepare",
		Short: "Validate restored source and prepare the build home",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := artifact.Prepare(source); err != nil {
				return err
			}

			if err := os.MkdirAll(home, 0755); err != nil {
				return fmt.Errorf("create build home: %w", err)
			}

			return nil
		},
	}

	command.Flags().StringVar(&source, "source", "/var/source", "restored source directory")
	command.Flags().StringVar(&home, "home", "/var/workdir/home", "build home directory to create")
	return command
}

// newStageOutputCommand copies validated build outputs to the trusted export tree.
func newStageOutputCommand() *cobra.Command {
	var source, output, platform string
	command := &cobra.Command{
		Use:   "stage-output",
		Short: "Stage validated build outputs for export",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return artifact.StageOutput(source, output, platform)
		},
	}

	command.Flags().StringVar(&source, "source", "/var/source", "build source directory")
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
		Short: "Restore artifact inputs into separate indexed directories",
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
		RunE: func(*cobra.Command, []string) error {
			return artifact.Pack(inputs, output, archive, layout)
		},
	}

	command.Flags().StringVar(&inputs, "inputs", "/var/workdir/inputs", "indexed artifact directories")
	command.Flags().StringVar(&output, "package", "/var/workdir/package", "staged deliverables directory")
	command.Flags().StringVar(&archive, "archive", "/var/workdir/payload.tar.gz", "archive filename")
	command.Flags().StringVar(&layout, "layout", "flat", "flat or platform")
	return command
}

// newPublishCommand publishes the payload before writing results.
func newPublishCommand() *cobra.Command {
	var opts publishOptions
	command := &cobra.Command{
		Use:   "publish",
		Short: "Publish the payload",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return publish(command.Context(), opts, command.ErrOrStderr())
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
	return command
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

// newRemoteBuildCommand forwards the developer argv to the VM worker while
// keeping controller credentials and build parameters in separate options.
func newRemoteBuildCommand() *cobra.Command {
	var opts remote.BuildOptions
	command := &cobra.Command{
		Use:   "build-remote [flags] -- COMMAND [ARG...]",
		Short: "Run a build command on the controller's remote VM",
		Args:  requireBuildCommand,
		RunE: func(command *cobra.Command, args []string) error {
			opts.BuildCmd = args
			opts.Stdout, opts.Stderr = command.OutOrStdout(), command.ErrOrStderr()
			return remote.Build(command.Context(), opts)
		},
	}

	command.Flags().StringVar(&opts.SSHDir, "ssh-dir", "/ssh", "controller Secret directory")
	command.Flags().StringVar(&opts.SourceRoot, "source", "/var/source", "source directory")
	command.Flags().StringVar(&opts.InputRoot, "inputs", "/var/workdir/inputs", "indexed inputs directory")
	command.Flags().StringVar(&opts.ControlRoot, "control", "/var/workdir/control", "trusted control directory")
	command.Flags().StringVar(&opts.BuilderImage, "image", "", "digest-pinned developer image")
	return command
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
