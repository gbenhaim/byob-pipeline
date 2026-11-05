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
// the build output directory, and the immutable developer image and executable argv.
type BuildOptions struct {
	SSHDir, SourceRoot, InputRoot, OutputRoot, ControlRoot, BuilderImage string
	BuildCmd                                                             []string
	Stdout, Stderr                                                       io.Writer
}

// Build runs the developer container on the controller's VM and returns only its
// BUILD_OUTPUT directory. The checkout and controller credentials stay separate
// from the container. Context cancellation stops local transfers and attempts
// remote cleanup using a fresh, bounded context before deleting the private key.
func Build(ctx context.Context, options BuildOptions) (result error) {
	defer func() {
		if result != nil && ctx.Err() != nil {
			result = errors.Join(result, ctx.Err())
		}

	}()
	entrypoint, err := buildEntrypoint(options.BuildCmd)
	if err != nil {
		return err
	}

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
	session, err := newSSHSession(ctx, options.SSHDir, private, host, options.Stdout, options.Stderr)
	if err != nil {
		return err
	}

	directory, err := createRemoteWorkspace(ctx, session, base)
	if err != nil {
		return err
	}

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := session.run(cleanupCtx, cleanupCommand(directory), nil, io.Discard); err != nil {
			result = errors.Join(result, fmt.Errorf("clean up remote build: %w", err))
		}

	}()

	if err := createRemoteDirectories(ctx, session, directory); err != nil {
		return err
	}

	if err := transferBuildInputs(ctx, session, options, directory); err != nil {
		return err
	}

	image, err := runBuildCommand(ctx, session, options, entrypoint, directory)
	if err != nil {
		return err
	}

	if err := returnBuildOutput(ctx, session, options.OutputRoot, directory); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(options.ControlRoot, "builder-image"), []byte(image), 0o600)
}

