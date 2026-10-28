// Package cli implements the small, PoC-only trusted Task command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/gbenhaim/byob-pipeline/internal/artifact"
	"github.com/gbenhaim/byob-pipeline/internal/remote"
)

// Run executes one Task operation. Parameters are data, never shell source.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("a subcommand is required (use --help)")
	}
	if args[0] == "--help" || args[0] == "help" || args[0] == "-h" {
		_, err := fmt.Fprintln(stdout, "Usage: byob <command> [options]\nCommands: prepare, stage-output, restore-inputs, pack, publish, select-platform, record-image, build-remote")
		return err
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var run func() error
	allowArgs := false
	switch args[0] {
	case "prepare":
		source := fs.String("source", "/var/workdir/source", "restored source directory")
		script := fs.String("script", "build.sh", "executable path relative to source")
		run = func() error { return artifact.Prepare(*source, *script) }
	case "stage-output":
		source := fs.String("source", "/var/workdir/source", "build source directory")
		output := fs.String("output", "/var/build-output", "private output staging directory")
		platform := fs.String("platform", "", "trusted platform metadata")
		run = func() error { return artifact.StageOutput(*source, *output, *platform) }
	case "restore-inputs":
		root := fs.String("root", "/var/workdir/inputs", "input restoration directory")
		required := fs.Bool("require", false, "require at least one artifact")
		allowArgs = true
		run = func() error { return restoreInputs(ctx, *root, fs.Args(), *required, stdout, stderr) }
	case "pack":
		input := fs.String("inputs", "/var/workdir/inputs", "indexed artifact directories")
		output := fs.String("package", "/var/workdir/package", "staged deliverables directory")
		archive := fs.String("archive", "/var/workdir/payload.tar.gz", "archive filename")
		layout := fs.String("layout", "flat", "flat or platform")
		run = func() error { return artifact.Pack(*input, *output, *archive, *layout) }
	case "publish":
		var opts publishOptions
		fs.StringVar(&opts.Image, "image", "", "tagged output image")
		fs.StringVar(&opts.ArtifactType, "artifact-type", "application/vnd.konflux.build-output.v1", "OCI artifact type")
		fs.StringVar(&opts.Workdir, "workdir", "/var/workdir", "payload and SBOM directory")
		fs.StringVar(&opts.AuthDir, "auth-dir", "/var/run/byob-registry-auth", "registry authentication directory")
		fs.StringVar(&opts.URLResult, "url-result", "", "IMAGE_URL result file")
		fs.StringVar(&opts.DigestResult, "digest-result", "", "IMAGE_DIGEST result file")
		fs.StringVar(&opts.SBOMResult, "sbom-result", "", "SBOM_BLOB_URL result file")
		fs.BoolVar(&opts.PlainHTTP, "plain-http", false, "use HTTP for a disposable test registry")
		run = func() error { return publish(ctx, opts, stdout, stderr) }
	case "select-platform":
		sshDir := fs.String("ssh-dir", "/ssh", "controller Secret directory")
		result := fs.String("result", "", "MODE result file")
		run = func() error { return remote.SelectPlatform(*sshDir, *result) }
	case "record-image":
		mode := fs.String("mode", "", "local or remote execution")
		image := fs.String("image", "", "requested builder image")
		control := fs.String("control", "/var/workdir/control", "trusted control directory")
		result := fs.String("result", "", "BUILD_IMAGE_ARTIFACT_INPUTS result file")
		run = func() error { return remote.RecordImage(*mode, *image, *control, *result) }
	case "build-remote":
		opts := remote.BuildOptions{Stdout: stdout, Stderr: stderr}
		fs.StringVar(&opts.SSHDir, "ssh-dir", "/ssh", "controller Secret directory")
		fs.StringVar(&opts.SourceRoot, "source", "/var/workdir/source", "source directory")
		fs.StringVar(&opts.InputRoot, "inputs", "/var/workdir/inputs", "indexed inputs directory")
		fs.StringVar(&opts.ControlRoot, "control", "/var/workdir/control", "trusted control directory")
		fs.StringVar(&opts.BuilderImage, "image", "", "digest-pinned developer image")
		fs.StringVar(&opts.ScriptPath, "script", "build.sh", "executable path relative to source")
		run = func() error { return remote.Build(ctx, opts) }
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !allowArgs && fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments for %s: %v", args[0], fs.Args())
	}
	return run()
}
