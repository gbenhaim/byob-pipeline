package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func sourceTree(t *testing.T) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	writeFile(t, filepath.Join(source, "build.sh"), "#!/bin/sh\nprintf hello\n", 0755)
	return source
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPrepareAcceptsScriptWithSpacesAndInternalSymlink(t *testing.T) {
	source := sourceTree(t)
	link(t, "build.sh", filepath.Join(source, "build script.sh"))
	if err := Prepare(source, "./build script.sh"); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRejectsInvalidScript(t *testing.T) {
	for _, script := range []string{"/bin/sh", "../build.sh", "foo/../build.sh", "missing", "", "."} {
		t.Run(script, func(t *testing.T) {
			if err := Prepare(sourceTree(t), script); err == nil {
				t.Fatalf("accepted invalid script %q", script)
			}
		})
	}
	for _, tc := range []struct {
		name, contents string
		mode           os.FileMode
	}{
		{"not executable", "#!/bin/sh\n", 0644},
		{"missing shebang", "echo hello\n", 0755},
		{"empty script", "", 0755},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := sourceTree(t)
			writeFile(t, filepath.Join(source, "build.sh"), tc.contents, tc.mode)
			if err := Prepare(source, "build.sh"); err == nil {
				t.Fatalf("accepted %s", tc.name)
			}
		})
	}
}

func TestPrepareRequiresExecutePermissionForCurrentUser(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root may execute a file with any executable bit")
	}
	source := sourceTree(t)
	writeFile(t, filepath.Join(source, "build.sh"), "#!/bin/sh\n", 0610)
	if err := Prepare(source, "build.sh"); err == nil {
		t.Fatal("accepted script whose owner lacks execute permission")
	}
}

func TestPrepareRejectsSymlinkRootWithTrailingSlash(t *testing.T) {
	source := sourceTree(t)
	link(t, source, source+"-link")
	if err := Prepare(source+"-link/", "build.sh"); err == nil {
		t.Fatal("accepted a symlink root with a trailing slash")
	}
}

