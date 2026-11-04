package artifact

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type directoryMode struct {
	path string
	mode os.FileMode
}

// CheckDestination requires path to be an empty directory and not a symlink.
func CheckDestination(path string) error {
	if err := realDirectory(path); err != nil {
		return err
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}

	if len(entries) != 0 {
		return fmt.Errorf("destination must be empty: %s", path)
	}

	return nil
}

// ExtractArchive extracts a gzip-compressed tar into an empty destination.
// Files from a flat archive land in that directory. Directory prefixes,
// including platform directories, are preserved. A failed extract removes
// anything it already wrote.
func ExtractArchive(archivePath, destination string) error {
	if err := CheckDestination(destination); err != nil {
		return err
	}

	if err := extractGzipTar(archivePath, destination); err != nil {
		if removeErr := removeChildren(destination); removeErr != nil {
			return fmt.Errorf("%w (cleanup: %v)", err, removeErr)
		}

		return err
	}

	return nil
}

// extractGzipTar writes one validated archive into destination.
func extractGzipTar(archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}

	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}

	defer compressed.Close()
	archive := tar.NewReader(compressed)
	files := 0
	var directories []directoryMode
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}

		relative, err := archiveRelative(header.Name)
		if err != nil {
			return err
		}

		mode := os.FileMode(header.Mode) & os.ModePerm
		target := pathInside(destination, relative)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := addDirectory(destination, relative, mode, &directories); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := ensureParents(destination, relative); err != nil {
				return err
			}

			if err := createFile(archive, target, mode); err != nil {
				return err
			}

			files++
		case tar.TypeSymlink:
			if err := ensureParents(destination, relative); err != nil {
				return err
			}

			if symlinkEscapes(destination, target, header.Linkname) {
				return fmt.Errorf("symlink escapes destination: %s -> %s", relative, header.Linkname)
			}

			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported archive entry %q", header.Name)
		}
	}

	if err := applyDirectoryModes(directories); err != nil {
		return err
	}

	if files == 0 {
		return fmt.Errorf("archive contains no regular files")
	}

	return nil
}

// archiveRelative returns a slash-separated path that stays inside the archive.
func archiveRelative(name string) (string, error) {
	slash := filepath.ToSlash(name)
	if slash == "" || strings.HasPrefix(slash, "/") {
		return "", fmt.Errorf("invalid archive path %q", name)
	}

	for strings.HasPrefix(slash, "./") {
		slash = strings.TrimPrefix(slash, "./")
	}

	slash = strings.TrimSuffix(slash, "/")
	if slash == "" || slash == "." {
		return "", fmt.Errorf("invalid archive path %q", name)
	}

	for _, part := range strings.Split(slash, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid archive path %q", name)
		}
	}

	return slash, nil
}

// pathInside joins a validated relative archive path onto destination.
func pathInside(destination, relative string) string {
	current := destination
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
	}

	return current
}

// ensureParents creates real parent directories and refuses to enter a symlink.
func ensureParents(destination, relative string) error {
	parts := strings.Split(relative, "/")
	current := destination
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0755); err != nil {
				return err
			}

			continue
		}

		if err != nil {
			return err
		}

		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("archive path %q enters a non-directory", relative)
		}
	}

	return nil
}

// addDirectory creates one archive directory and remembers its final mode.
func addDirectory(destination, relative string, mode os.FileMode, modes *[]directoryMode) error {
	if err := ensureParents(destination, relative); err != nil {
		return err
	}

	return createDirectory(pathInside(destination, relative), mode, modes)
}

// createDirectory creates a real directory or accepts one created for a child.
func createDirectory(path string, mode os.FileMode, modes *[]directoryMode) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.Mkdir(path, 0700); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("archive directory conflicts with an existing entry: %s", path)
	}

	*modes = append(*modes, directoryMode{path, mode})
	return nil
}

// applyDirectoryModes restores directory permissions after children are written.
func applyDirectoryModes(modes []directoryMode) error {
	for _, dir := range modes {
		if err := os.Chmod(dir.path, dir.mode); err != nil {
			return err
		}
	}

	return nil
}

// symlinkEscapes reports whether a link target leaves destination.
func symlinkEscapes(destination, linkPath, target string) bool {
	if target == "" || filepath.IsAbs(target) {
		return true
	}

	resolved := filepath.Clean(filepath.Join(filepath.Dir(linkPath), target))
	relative, err := filepath.Rel(destination, resolved)
	if err != nil {
		return true
	}

	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// createFile writes one regular file without following an existing symlink.
func createFile(archive *tar.Reader, path string, mode os.FileMode) error {
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}

	defer output.Close()
	if _, err := io.Copy(output, archive); err != nil {
		return err
	}

	if err := output.Chmod(mode); err != nil {
		return err
	}

	return output.Close()
}

// removeChildren deletes entries written by a failed extract.
func removeChildren(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}

	return nil
}
