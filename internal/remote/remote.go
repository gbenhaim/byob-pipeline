// Package remote implements controller selection and SSH build orchestration.
package remote

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed worker.sh
var worker string

var (
	pinnedImage = regexp.MustCompile(`^.+@sha256:[a-f0-9]{64}$`)
	validHost   = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.@:-]*$`)
	validBase   = regexp.MustCompile(`^/[a-zA-Z0-9_./-]+$`)
)

// SelectPlatform records whether the controller selected the local developer step
// or a remote VM. The local path does not require an SSH key.
func SelectPlatform(sshDir, resultPath string) error {
	if data, err := os.ReadFile(filepath.Join(sshDir, "error")); err == nil {
		return fmt.Errorf("controller failed: %s", data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	host, err := readValue(filepath.Join(sshDir, "host"))
	if err != nil {
		return err
	}
	if host == "" {
		return errors.New("controller supplied an empty host")
	}
	mode := "remote"
	if host == "localhost" {
		mode = "local"
	}
	return os.WriteFile(resultPath, []byte(mode), 0o600)
}

// RecordImage records the requested immutable local image or the manifest digest
// that Podman actually observed on the VM.
func RecordImage(mode, image, controlRoot, resultPath string) error {
	if mode == "remote" {
		var err error
		image, err = readValue(filepath.Join(controlRoot, "builder-image"))
		if err != nil {
			return err
		}
	}
	if !pinnedImage.MatchString(image) {
		return errors.New("builder must use an immutable sha256 image reference")
	}
	separator := strings.LastIndexByte(image, '@')
	data, err := json.Marshal(struct {
		URI    string `json:"uri"`
		Digest string `json:"digest"`
	}{URI: image[:separator], Digest: image[separator+1:]})
	if err != nil {
		return err
	}
	return os.WriteFile(resultPath, append(data, '\n'), 0o600)
}

// BuildOptions identifies the controller credentials, source and indexed inputs,
// and the immutable developer image and source-relative script to run.
type BuildOptions struct {
	SSHDir, SourceRoot, InputRoot, ControlRoot, BuilderImage, ScriptPath string
	Stdout, Stderr                                                       io.Writer
}

// Build runs the developer container on the controller's VM and returns only its
// out/ directory. The checkout and controller credentials stay separate from the
// container. Context cancellation stops local transfers and attempts remote cleanup
// using a fresh, bounded context before deleting the private key.
func Build(ctx context.Context, options BuildOptions) (result error) {
	defer func() {
		if result != nil && ctx.Err() != nil {
			result = errors.Join(result, ctx.Err())
		}
	}()
	host, err := readValue(filepath.Join(options.SSHDir, "host"))
	if err != nil {
		return err
	}
	if !validHost.MatchString(host) {
		return errors.New("invalid SSH host")
	}
	if !pinnedImage.MatchString(options.BuilderImage) {
		return errors.New("builder image must be digest-pinned")
	}
	base, err := readValue(filepath.Join(options.SSHDir, "user-dir"))
	if err != nil {
		return err
	}
	if !validBase.MatchString(base) || strings.Contains("/"+base+"/", "/../") {
		return errors.New("invalid controller user-dir")
	}
	base = filepath.Clean(base)
	if err := os.MkdirAll(options.ControlRoot, 0o700); err != nil {
		return err
	}
	private, err := os.MkdirTemp("", "byob-ssh-")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.RemoveAll(private)) }()
	if err := prepareKey(ctx, options.SSHDir, filepath.Join(private, "key")); err != nil {
		return err
	}
	mode := "accept-new"
	if knownHosts, err := os.ReadFile(filepath.Join(options.SSHDir, "known_hosts")); err == nil {
		if err := os.WriteFile(filepath.Join(private, "known_hosts"), knownHosts, 0o600); err != nil {
			return err
		}
		mode = "yes"
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stdout, stderr := options.Stdout, options.Stderr
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	// Several subprocesses can report errors concurrently during a transfer.
	var outputLock sync.Mutex
	stdout = lockedWriter{&outputLock, stdout}
	stderr = lockedWriter{&outputLock, stderr}
	session := sshSession{
		args: []string{"-F", "/dev/null", "-i", filepath.Join(private, "key"),
			"-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
			"-o", "ServerAliveInterval=30", "-o", "ServerAliveCountMax=3",
			"-o", "UserKnownHostsFile=" + filepath.Join(private, "known_hosts"),
			"-o", "StrictHostKeyChecking=" + mode, host},
		stderr: stderr,
	}
	directory, err := session.output(ctx, "mktemp -d "+quote(filepath.Join(base, "byob.XXXXXXXX")))
	if err != nil {
		return fmt.Errorf("create remote directory: %w", err)
	}
	directory = strings.TrimRight(directory, "\n")
	// Only accept one newly allocated child. Never use a server's unexpected
	// response as the target of the deferred recursive deletion.
	if filepath.Dir(directory) != base || !strings.HasPrefix(filepath.Base(directory), "byob.") || strings.ContainsAny(directory, "\r\n") {
		return errors.New("invalid remote temporary directory")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := session.run(cleanupCtx, cleanupCommand(directory), nil, io.Discard); err != nil {
			result = errors.Join(result, fmt.Errorf("clean up remote build: %w", err))
		}
	}()
	if err := session.run(ctx, "mkdir -p "+quote(directory+"/source")+" "+quote(directory+"/inputs"), nil, io.Discard); err != nil {
		return fmt.Errorf("create remote workspace: %w", err)
	}
	for _, transfer := range []struct{ local, remote string }{
		{options.SourceRoot, directory + "/source"},
		{options.InputRoot, directory + "/inputs"},
	} {
		transferCtx, cancel := context.WithCancel(ctx)
		tar := command(transferCtx, "tar", "-C", transfer.local, "-cf", "-", ".")
		tar.Stderr = stderr
		ssh := session.command(transferCtx, "tar -xf - -C "+quote(transfer.remote))
		ssh.Stdout = stdout
		err := pipeline(tar, ssh, cancel)
		cancel()
		if err != nil {
			return fmt.Errorf("transfer %s: %w", transfer.local, err)
		}
	}
	args := quote(directory) + " " + quote(options.BuilderImage) + " " + quote(options.ScriptPath)
	if err := session.run(ctx, "bash -s -- "+args, strings.NewReader(worker), stdout); err != nil {
		return fmt.Errorf("remote build: %w", err)
	}
	image, err := session.output(ctx, "cat "+quote(directory+"/builder-image"))
	if err != nil {
		return fmt.Errorf("read remote builder image: %w", err)
	}
	if !pinnedImage.MatchString(image) {
		return errors.New("remote builder reported an invalid immutable image reference")
	}
	if err := os.WriteFile(filepath.Join(options.ControlRoot, "builder-image"), []byte(image), 0o600); err != nil {
		return err
	}
	// Source and control are separate volumes in a Task. Stage on the source
	// filesystem so the final output rename works across that volume boundary.
	returned, err := os.MkdirTemp(options.SourceRoot, ".byob-returned-")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.RemoveAll(returned)) }()
	transferCtx, cancel := context.WithCancel(ctx)
	ssh := session.command(transferCtx, "tar -C "+quote(directory+"/output")+" -cf - .")
	tar := command(transferCtx, "tar", "--no-same-owner", "-xf", "-", "-C", returned)
	tar.Stderr = stderr
	err = pipeline(ssh, tar, cancel)
	cancel()
	if err != nil {
		return fmt.Errorf("return build output: %w", err)
	}
	output := filepath.Join(returned, "out")
	info, err := os.Lstat(output)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("out/ must be a real directory")
	}
	if err := os.RemoveAll(filepath.Join(options.SourceRoot, "out")); err != nil {
		return err
	}
	return os.Rename(output, filepath.Join(options.SourceRoot, "out"))
}

func readValue(path string) (string, error) {
	data, err := os.ReadFile(path)
	return strings.TrimRight(string(data), "\n"), err
}

func prepareKey(ctx context.Context, sshDir, destination string) error {
	otp, err := os.ReadFile(filepath.Join(sshDir, "otp"))
	if errors.Is(err, os.ErrNotExist) {
		key, err := os.ReadFile(filepath.Join(sshDir, "id_rsa"))
		if err != nil {
			return err
		}
		return os.WriteFile(destination, key, 0o600)
	}
	if err != nil {
		return err
	}
	server, err := readValue(filepath.Join(sshDir, "otp-server"))
	if err != nil {
		return err
	}
	address, err := url.Parse(server)
	if err != nil || address.Scheme != "https" || address.Host == "" || address.User != nil {
		return errors.New("OTP server must be an HTTPS URL without user credentials")
	}
	ca, err := os.ReadFile(filepath.Join(sshDir, "otp-ca"))
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return errors.New("invalid OTP CA certificate")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   time.Minute,
		// A one-time credential must never be forwarded to a redirected origin.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server, bytes.NewReader(otp))
	if err != nil {
		return errors.New("invalid OTP request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("exchange OTP: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("OTP server returned HTTP %d", response.StatusCode)
	}
	key, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil {
		return fmt.Errorf("read OTP key: %w", err)
	}
	if len(key) == 0 || len(key) > 1024*1024 {
		return errors.New("OTP server returned an empty or oversized key")
	}
	return os.WriteFile(destination, append(key, '\n'), 0o600)
}

// quote produces one POSIX shell argument. SSH always sends a command string to
// the remote shell, so exec.Command's argument separation alone is insufficient.
func quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func cleanupCommand(directory string) string {
	return "if [ -s " + quote(directory+"/container-id") + " ]; then " +
		"podman --default-mounts-file=/dev/null rm -f \"$(cat " + quote(directory+"/container-id") + ")\" >/dev/null; fi; " +
		"rm -rf -- " + quote(directory)
}

type sshSession struct {
	args   []string
	stderr io.Writer
}

func (s sshSession) command(ctx context.Context, script string) *exec.Cmd {
	cmd := command(ctx, "ssh", append(append([]string{}, s.args...), script)...)
	cmd.Stderr = s.stderr
	return cmd
}

func (s sshSession) run(ctx context.Context, script string, stdin io.Reader, stdout io.Writer) error {
	cmd := s.command(ctx, script)
	cmd.Stdin, cmd.Stdout = stdin, stdout
	return cmd.Run()
}

func (s sshSession) output(ctx context.Context, script string) (string, error) {
	var output bytes.Buffer
	err := s.run(ctx, script, nil, &output)
	return output.String(), err
}

func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	// Kill the local process group on cancellation so transfer helpers do not
	// retain pipes or continue using credentials after the SSH client exits.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		} else {
			return err
		}
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

func pipeline(producer, consumer *exec.Cmd, cancel context.CancelFunc) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	defer writer.Close()
	producer.Stdout, consumer.Stdin = writer, reader
	if err := consumer.Start(); err != nil {
		return err
	}
	if err := producer.Start(); err != nil {
		cancel()
		_ = consumer.Wait()
		return err
	}
	// Only the child processes retain the pipe ends now.
	_ = reader.Close()
	_ = writer.Close()
	done := make(chan error, 2)
	go func() { done <- producer.Wait() }()
	go func() { done <- consumer.Wait() }()
	first := <-done
	if first != nil {
		cancel()
	}
	return errors.Join(first, <-done)
}

type lockedWriter struct {
	mu     *sync.Mutex
	writer io.Writer
}

func (w lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
}
