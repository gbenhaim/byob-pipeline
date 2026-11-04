package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gbenhaim/byob-pipeline/internal/artifact"
	"github.com/spf13/cobra"
)

const (
	payloadName      = "payload.tar.gz"
	payloadMediaType = "application/vnd.oci.image.layer.v1.tar+gzip"
	payloadTitle     = "org.opencontainers.image.title"
)

type unpackOptions struct {
	Image, Destination, ArtifactType, AuthDir string
	PlainHTTP                                 bool
}

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Annotations map[string]string `json:"annotations"`
}

type ociManifest struct {
	ArtifactType string          `json:"artifactType"`
	Layers       []ociDescriptor `json:"layers"`
}

// newUnpackCommand extracts a build-output artifact addressed by its manifest digest.
func newUnpackCommand() *cobra.Command {
	var opts unpackOptions
	command := &cobra.Command{
		Use:   "unpack",
		Short: "Unpack a build-output OCI artifact",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return unpack(command.Context(), opts, command.ErrOrStderr())
		},
	}

	command.Flags().StringVar(&opts.Image, "image", "", "digest-pinned build-output image")
	command.Flags().StringVar(&opts.Destination, "destination", "", "directory that receives the payload")
	command.Flags().StringVar(
		&opts.ArtifactType,
		"artifact-type",
		"application/vnd.konflux.build-output.v1",
		"required OCI artifact type",
	)
	command.Flags().StringVar(
		&opts.AuthDir,
		"auth-dir",
		"/var/run/byob-registry-auth",
		"registry authentication directory",
	)
	command.Flags().BoolVar(&opts.PlainHTTP, "plain-http", false, "use HTTP for a disposable test registry")
	return command
}

// unpack fetches one build-output manifest, checks its payload layer, and extracts it.
func unpack(ctx context.Context, opts unpackOptions, stderr io.Writer) error {
	if err := validateUnpackOptions(opts); err != nil {
		return err
	}

	repository, err := repositoryName(opts.Image)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(opts.Destination, 0755); err != nil {
		return err
	}

	if err := artifact.CheckDestination(opts.Destination); err != nil {
		return err
	}

	env, authfile, err := publishEnvironment(opts.AuthDir)
	if err != nil {
		return err
	}

	report(stderr, "Unpacking %s as %s into %s", opts.Image, opts.ArtifactType, opts.Destination)
	manifest, err := fetchManifest(ctx, opts, authfile, env, stderr)
	if err != nil {
		return err
	}

	layer, err := payloadLayer(manifest, opts.ArtifactType)
	if err != nil {
		return err
	}

	archive, err := fetchLayer(ctx, repository, layer.Digest, opts, authfile, env, stderr)
	if err != nil {
		return err
	}

	defer os.RemoveAll(filepath.Dir(archive))
	downloaded, err := digestFile(archive)
	if err != nil {
		return err
	}

	if downloaded != layer.Digest {
		return fmt.Errorf("downloaded layer digest %s does not match %s", downloaded, layer.Digest)
	}

	if err := artifact.ExtractArchive(archive, opts.Destination); err != nil {
		return fmt.Errorf("extract payload: %w", err)
	}

	report(stderr, "Unpacked %s into %s", opts.Image, opts.Destination)
	return nil
}

// validateUnpackOptions requires the image, destination, and artifact type.
func validateUnpackOptions(opts unpackOptions) error {
	if opts.Image == "" || opts.Destination == "" || opts.ArtifactType == "" {
		return fmt.Errorf("image, destination, and artifact type are required")
	}

	return nil
}

// repositoryName accepts registry/repo@sha256:<manifest-digest> and returns the repository.
func repositoryName(reference string) (string, error) {
	if strings.HasPrefix(reference, "oci:") || strings.ContainsAny(reference, " \t\r\n") {
		return "", invalidImageReference()
	}

	name, digest, ok := strings.Cut(reference, "@")
	if !ok || !manifestDigest.MatchString(digest) || strings.Contains(name, "@") || strings.Contains(name, "..") {
		return "", invalidImageReference()
	}

	host, repo, ok := strings.Cut(name, "/")
	if !ok || repo == "" || strings.Contains(repo, ":") || !registryHost(host) {
		return "", invalidImageReference()
	}

	for _, part := range strings.Split(repo, "/") {
		if part == "" || part == "." || part == ".." {
			return "", invalidImageReference()
		}
	}

	return name, nil
}

