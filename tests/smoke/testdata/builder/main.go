// This native executable verifies build commands in an image containing no shell.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// main validates the shell-free environment and writes the received argv as an output.
func main() {
	if err := build(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// build checks argv, writable HOME, launcher protection, and output creation without external tools.
func build() error {
	for _, path := range []string{"/bin/sh", "/bin/bash"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			return fmt.Errorf("shell unexpectedly present at %s: %v", path, err)
		}
	}
	data, err := os.ReadFile("expected-arguments.json")
	if err != nil {
		return err
	}
	var want []string
	if err := json.Unmarshal(data, &want); err != nil {
		return err
	}
	if !slices.Equal(os.Args[1:], want) {
		return fmt.Errorf("received argv %q; expected %q", os.Args[1:], want)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "writable"), []byte("home"), 0600); err != nil {
		return err
	}
	if _, err := os.Stat("/var/build-tools/byob"); err == nil {
		file, err := os.OpenFile("/var/build-tools/byob", os.O_WRONLY, 0)
		if err == nil {
			file.Close()
			return fmt.Errorf("launcher is writable by the developer")
		}
	}
	if err := os.RemoveAll("out"); err != nil {
		return err
	}
	if err := os.Mkdir("out", 0755); err != nil {
		return err
	}
	return os.WriteFile("out/arguments.json", data, 0644)
}