// WriteBuildCommand stores the trusted build argv for a later remote execution.
func WriteBuildCommand(path string, command []string) error {
	if path == "" {
		return errors.New("build command file is required")
	}

	if len(command) == 0 || command[0] == "" {
		return errors.New("build command must contain a non-empty executable")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	data, err := json.Marshal(command)
	if err != nil {
		return fmt.Errorf("encode build command: %w", err)
	}

	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// LoadBuildCommand reads the trusted build argv and appends caller arguments.
// Positional arguments never replace a missing executable.
func LoadBuildCommand(path string, args []string) ([]string, error) {
	if path == "" {
		return nil, errors.New("build command file is required")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read build command: %w", err)
	}

	var command []string
	if err := json.Unmarshal(data, &command); err != nil {
		return nil, fmt.Errorf("read build command: %w", err)
	}

	if len(command) == 0 || command[0] == "" {
		return nil, errors.New("build command must contain a non-empty executable")
	}

	combined := make([]string, 0, len(command)+len(args))
	combined = append(combined, command...)
	combined = append(combined, args...)
	return combined, nil
}

// buildEntrypoint validates and encodes the executable as a one-element JSON array.
func buildEntrypoint(command []string) ([]byte, error) {
	if len(command) == 0 || command[0] == "" {
		return nil, errors.New("build command must contain a non-empty executable")
	}

	// Podman parses --entrypoint as JSON when possible. An explicit one-element
	// array preserves executable names that themselves resemble JSON or shell text.
	entrypoint, err := json.Marshal(command[:1])
	if err != nil {
		return nil, fmt.Errorf("encode build executable: %w", err)
	}

	return entrypoint, nil
}

// newSSHSession prepares private credentials and fixed SSH options for one build.
func newSSHSession(ctx context.Context, sshDir, privateDir, host string, stdout, stderr io.Writer) (sshSession, error) {
	if err := prepareKey(ctx, sshDir, filepath.Join(privateDir, "key")); err != nil {
		return sshSession{}, err
	}

	mode, err := prepareKnownHosts(sshDir, privateDir)
	if err != nil {
		return sshSession{}, err
	}

	stdout, stderr = lockOutputWriters(stdout, stderr)
	session := sshSession{
		args: []string{
			"-F", "/dev/null", "-i", filepath.Join(privateDir, "key"),
			"-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
			"-o", "ServerAliveInterval=30", "-o", "ServerAliveCountMax=3",
			"-o", "UserKnownHostsFile=" + filepath.Join(privateDir, "known_hosts"),
			"-o", "StrictHostKeyChecking=" + mode, host,
		},
		stdout: stdout,
		stderr: stderr,
	}

	return session, nil
}

// prepareKnownHosts copies pinned host keys and requires strict SSH verification.
func prepareKnownHosts(sshDir, privateDir string) (string, error) {
	knownHosts, err := os.ReadFile(filepath.Join(sshDir, "known_hosts"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("controller known_hosts is required")
		}

		return "", err
	}

	if len(bytes.TrimSpace(knownHosts)) == 0 {
		return "", errors.New("controller known_hosts is required")
	}

	path := filepath.Join(privateDir, "known_hosts")
	if err := os.WriteFile(path, knownHosts, 0o600); err != nil {
		return "", err
	}

	return "yes", nil
}

// lockOutputWriters selects defaults and serializes writes from parallel processes.
func lockOutputWriters(stdout, stderr io.Writer) (io.Writer, io.Writer) {
	if stdout == nil {
		stdout = os.Stdout
	}

	if stderr == nil {
		stderr = os.Stderr
	}

	var outputLock sync.Mutex
	return lockedWriter{&outputLock, stdout}, lockedWriter{&outputLock, stderr}
}

// createRemoteWorkspace requests a temporary child and validates it before cleanup uses its path.
func createRemoteWorkspace(ctx context.Context, session sshSession, base string) (string, error) {
	directory, err := session.output(ctx, "mktemp -d "+quote(filepath.Join(base, "byob.XXXXXXXX")))
	if err != nil {
		return "", fmt.Errorf("create remote directory: %w", err)
	}

	directory = strings.TrimRight(directory, "\n")

	// Only accept one newly allocated child. Never use a server's unexpected
	// response as the target of the deferred recursive deletion.
	if filepath.Dir(directory) != base ||
		!strings.HasPrefix(filepath.Base(directory), "byob.") ||
		strings.ContainsAny(directory, "\r\n") {
		return "", errors.New("invalid remote temporary directory")
	}

	return directory, nil
}

// createRemoteDirectories prepares separate source and artifact-input trees on the VM.
func createRemoteDirectories(ctx context.Context, session sshSession, directory string) error {
	script := "mkdir -p " + quote(directory+"/source") + " " + quote(directory+"/inputs")
	if err := session.run(ctx, script, nil, io.Discard); err != nil {
		return fmt.Errorf("create remote workspace: %w", err)
	}

	return nil
}

// transferBuildInputs sends the checkout and every restored artifact input to the VM.
func transferBuildInputs(ctx context.Context, session sshSession, options BuildOptions, directory string) error {
	transfers := []struct {
		local  string
		remote string
	}{
		{options.SourceRoot, directory + "/source"},
		{options.InputRoot, directory + "/inputs"},
	}

	for _, transfer := range transfers {
		if err := transferDirectory(ctx, session, transfer.local, transfer.remote); err != nil {
			return fmt.Errorf("transfer %s: %w", transfer.local, err)
		}
	}

	return nil
}

// transferDirectory streams one local directory to a remote directory using tar over SSH.
func transferDirectory(ctx context.Context, session sshSession, local, remote string) error {
	transferCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	tar := command(transferCtx, "tar", "-C", local, "-cf", "-", ".")
	tar.Stderr = session.stderr
	ssh := session.command(transferCtx, "tar -xf - -C "+quote(remote))
	ssh.Stdout = session.stdout

	return pipeline(tar, ssh, cancel)
}

// runBuildCommand starts the remote worker and validates the image digest it observed.
func runBuildCommand(
	ctx context.Context,
	session sshSession,
	options BuildOptions,
	entrypoint []byte,
	directory string,
) (string, error) {
	var arguments strings.Builder
	arguments.WriteString(quote(directory))
	arguments.WriteByte(' ')
	arguments.WriteString(quote(options.BuilderImage))
	arguments.WriteByte(' ')
	arguments.WriteString(quote(string(entrypoint)))
	for _, argument := range options.BuildCmd[1:] {
		arguments.WriteByte(' ')
		arguments.WriteString(quote(argument))
	}

	if err := session.run(ctx, "bash -s -- "+arguments.String(), strings.NewReader(worker), session.stdout); err != nil {
		return "", fmt.Errorf("remote build: %w", err)
	}

	image, err := session.output(ctx, "cat "+quote(directory+"/builder-image"))
	if err != nil {
		return "", fmt.Errorf("read remote builder image: %w", err)
	}

	if !pinnedImage.MatchString(image) {
		return "", errors.New("remote builder reported an invalid immutable image reference")
	}

	return image, nil
}

// returnBuildOutput extracts the VM deliverables into the build output directory.
// The temporary directory is on that same volume so the final rename stays local.
func returnBuildOutput(ctx context.Context, session sshSession, outputRoot, directory string) (result error) {
	if err := emptyRealDirectory(outputRoot); err != nil {
		return err
	}

	returned, err := os.MkdirTemp(outputRoot, ".byob-returned-")
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, os.RemoveAll(returned)) }()

	transferCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ssh := session.command(transferCtx, "tar -C "+quote(directory+"/output")+" -cf - .")
	tar := command(transferCtx, "tar", "--no-same-owner", "-xf", "-", "-C", returned)
	tar.Stderr = session.stderr
	if err := pipeline(ssh, tar, cancel); err != nil {
		return fmt.Errorf("return build output: %w", err)
	}

	return moveChildren(returned, outputRoot)
}

