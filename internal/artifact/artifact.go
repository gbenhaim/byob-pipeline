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
	"time"
)

// Prepare validates the restored source tree before it enters a developer image.
// The build command is resolved later inside that image, where it will execute.
func Prepare(source string) error {
	if _, err := checkTree(source); err != nil {
		return fmt.Errorf("source validation failed: %w", err)
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

	selected, err := selectPackageInputs(inputRoot, layout)
	if err != nil {
		return err
	}

	if err := stagePackage(selected, packageRoot, layout); err != nil {
		return err
	}

	return writeArchive(packageRoot, archivePath)
}

type packageSelection struct {
	output string
	name   string
}

// selectPackageInputs validates each indexed output before package staging begins.
func selectPackageInputs(inputRoot, layout string) ([]packageSelection, error) {
	if err := realDirectory(inputRoot); err != nil {
		return nil, err
	}

	inputs, err := os.ReadDir(inputRoot)
	if err != nil {
		return nil, err
	}

	if len(inputs) == 0 || (layout == "flat" && len(inputs) != 1) {
		return nil, fmt.Errorf("flat layout requires one source; platform layout requires at least one")
	}

	selected := make([]packageSelection, 0, len(inputs))
	destinations := make(map[string]bool)
	for _, input := range inputs {
		source := filepath.Join(inputRoot, input.Name())
		if err := realDirectory(source); err != nil {
			return nil, err
		}

		output := filepath.Join(source, "out")
		if err := checkOutput(output); err != nil {
			return nil, err
		}

		name := ""
		if layout == "platform" {
			name, err = packagePlatformName(source, destinations)
			if err != nil {
				return nil, err
			}
		}

		destinations[name] = true
		selected = append(selected, packageSelection{output: output, name: name})
	}

	return selected, nil
}

// packagePlatformName reads and validates the trusted platform marker for one input.
func packagePlatformName(source string, destinations map[string]bool) (string, error) {
	marker := filepath.Join(source, ".konflux-platform")
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("missing trusted platform marker: %s", marker)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		return "", err
	}

	platform := strings.TrimSpace(string(data))
	if err := checkPlatform(platform); err != nil {
		return "", err
	}

	name := platformPunctuation.ReplaceAllString(platform, "-")
	if destinations[name] {
		return "", fmt.Errorf("duplicate platform directory: %s", name)
	}

	return name, nil
}

// stagePackage copies selected outputs into the requested flat or platform layout.
func stagePackage(selected []packageSelection, packageRoot, layout string) error {
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

	return nil
}

// safeRelative requires a nonempty relative path with no parent components.
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

// checkPlatform validates labels before they become platform directory names.
func checkPlatform(platform string) error {
	if !platformLabel.MatchString(platform) {
		return fmt.Errorf("invalid platform label: %q", platform)
	}

	return safeRelative(platform)
}

// realDirectory requires the cleaned path to name a directory, not a symlink.
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

// checkOutput validates an output tree and requires at least one regular file.
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

// checkTree counts regular files while rejecting special files and unsafe links.
// The root must be a real directory; links within it must stay inside the tree.
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

// checkLink resolves components without cleaning away '..' before following links.
// Missing targets are allowed, but dangling links must stay inside the artifact.
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

// copyTree copies a validated tree, preserving file modes and symlink targets.
// Directory modes are restored after copying so read-only trees remain writable
// during construction.
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

// copyFile creates a new destination with the source contents and requested mode.
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

// writeArchive atomically publishes a gzip-compressed tar of the staged tree.
// Sorted entries and normalized owner and time metadata make it reproducible.
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
	if err := writeArchiveEntries(root, archive); err != nil {
		return err
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

// writeArchiveEntries serializes sorted files from root with normalized metadata.
func writeArchiveEntries(root string, archive *tar.Writer) error {
	paths, err := archivePaths(root)
	if err != nil {
		return err
	}

	for _, path := range paths {
		if err := writeArchiveEntry(root, path, archive); err != nil {
			return err
		}
	}

	return nil
}

// archivePaths returns every non-root path in deterministic lexical order.
func archivePaths(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && path != root {
			paths = append(paths, path)
		}

		return err
	})
	if err != nil {
		return nil, err
	}

	slices.Sort(paths)
	return paths, nil
}

// writeArchiveEntry writes one tar header and, for regular files, its contents.
func writeArchiveEntry(root, path string, archive *tar.Writer) error {
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
		return archiveFile(archive, path)
	}

	return nil
}

// archiveFile writes a regular file's contents after its tar header is emitted.
func archiveFile(archive *tar.Writer, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}

	defer file.Close()
	_, err = io.Copy(archive, file)
	return err
}
