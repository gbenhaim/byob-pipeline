package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	for index, ref := range refs {
		if !artifactReference.MatchString(ref) {
			return fmt.Errorf("invalid build artifact reference %q", ref)
		}
		destination := filepath.Join(root, strconv.Itoa(index))
		if err := os.MkdirAll(destination, 0755); err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, "use-archive", ref+"="+destination)
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("restore input %d: %w", index, err)
		}
	}
	return nil
}

type publishOptions struct {
	Image, ArtifactType, Workdir, AuthDir, URLResult, DigestResult, SBOMResult string
	PlainHTTP                                                                  bool
}

func publish(ctx context.Context, opts publishOptions, stdout, stderr io.Writer) error {
	if opts.Image == "" || opts.ArtifactType == "" || opts.URLResult == "" || opts.DigestResult == "" || opts.SBOMResult == "" {
		return fmt.Errorf("image, artifact-type and all three result paths are required")
	}
	args := []string{"push", "--format", "json", "--artifact-type", opts.ArtifactType}
	env := os.Environ()
	authfile := filepath.Join(opts.AuthDir, "config.json")
	if info, err := os.Stat(authfile); err == nil && info.Mode().IsRegular() {
		args = append(args, "--registry-config", authfile)
		env = append(env, "DOCKER_CONFIG="+opts.AuthDir)
	} else if err != nil && !os.IsNotExist(err) {
		return err
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
		return fmt.Errorf("publish payload: %w", err)
	}
	var result struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(manifest.Bytes(), &result); err != nil {
		return fmt.Errorf("read publication digest: %w", err)
	}
	if !manifestDigest.MatchString(result.Digest) {
		return fmt.Errorf("registry returned an invalid manifest digest")
	}
	repo := strings.SplitN(opts.Image, "@", 2)[0]
	if colon := strings.LastIndex(repo, ":"); colon > strings.LastIndex(repo, "/") {
		repo = repo[:colon]
	}
	args = []string{"attach", "sbom", "--sbom", "sbom.spdx.json", "--type", "spdx"}
	if opts.PlainHTTP {
		args = append(args, "--allow-http-registry")
	}
	args = append(args, repo+"@"+result.Digest)
	cmd = exec.CommandContext(ctx, "cosign", args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = opts.Workdir, env, stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("attach SBOM: %w", err)
	}
	sbom, err := os.ReadFile(filepath.Join(opts.Workdir, "sbom.spdx.json"))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(sbom)
	// No final subject is emitted until both publication operations succeeded.
	for _, value := range []struct{ path, text string }{{opts.URLResult, opts.Image}, {opts.DigestResult, result.Digest}, {opts.SBOMResult, fmt.Sprintf("%s@sha256:%x", repo, sum)}} {
		if err := os.WriteFile(value.path, []byte(value.text), 0644); err != nil {
			return err
		}
	}
	return nil
}
