// Package launcher stages and directly executes the local developer build.
package launcher

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Install atomically copies the current executable to directory/byob with
// read/execute permissions. The trusted CLI is built with CGO_ENABLED=0 so this
// copy can run in developer images without a shell or dynamic loader.
func Install(directory string) error {
	if directory == "" {
		return fmt.Errorf("launcher directory is required")
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate launcher: %w", err)
	}
	source, err := os.Open(executable)
	if err != nil {
		return fmt.Errorf("open launcher: %w", err)
	}
	defer source.Close()
	if err := os.MkdirAll(directory, 0755); err != nil {
		return fmt.Errorf("create launcher directory: %w", err)
	}
	staged, err := os.CreateTemp(directory, ".byob-")
	if err != nil {
		return fmt.Errorf("stage launcher: %w", err)
	}
	defer os.Remove(staged.Name())
	defer staged.Close()
	if _, err := io.Copy(staged, source); err != nil {
		return fmt.Errorf("copy launcher: %w", err)
	}
	if err := staged.Chmod(0555); err != nil {
		return fmt.Errorf("make launcher executable: %w", err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("close launcher: %w", err)
	}
	if err := os.Rename(staged.Name(), filepath.Join(directory, "byob")); err != nil {
		return fmt.Errorf("install launcher: %w", err)
	}
	return nil
}

// Exec creates the build HOME and enters the restored checkout after the
// container starts. It resolves command[0] using the developer image's PATH or
// explicit path, then replaces this process to preserve argv and exit status.
func Exec(source, home string, command []string) error {
	if len(command) == 0 || command[0] == "" {
		return fmt.Errorf("build command executable is required")
	}
	if source == "" || home == "" {
		return fmt.Errorf("source and home directories are required")
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return fmt.Errorf("resolve build home: %w", err)
	}
	if err := os.MkdirAll(home, 0755); err != nil {
		return fmt.Errorf("create build home: %w", err)
	}
	if err := os.Setenv("HOME", home); err != nil {
		return fmt.Errorf("set build home: %w", err)
	}
	if err := os.Chdir(source); err != nil {
		return fmt.Errorf("enter build source: %w", err)
	}
	executable, err := exec.LookPath(command[0])
	if err != nil {
		return fmt.Errorf("find build executable: %w", err)
	}
	if err := syscall.Exec(executable, command, os.Environ()); err != nil {
		return fmt.Errorf("execute build command: %w", err)
	}
	return nil
}
