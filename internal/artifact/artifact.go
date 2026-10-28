// Package artifact validates and stages build outputs for trusted handoffs.
package artifact

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Prepare checks the restored source and the executable script selected to run.
func Prepare(source, script string) error {
	if _, err := checkTree(source); err != nil {
		return fmt.Errorf("source validation failed: %w", err)
	}
	if err := safeRelative(script); err != nil {
		return err
	}
	path := filepath.Join(source, script)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("script: %w", err)
	}
	if !info.Mode().IsRegular() || syscall.Access(path, 1) != nil {
		return fmt.Errorf("script must be an executable regular file: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	var prefix [2]byte
	if _, err := io.ReadFull(file, prefix[:]); err != nil || string(prefix[:]) != "#!" {
		return fmt.Errorf("script must have an interpreter shebang: %s", path)
	}
	return nil
}

// StageOutput copies only validated out/ contents into the private export tree.
// Platform metadata comes from the trusted caller, never the source tree.
func StageOutput(source, destination, platform string) error {
	if err := realDirectory(source); err != nil {
		return err
	}
	output := filepath.Join(source, "out")
	if err := checkOutput(output); err != nil {
		return err
	}
	if platform != "" {
		if err := checkPlatform(platform); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(destination, 0755); err != nil {
		return err
	}
	if err := realDirectory(destination); err != nil {
		return err
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("export directory must be empty: %s", destination)
	}
	if err := copyTree(output, filepath.Join(destination, "out")); err != nil {
		return err
	}
	if platform != "" {
		return os.WriteFile(filepath.Join(destination, ".konflux-platform"), []byte(platform+"\n"), 0644)
	}
	return nil
}

// Pack stages one flat output or platform-named outputs and writes a reproducible
// gzip-compressed tar archive. All inputs are validated before staging begins.
func Pack(inputRoot, packageRoot, archivePath, layout string) error {
	if layout != "flat" && layout != "platform" {
		return fmt.Errorf("unknown layout: %s", layout)
	}
	if err := realDirectory(inputRoot); err != nil {
		return err
	}
	inputs, err := os.ReadDir(inputRoot)
	if err != nil {
		return err
	}
	if len(inputs) == 0 || (layout == "flat" && len(inputs) != 1) {
		return fmt.Errorf("flat layout requires one source; platform layout requires at least one")
	}
	type selection struct{ output, name string }
	selected := make([]selection, 0, len(inputs))
	destinations := make(map[string]bool)
	for _, input := range inputs {
		source := filepath.Join(inputRoot, input.Name())
		if err := realDirectory(source); err != nil {
			return err
		}
		output := filepath.Join(source, "out")
		if err := checkOutput(output); err != nil {
			return err
		}
		name := ""
		if layout == "platform" {
			marker := filepath.Join(source, ".konflux-platform")
			info, err := os.Lstat(marker)
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("missing trusted platform marker: %s", marker)
			}
			data, err := os.ReadFile(marker)
			if err != nil {
				return err
			}
			platform := strings.TrimSpace(string(data))
			if err := checkPlatform(platform); err != nil {
				return err
			}
			name = platformPunctuation.ReplaceAllString(platform, "-")
			if destinations[name] {
				return fmt.Errorf("duplicate platform directory: %s", name)
			}
		}
		destinations[name] = true
		selected = append(selected, selection{output, name})
	}
	if err := os.RemoveAll(packageRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(packageRoot), 0755); err != nil {
		return err
	}
	if layout == "platform" {
		if err := os.Mkdir(packageRoot, 0755); err != nil {
			return err
		}
	}
	for _, item := range selected {
		if err := copyTree(item.output, filepath.Join(packageRoot, item.name)); err != nil {
			return err
		}
	}
	return writeArchive(packageRoot, archivePath)
}

func safeRelative(value string) error {
	if value == "" || filepath.IsAbs(value) || slices.Contains(strings.Split(filepath.ToSlash(value), "/"), "..") {
		return fmt.Errorf("expected a relative path without '..': %q", value)
	}
	return nil
}

var (
	platformLabel       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]*$`)
	platformPunctuation = regexp.MustCompile(`[^A-Za-z0-9]`)
)

func checkPlatform(platform string) error {
	if !platformLabel.MatchString(platform) {
		return fmt.Errorf("invalid platform label: %q", platform)
	}
	return safeRelative(platform)
}

func realDirectory(path string) error {
	info, err := os.Lstat(filepath.Clean(path))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("expected a real directory: %s", path)
	}
	return nil
}

func checkOutput(path string) error {
	files, err := checkTree(path)
	if err != nil {
		return err
	}
	if files == 0 {
		return fmt.Errorf("output has no regular files: %s", path)
	}
	return nil
}

func checkTree(root string) (int, error) {
	if err := realDirectory(root); err != nil {
		return 0, err
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return 0, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return 0, err
	}
	files := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			return checkLink(root, relative)
		case entry.IsDir():
			return nil
		case entry.Type().IsRegular():
			files++
			return nil
		default:
			return fmt.Errorf("unsupported special file: %s", path)
		}
	})
	return files, err
}

// Resolve components without cleaning away '..' before following links. Missing
// targets are allowed, but even a dangling link must stay inside the artifact.
func checkLink(root, relative string) error {
	pending := strings.Split(relative, string(filepath.Separator))
	current := root
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if current == root {
				return fmt.Errorf("symlink escapes artifact: %s", relative)
			}
			current = filepath.Dir(current)
			continue
		}
		path := filepath.Join(current, part)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			current = path
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = path
			continue
		}
		links++
		if links > 255 {
			return fmt.Errorf("symlink cycle or excessive links: %s", relative)
		}
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if filepath.IsAbs(target) {
			return fmt.Errorf("absolute symlink in artifact: %s", path)
		}
		pending = append(strings.Split(target, string(filepath.Separator)), pending...)
	}
	return nil
}

func copyTree(source, destination string) error {
	type directory struct {
		path string
		mode os.FileMode
	}
	var directories []directory
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		switch {
		case info.IsDir():
			if err := os.Mkdir(target, info.Mode().Perm()|0700); err != nil {
				return err
			}
			directories = append(directories, directory{target, info.Mode()})
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode())
		default:
			return fmt.Errorf("unsupported special file: %s", path)
		}
	})
	if err != nil {
		return err
	}
	for _, dir := range slices.Backward(directories) {
		if err := os.Chmod(dir.path, dir.mode); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer output.Close()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Chmod(mode); err != nil {
		return err
	}
	return output.Close()
}

func writeArchive(root, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".payload-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	var paths []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && path != root {
			paths = append(paths, path)
		}
		return err
	}); err != nil {
		return err
	}
	slices.Sort(paths)
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		header.Uid, header.Gid = 0, 0
		header.Uname, header.Gname = "", ""
		header.ModTime = time.Unix(0, 0)
		header.AccessTime, header.ChangeTime = time.Time{}, time.Time{}
		header.Format = tar.FormatPAX
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			if err := archiveFile(archive, path); err != nil {
				return err
			}
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func archiveFile(archive *tar.Writer, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(archive, file)
	return err
}
