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
var manifestDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// restoreInputs validates OCI references and restores each artifact separately so
// duplicate filenames cannot overwrite outputs from another build.
func restoreInputs(ctx context.Context, root string, refs []string, required bool, stdout, stderr io.Writer) error {
	if required && len(refs) == 0 {
		return fmt.Errorf("at least one build artifact is required")
	}

	if root == "" {
		return fmt.Errorf("input root is required")
	}

	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}

	env, err := commandEnvironment()
	if err != nil {
		return err
	}

	for index, ref := range refs {
		if !artifactReference.MatchString(ref) {
			return fmt.Errorf("invalid build artifact reference %q", ref)
		}

		destination := filepath.Join(root, strconv.Itoa(index))
		if err := os.MkdirAll(destination, 0755); err != nil {
			return err
		}

		cmd := exec.CommandContext(ctx, "use-archive", ref+"="+destination)
		cmd.Stdout, cmd.Stderr, cmd.Env = stdout, stderr, env
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("restore input %d: %w", index, err)
		}
	}

	return nil
}

type publishOptions struct {
	Image, ArtifactType, Workdir, AuthDir, URLResult, DigestResult string
	PlainHTTP                                                      bool
}

// publish pushes the payload, then writes result files after publication succeeds.
func publish(ctx context.Context, opts publishOptions, stderr io.Writer) error {
	if err := validatePublishOptions(opts); err != nil {
		return err
	}

	env, authfile, err := publishEnvironment(opts.AuthDir)
	if err != nil {
		return err
	}

	digest, err := publishPayload(ctx, opts, env, authfile, stderr)
	if err != nil {
		return err
	}

	return writePublishResults(opts, digest)
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

	args = append(args, opts.Image, "payload.tar.gz:application/vnd.oci.image.layer.v1.tar+gzip")
	cmd := exec.CommandContext(ctx, "oras", args...)
	cmd.Dir, cmd.Env, cmd.Stderr = opts.Workdir, env, stderr
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