func TestPrepareRejectsUnsafeSourceTrees(t *testing.T) {
	for _, kind := range []string{"sibling", "absolute", "cycle", "fifo", "symlink root"} {
		t.Run(kind, func(t *testing.T) {
			source := sourceTree(t)
			switch kind {
			case "sibling":
				sibling := source + "-evil"
				writeFile(t, filepath.Join(sibling, "outside"), "outside", 0644)
				link(t, "../source-evil", filepath.Join(source, "escape"))
			case "absolute":
				link(t, filepath.Join(source, "build.sh"), filepath.Join(source, "absolute"))
			case "cycle":
				link(t, "b", filepath.Join(source, "a"))
				link(t, "a", filepath.Join(source, "b"))
			case "fifo":
				if err := syscall.Mkfifo(filepath.Join(source, "fifo"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink root":
				link(t, source, source+"-link")
				source += "-link"
			}
			if err := Prepare(source, "build.sh"); err == nil {
				t.Fatalf("accepted %s", kind)
			}
		})
	}
}

func TestStageOutputExportsOnlyDeliverablesAndPreservesModesAndLinks(t *testing.T) {
	source := sourceTree(t)
	writeFile(t, filepath.Join(source, "out", "bin", "hello"), "deliverable", 0751)
	if err := os.Chmod(filepath.Join(source, "out", "bin"), 0750); err != nil {
		t.Fatal(err)
	}
	link(t, "bin/hello", filepath.Join(source, "out", "link"))
	link(t, "missing", filepath.Join(source, "out", "dangling"))
	writeFile(t, filepath.Join(source, "changed-source"), "must not be exported", 0644)
	writeFile(t, filepath.Join(source, ".skip-trusted-artifacts"), "", 0644)
	if err := syscall.Mkfifo(filepath.Join(source, "build-cache-fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	link(t, "/outside", filepath.Join(source, "build-cache-link"))
	destination := t.TempDir()
	if err := StageOutput(source, destination, ""); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 1 || entries[0].Name() != "out" {
		t.Fatalf("stage entries = %v, error = %v", entries, err)
	}
	if got := string(readFile(t, filepath.Join(destination, "out", "bin", "hello"))); got != "deliverable" {
		t.Fatalf("copied content = %q", got)
	}
	for name, want := range map[string]os.FileMode{"bin": 0750, "bin/hello": 0751} {
		info, err := os.Stat(filepath.Join(destination, "out", name))
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("mode for %s = %v, error = %v", name, info, err)
		}
	}
	for name, want := range map[string]string{"link": "bin/hello", "dangling": "missing"} {
		got, err := os.Readlink(filepath.Join(destination, "out", name))
		if err != nil || got != want {
			t.Fatalf("link %s = %q, %v", name, got, err)
		}
	}
}

func TestStageOutputRejectsInvalidOutputBeforeCopying(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "directories only", "links only", "fifo", "escape", "absolute", "cycle", "output symlink", "source symlink"} {
		t.Run(kind, func(t *testing.T) {
			source := sourceTree(t)
			output := filepath.Join(source, "out")
			if kind != "missing" {
				if err := os.Mkdir(output, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "missing" && kind != "empty" && kind != "directories only" && kind != "links only" {
				writeFile(t, filepath.Join(output, "hello"), "hello", 0644)
			}
			switch kind {
			case "directories only":
				if err := os.Mkdir(filepath.Join(output, "empty"), 0755); err != nil {
					t.Fatal(err)
				}
			case "links only":
				link(t, "missing", filepath.Join(output, "link"))
			case "fifo":
				if err := syscall.Mkfifo(filepath.Join(output, "fifo"), 0600); err != nil {
					t.Fatal(err)
				}
			case "escape":
				link(t, "../build.sh", filepath.Join(output, "escape"))
			case "absolute":
				link(t, filepath.Join(output, "hello"), filepath.Join(output, "link"))
			case "cycle":
				link(t, "link", filepath.Join(output, "link"))
			case "output symlink":
				if err := os.Rename(output, filepath.Join(source, "other")); err != nil {
					t.Fatal(err)
				}
				link(t, "other", output)
			case "source symlink":
				link(t, source, source+"-link")
				source += "-link"
			}
			destination := t.TempDir()
			if err := StageOutput(source, destination, ""); err == nil {
				t.Fatalf("accepted %s", kind)
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid output was staged: %v, %v", entries, err)
			}
		})
	}
}

func TestStageOutputWritesTrustedPlatformOutsideUserOutput(t *testing.T) {
	source := sourceTree(t)
	writeFile(t, filepath.Join(source, "out", "hello"), "deliverable", 0644)
	writeFile(t, filepath.Join(source, ".konflux-platform"), "forged", 0644)
	destination := t.TempDir()
	if err := StageOutput(source, destination, "linux/arm64"); err != nil {
		t.Fatal(err)
	}
	if got := string(readFile(t, filepath.Join(destination, ".konflux-platform"))); got != "linux/arm64\n" {
		t.Fatalf("platform = %q", got)
	}
	if got := string(readFile(t, filepath.Join(source, ".konflux-platform"))); got != "forged" {
		t.Fatalf("source platform changed to %q", got)
	}
}

func TestStageOutputRejectsInvalidPlatform(t *testing.T) {
	for _, platform := range []string{"/linux/amd64", "../amd64", "linux/../amd64", "linux arm64", "linux\narm64", ".", "-linux"} {
		t.Run(platform, func(t *testing.T) {
			source := sourceTree(t)
			writeFile(t, filepath.Join(source, "out", "hello"), "hello", 0644)
			destination := t.TempDir()
			if err := StageOutput(source, destination, platform); err == nil {
				t.Fatalf("accepted platform %q", platform)
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid platform staged output: %v, %v", entries, err)
			}
		})
	}
}

func inputTree(t *testing.T, root, index, platform, contents string) string {
	t.Helper()
	source := filepath.Join(root, "inputs", index)
	writeFile(t, filepath.Join(source, "out", "hello"), contents, 0644)
	writeFile(t, filepath.Join(source, ".konflux-platform"), platform+"\n", 0644)
	return source
}

func pack(t *testing.T, root, layout string) error {
	t.Helper()
	return Pack(filepath.Join(root, "inputs"), filepath.Join(root, "package"), filepath.Join(root, "payload.tar.gz"), layout)
}

type archiveEntry struct {
	header tar.Header
	data   string
}

func archiveEntries(t *testing.T, path string) []archiveEntry {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(readFile(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if !reader.ModTime.IsZero() || reader.Name != "" {
		t.Fatalf("nondeterministic gzip header: %v", reader.Header)
	}
	var entries []archiveEntry
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
		if header.Uid != 0 || header.Gid != 0 || header.ModTime.Unix() != 0 || header.Uname != "" || header.Gname != "" {
			t.Fatalf("nondeterministic tar metadata: %+v", header)
		}
		entries = append(entries, archiveEntry{header: *header, data: string(contents)})
	}
	return entries
}

func TestPackMapsPlatformsWithoutAssumingInputOrder(t *testing.T) {
	root := t.TempDir()
	inputTree(t, root, "0", "linux/arm64", "arm")
	inputTree(t, root, "1", "linux/amd64", "amd")
	if err := pack(t, root, "platform"); err != nil {
		t.Fatal(err)
	}
	entries := archiveEntries(t, filepath.Join(root, "payload.tar.gz"))
	var names []string
	contents := map[string]string{}
	for _, entry := range entries {
		names = append(names, strings.TrimSuffix(entry.header.Name, "/"))
		contents[entry.header.Name] = entry.data
	}
	if want := []string{"linux-amd64", "linux-amd64/hello", "linux-arm64", "linux-arm64/hello"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("archive names = %v, want %v", names, want)
	}
	if contents["linux-arm64/hello"] != "arm" || contents["linux-amd64/hello"] != "amd" {
		t.Fatalf("platform contents = %v", contents)
	}
}

func TestPackFlatPreservesLinksModesAndProducesDeterministicArchive(t *testing.T) {
	root := t.TempDir()
	source := inputTree(t, root, "0", "", "hello")
	writeFile(t, filepath.Join(source, "out", "bin", "tool"), "tool", 0751)
	link(t, "hello", filepath.Join(source, "out", "link"))
	link(t, "../hello", filepath.Join(source, "out", "bin", "link"))
	link(t, "bin", filepath.Join(source, "out", "dir-link"))
	link(t, "missing", filepath.Join(source, "out", "dangling"))
	if err := pack(t, root, "flat"); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "payload.tar.gz")
	first := readFile(t, archive)
	old := time.Unix(123, 0)
	if err := os.Chtimes(filepath.Join(source, "out", "hello"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := pack(t, root, "flat"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, readFile(t, archive)) {
		t.Fatal("archive changed when only input timestamps changed")
	}
	var names []string
	links := map[string]string{}
	for _, entry := range archiveEntries(t, archive) {
		names = append(names, strings.TrimSuffix(entry.header.Name, "/"))
		if entry.header.Typeflag == tar.TypeSymlink {
			links[entry.header.Name] = entry.header.Linkname
		}
		if entry.header.Name == "bin/tool" && entry.header.Mode != 0751 {
			t.Fatalf("executable mode = %o", entry.header.Mode)
		}
	}
	if want := []string{"bin", "bin/link", "bin/tool", "dangling", "dir-link", "hello", "link"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("archive names = %v, want %v", names, want)
	}
	if want := map[string]string{"bin/link": "../hello", "dangling": "missing", "dir-link": "bin", "link": "hello"}; !reflect.DeepEqual(links, want) {
		t.Fatalf("archive links = %v, want %v", links, want)
	}
}

func TestPackRejectsInvalidInputWithoutCreatingArchive(t *testing.T) {
	for _, kind := range []string{"empty inputs", "empty output", "escape", "absolute", "cycle", "fifo", "source symlink", "output symlink", "input root symlink", "multiple flat", "unknown layout", "duplicate platforms", "missing marker", "symlink marker", "invalid platform"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			source := inputTree(t, root, "0", "linux/arm64", "hello")
			output := filepath.Join(source, "out")
			layout := "flat"
			switch kind {
			case "empty inputs":
				if err := os.RemoveAll(source); err != nil {
					t.Fatal(err)
				}
			case "empty output":
				if err := os.Remove(filepath.Join(output, "hello")); err != nil {
					t.Fatal(err)
				}
			case "escape":
				link(t, "../../outside", filepath.Join(output, "escape"))
			case "absolute":
				link(t, filepath.Join(output, "hello"), filepath.Join(output, "link"))
			case "cycle":
				link(t, "link", filepath.Join(output, "link"))
			case "fifo":
				if err := syscall.Mkfifo(filepath.Join(output, "fifo"), 0600); err != nil {
					t.Fatal(err)
				}
			case "source symlink", "output symlink", "input root symlink":
				path := source
				if kind == "output symlink" {
					path = output
				} else if kind == "input root symlink" {
					path = filepath.Dir(source)
				}
				moved := filepath.Join(root, "moved")
				if err := os.Rename(path, moved); err != nil {
					t.Fatal(err)
				}
				link(t, moved, path)
			case "multiple flat":
				inputTree(t, root, "1", "linux/amd64", "second")
			case "unknown layout":
				layout = "unknown"
			case "duplicate platforms":
				layout = "platform"
				inputTree(t, root, "1", "linux-arm64", "second")
			case "missing marker", "symlink marker", "invalid platform":
				layout = "platform"
				marker := filepath.Join(source, ".konflux-platform")
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink marker" {
					writeFile(t, filepath.Join(source, "marker"), "linux/arm64\n", 0644)
					link(t, "marker", marker)
				} else if kind == "invalid platform" {
					writeFile(t, marker, "linux/../arm64", 0644)
				}
			}
			if err := pack(t, root, layout); err == nil {
				t.Fatalf("accepted %s", kind)
			}
			if _, err := os.Lstat(filepath.Join(root, "payload.tar.gz")); !os.IsNotExist(err) {
				t.Fatalf("archive exists after invalid input: %v", err)
			}
		})
	}
}