// registryHost reports whether host is a registry name or a registry name and numeric port.
func registryHost(host string) bool {
	if host == "" {
		return false
	}

	name := host
	if colon := strings.LastIndex(host, ":"); colon >= 0 {
		port := host[colon+1:]
		if port == "" {
			return false
		}

		for _, r := range port {
			if r < '0' || r > '9' {
				return false
			}
		}

		name = host[:colon]
	}

	return name != "" && !strings.Contains(name, "/")
}

// invalidImageReference describes the manifest digest reference unpack accepts.
func invalidImageReference() error {
	return fmt.Errorf("image reference must be registry/repo@sha256:<manifest-digest>")
}

// fetchManifest reads the OCI manifest for the requested image.
func fetchManifest(
	ctx context.Context,
	opts unpackOptions,
	authfile string,
	env []string,
	stderr io.Writer,
) ([]byte, error) {
	args := orasFetchArgs("manifest", authfile, opts.PlainHTTP)
	args = append(args, opts.Image)
	var stdout bytes.Buffer
	if err := runORAS(ctx, args, env, &stdout, stderr); err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}

	return stdout.Bytes(), nil
}

// payloadLayer requires the manifest to carry one payload.tar.gz layer of the expected type.
func payloadLayer(manifest []byte, artifactType string) (ociDescriptor, error) {
	var document ociManifest
	if err := json.Unmarshal(manifest, &document); err != nil {
		return ociDescriptor{}, fmt.Errorf("read manifest: %w", err)
	}

	if document.ArtifactType != artifactType {
		return ociDescriptor{}, fmt.Errorf("artifact type %q does not match %q", document.ArtifactType, artifactType)
	}

	if len(document.Layers) != 1 {
		return ociDescriptor{}, fmt.Errorf("build-output artifact must contain exactly one layer")
	}

	layer := document.Layers[0]
	title := ""
	if layer.Annotations != nil {
		title = layer.Annotations[payloadTitle]
	}

	if title != payloadName {
		return ociDescriptor{}, fmt.Errorf("layer name %q does not match %q", title, payloadName)
	}

	if layer.MediaType != payloadMediaType {
		return ociDescriptor{}, fmt.Errorf("layer media type %q does not match %q", layer.MediaType, payloadMediaType)
	}

	if !manifestDigest.MatchString(layer.Digest) {
		return ociDescriptor{}, fmt.Errorf("layer digest is invalid")
	}

	return layer, nil
}

// fetchLayer downloads the payload blob to a temporary directory and returns its path.
func fetchLayer(
	ctx context.Context,
	repository, digest string,
	opts unpackOptions,
	authfile string,
	env []string,
	stderr io.Writer,
) (string, error) {
	dir, err := os.MkdirTemp("", "byob-unpack-")
	if err != nil {
		return "", err
	}

	output := filepath.Join(dir, payloadName)
	args := orasFetchArgs("blob", authfile, opts.PlainHTTP, "--output", output)
	args = append(args, repository+"@"+digest)
	if err := runORAS(ctx, args, env, io.Discard, stderr); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("fetch layer: %w", err)
	}

	return output, nil
}

// orasFetchArgs builds an oras fetch invocation with the registry options for this unpack.
func orasFetchArgs(command, authfile string, plain bool, extra ...string) []string {
	args := []string{command, "fetch", "--no-tty"}
	args = append(args, extra...)
	if authfile != "" {
		args = append(args, "--registry-config", authfile)
	}

	if plain {
		args = append(args, "--plain-http")
	}

	return args
}

// runORAS executes oras and keeps its stdout separate from the Task log.
func runORAS(ctx context.Context, args, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "oras", args...)
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// digestFile returns the sha256 digest of a downloaded layer.
func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}

	defer file.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}

	return "sha256:" + hex.EncodeToString(sum.Sum(nil)), nil
}
