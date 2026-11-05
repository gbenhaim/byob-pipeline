package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var artifactReference = regexp.MustCompile(`^oci:[^\s=]+@sha256:[a-f0-9]{64}$`)
var artifactLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var manifestDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type restoredArtifact struct {
	name     string
	ref      string
	explicit bool
}

// restoreInputs validates OCI references and restores each artifact separately so
// duplicate filenames cannot overwrite outputs from another build. A bare
// reference uses its array index. A task-name label uses that name.
func restoreInputs(ctx context.Context, root string, refs []string, required bool, stdout, stderr io.Writer) error {
	if required && len(refs) == 0 {
		return fmt.Errorf("at least one build artifact is required")
	}

	if root == "" {
		return fmt.Errorf("input root is required")
	}

	parsed, err := parseArtifactInputs(refs)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}

	if len(parsed) == 0 {
		report(stderr, "No build artifacts to restore into %s", root)
		return nil
	}

	env, err := commandEnvironment()
	if err != nil {
		return err
	}

	report(stderr, "Restoring %d build artifact(s) into %s", len(parsed), root)
	for _, item := range parsed {
		destination := filepath.Join(root, item.name)
		if err := os.MkdirAll(destination, 0755); err != nil {
			return err
		}

		report(stderr, "Restoring build artifact %s from %s", item.name, item.ref)
		cmd := exec.CommandContext(ctx, "use-archive", item.ref+"="+destination)
		cmd.Stdout, cmd.Stderr, cmd.Env = stdout, stderr, env
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("restore input %s: %w", item.name, err)
		}

		report(stderr, "Restored build artifact %s into %s", item.name, destination)
	}

	return nil
}

// parseArtifactInputs checks every reference before a restore begins.
// Names are either all explicit or all array indexes.
func parseArtifactInputs(refs []string) ([]restoredArtifact, error) {
	parsed := make([]restoredArtifact, 0, len(refs))
	explicit := 0
	seen := map[string]bool{}
	for index, ref := range refs {
		item, err := parseArtifactInput(ref, index)
		if err != nil {
			return nil, err
		}

		if seen[item.name] {
			return nil, fmt.Errorf("duplicate build artifact name %q", item.name)
		}

		seen[item.name] = true
		if item.explicit {
			explicit++
		}

		parsed = append(parsed, item)
	}

	if explicit != 0 && explicit != len(parsed) {
		return nil, fmt.Errorf("build artifact names must be set on every input")
	}

	return parsed, nil
}

// parseArtifactInput splits one bare OCI reference or one name=reference input.
func parseArtifactInput(value string, index int) (restoredArtifact, error) {
	if artifactReference.MatchString(value) {
		return restoredArtifact{name: strconv.Itoa(index), ref: value}, nil
	}

	name, ref, ok := strings.Cut(value, "=")
	if !ok || !artifactLabel.MatchString(name) || !artifactReference.MatchString(ref) {
		return restoredArtifact{}, fmt.Errorf("invalid build artifact reference %q", value)
	}

	return restoredArtifact{name: name, ref: ref, explicit: true}, nil
}

// restoreSource extracts one checkout artifact directly into the source directory.
func restoreSource(ctx context.Context, ref, destination string, stdout, stderr io.Writer) error {
	if !artifactReference.MatchString(ref) {
		return fmt.Errorf("invalid source artifact reference %q", ref)
	}

	info, err := os.Stat(destination)
	if err != nil {
		return fmt.Errorf("source directory: %w", err)
	}

	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", destination)
	}

	env, err := commandEnvironment()
	if err != nil {
		return err
	}

	report(stderr, "Restoring source artifact %s into %s", ref, destination)
	cmd := exec.CommandContext(ctx, "use-archive", ref+"="+destination)
	cmd.Stdout, cmd.Stderr, cmd.Env = stdout, stderr, env
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restore source: %w", err)
	}

	report(stderr, "Restored source artifact into %s", destination)
	return nil
}

type publishOptions struct {
	Image, ArtifactType, Workdir, AuthDir, URLResult, DigestResult string
	Inputs, Package, Archive                                       string
	Artifacts                                                      []string
	PlainHTTP                                                      bool
}

// publish packs restored inputs when requested, pushes the payload, and writes
// result files only after publication succeeds.
func publish(ctx context.Context, opts publishOptions, stdout, stderr io.Writer) error {
	if err := validatePublishOptions(opts); err != nil {
		return err
	}

	if err := stagePublication(ctx, &opts, stdout, stderr); err != nil {
		return err
	}

	payload := filepath.Join(opts.Workdir, "payload.tar.gz")
	if opts.Archive != "" {
		payload = opts.Archive
	}

	report(stderr, "Publishing %s to %s as %s", payload, opts.Image, opts.ArtifactType)
	env, authfile, err := publishEnvironment(opts.AuthDir)
	if err != nil {
		return err
	}

	digest, err := publishPayload(ctx, opts, env, authfile, stderr)
	if err != nil {
		return err
	}

	if err := writePublishResults(opts, digest); err != nil {
		return err
	}

	report(stderr, "Published %s with digest %s", opts.Image, digest)
	return nil
}