// emptyRealDirectory requires a real, empty directory before deliverables arrive.
func emptyRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("build output directory must be a real directory: %s", path)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}

	if len(entries) != 0 {
		return fmt.Errorf("build output directory must be empty: %s", path)
	}

	return nil
}

// moveChildren renames every immediate child from source into destination.
func moveChildren(source, destination string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if err := os.Rename(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}

	return nil
}

// readValue reads controller metadata while removing trailing line-feed delimiters.
func readValue(path string) (string, error) {
	data, err := os.ReadFile(path)
	return strings.TrimRight(string(data), "\n"), err
}

// prepareKey privately copies the controller key or exchanges its OTP with the controller's HTTPS server.
func prepareKey(ctx context.Context, sshDir, destination string) error {
	otp, err := os.ReadFile(filepath.Join(sshDir, "otp"))
	if errors.Is(err, os.ErrNotExist) {
		return copyControllerKey(sshDir, destination)
	}

	if err != nil {
		return err
	}

	return exchangeOTP(ctx, sshDir, destination, otp)
}

// copyControllerKey installs the controller-provided SSH key with private permissions.
func copyControllerKey(sshDir, destination string) error {
	key, err := os.ReadFile(filepath.Join(sshDir, "id_rsa"))
	if err != nil {
		return err
	}

	return os.WriteFile(destination, key, 0o600)
}

// exchangeOTP requests a private SSH key using the controller's one-time credential.
func exchangeOTP(ctx context.Context, sshDir, destination string, otp []byte) error {
	server, client, transport, err := newOTPClient(sshDir)
	if err != nil {
		return err
	}

	defer transport.CloseIdleConnections()

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

// newOTPClient validates the OTP endpoint and builds a client trusting only its supplied CA.
func newOTPClient(sshDir string) (string, *http.Client, *http.Transport, error) {
	server, err := readValue(filepath.Join(sshDir, "otp-server"))
	if err != nil {
		return "", nil, nil, err
	}

	address, err := url.Parse(server)
	if err != nil || address.Scheme != "https" || address.Host == "" || address.User != nil {
		return "", nil, nil, errors.New("OTP server must be an HTTPS URL without user credentials")
	}

	ca, err := os.ReadFile(filepath.Join(sshDir, "otp-ca"))
	if err != nil {
		return "", nil, nil, err
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return "", nil, nil, errors.New("invalid OTP CA certificate")
	}

	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	client := &http.Client{
		Transport: transport,
		Timeout:   time.Minute,
		// A one-time credential must never be forwarded to a redirected origin.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	return server, client, transport, nil
}

// quote produces one POSIX shell argument. SSH always sends a command string to
// the remote shell, so exec.Command's argument separation alone is insufficient.
func quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// cleanupCommand removes any recorded remote container before deleting its temporary workspace.
func cleanupCommand(directory string) string {
	return "if [ -s " + quote(directory+"/container-id") + " ]; then " +
		"podman --default-mounts-file=/dev/null rm -f \"$(cat " + quote(directory+"/container-id") + ")\" >/dev/null; fi; " +
		"rm -rf -- " + quote(directory)
}

type sshSession struct {
	args   []string
	stdout io.Writer
	stderr io.Writer
}

// command creates an SSH process with the session's fixed connection options and error stream.
func (s sshSession) command(ctx context.Context, script string) *exec.Cmd {
	cmd := command(ctx, "ssh", append(append([]string{}, s.args...), script)...)
	cmd.Stderr = s.stderr
	return cmd
}

// run executes a remote shell command while streaming input and output.
func (s sshSession) run(ctx context.Context, script string, stdin io.Reader, stdout io.Writer) error {
	cmd := s.command(ctx, script)
	cmd.Stdin, cmd.Stdout = stdin, stdout
	return cmd.Run()
}

// output executes a remote shell command and captures its standard output.
func (s sshSession) output(ctx context.Context, script string) (string, error) {
	var output bytes.Buffer
	err := s.run(ctx, script, nil, &output)
	return output.String(), err
}

// command creates a cancellable process whose entire process group is terminated with its context.
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

// pipeline streams between two processes and cancels their shared context if either fails.
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

// Write serializes writes from concurrent transfer processes to the underlying output writer.
func (w lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
}