// stagePublication restores required build artifacts and packs the delivery archive.
func stagePublication(ctx context.Context, opts *publishOptions, stdout, stderr io.Writer) error {
	if opts.Inputs == "" {
		if len(opts.Artifacts) > 0 {
			return fmt.Errorf("--inputs is required when artifact references are supplied")
		}

		return nil
	}

	layout, err := selectLayout(opts.Artifacts)
	if err != nil {
		return err
	}

	if err := restoreInputs(ctx, opts.Inputs, opts.Artifacts, true, stdout, stderr); err != nil {
		return err
	}

	archive := opts.Archive
	if archive == "" {
		archive = filepath.Join(opts.Workdir, "payload.tar.gz")
	}

	packageRoot := opts.Package
	if packageRoot == "" {
		packageRoot = filepath.Join(opts.Workdir, "package")
	}

	opts.Archive = archive
	return packArchive(opts.Inputs, packageRoot, archive, layout, stderr)
}

// selectLayout chooses the archive shape from the original artifact references.
// One bare reference is flat, several bare references are platform-named, and
// task labels select the named layout.
func selectLayout(refs []string) (string, error) {
	parsed, err := parseArtifactInputs(refs)
	if err != nil {
		return "", err
	}

	if len(parsed) == 0 {
		return "", fmt.Errorf("at least one build artifact is required")
	}

	labeled := 0
	for _, item := range parsed {
		if item.explicit {
			labeled++
		}
	}

	if labeled == len(parsed) {
		return "named", nil
	}

	if len(parsed) == 1 {
		return "flat", nil
	}

	return "platform", nil
}

// validatePublishOptions requires the subject metadata and all result destinations.
func validatePublishOptions(opts publishOptions) error {
	if opts.Image == "" || opts.ArtifactType == "" || opts.URLResult == "" || opts.DigestResult == "" {
		return fmt.Errorf("image, artifact-type and both result paths are required")
	}

	return nil
}

// commandEnvironment returns the child process environment. A non-empty
// CA_FILE bundle is published as SSL_CERT_FILE so ORAS trusts that registry CA.
func commandEnvironment() ([]string, error) {
	env := os.Environ()
	caFile := os.Getenv("CA_FILE")
	if caFile == "" {
		return env, nil
	}

	info, err := os.Stat(caFile)
	if err != nil {
		if os.IsNotExist(err) {
			return env, nil
		}

		return nil, err
	}

	if !info.Mode().IsRegular() || info.Size() == 0 {
		return env, nil
	}

	return setEnv(env, "SSL_CERT_FILE", caFile), nil
}

// setEnv replaces key in env, or appends it when the key is absent.
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	updated := append([]string{}, env...)
	for index, item := range updated {
		if strings.HasPrefix(item, prefix) {
			updated[index] = prefix + value
			return updated
		}
	}

	return append(updated, prefix+value)
}

// publishEnvironment configures registry credentials for the ORAS process.
func publishEnvironment(authDir string) ([]string, string, error) {
	env, err := commandEnvironment()
	if err != nil {
		return nil, "", err
	}

	authfile := filepath.Join(authDir, "config.json")
	info, err := os.Stat(authfile)
	if err == nil && info.Mode().IsRegular() {
		env = append(env, "DOCKER_CONFIG="+authDir)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, "", err
	} else {
		return env, "", nil
	}

	return env, authfile, nil
}

// publishPayload pushes the OCI artifact and returns the registry manifest digest.
func publishPayload(
	ctx context.Context,
	opts publishOptions,
	env []string,
	authfile string,
	stderr io.Writer,
) (string, error) {
	args := []string{"push", "--format", "json", "--artifact-type", opts.ArtifactType}
	if authfile != "" {
		args = append(args, "--registry-config", authfile)
	}

	if opts.PlainHTTP {
		args = append(args, "--plain-http")
	}

	payloadName := "payload.tar.gz"
	payloadDir := opts.Workdir
	if opts.Archive != "" {
		payloadName = filepath.Base(opts.Archive)
		payloadDir = filepath.Dir(opts.Archive)
	}

	args = append(args, opts.Image, payloadName+":application/vnd.oci.image.layer.v1.tar+gzip")
	cmd := exec.CommandContext(ctx, "oras", args...)
	cmd.Dir, cmd.Env, cmd.Stderr = payloadDir, env, stderr
	var manifest bytes.Buffer
	cmd.Stdout = &manifest
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("publish payload: %w", err)
	}

	var result struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(manifest.Bytes(), &result); err != nil {
		return "", fmt.Errorf("read publication digest: %w", err)
	}

	if !manifestDigest.MatchString(result.Digest) {
		return "", fmt.Errorf("registry returned an invalid manifest digest")
	}

	return result.Digest, nil
}

// writePublishResults records the subject only after the registry write succeeds.
func writePublishResults(opts publishOptions, digest string) error {
	results := []struct {
		path  string
		value string
	}{
		{opts.URLResult, opts.Image},
		{opts.DigestResult, digest},
	}
	for _, result := range results {
		if err := os.WriteFile(result.path, []byte(result.value), 0644); err != nil {
			return err
		}
	}

	return nil
}
