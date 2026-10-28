package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// writeFile creates a fixture with parent directories and an exact permission mode.
func writeFile(path, contents string, mode os.FileMode) {
	Expect(os.MkdirAll(filepath.Dir(path), 0755)).To(Succeed())
	Expect(os.WriteFile(path, []byte(contents), mode)).To(Succeed())
	Expect(os.Chmod(path, mode)).To(Succeed())
}

// link creates a symbolic link fixture.
func link(target, path string) {
	Expect(os.Symlink(target, path)).To(Succeed())
}

// sourceTree creates a minimal checkout containing an executable build script.
func sourceTree() string {
	source := filepath.Join(GinkgoT().TempDir(), "source")
	writeFile(filepath.Join(source, "build.sh"), "#!/bin/sh\nprintf hello\n", 0755)
	return source
}

// readFile loads fixture contents and fails the current spec if reading fails.
func readFile(path string) []byte {
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	return data
}

// inputTree creates one indexed build input with output and a platform marker.
func inputTree(root, index, platform, contents string) string {
	source := filepath.Join(root, "inputs", index)
	writeFile(filepath.Join(source, "out", "hello"), contents, 0644)
	writeFile(filepath.Join(source, ".konflux-platform"), platform+"\n", 0644)
	return source
}

// pack packages fixture inputs into the conventional test archive and directory.
func pack(root, layout string) error {
	return Pack(filepath.Join(root, "inputs"), filepath.Join(root, "package"), filepath.Join(root, "payload.tar.gz"), layout)
}

type archiveEntry struct {
	header tar.Header
	data   string
}

// archiveEntries reads archive members and verifies deterministic gzip and tar metadata.
func archiveEntries(path string) []archiveEntry {
	reader, err := gzip.NewReader(bytes.NewReader(readFile(path)))
	Expect(err).NotTo(HaveOccurred())
	if err != nil {
		return nil
	}

	defer func() {
		Expect(reader.Close()).To(Succeed())
	}()

	Expect(reader.ModTime.IsZero()).To(BeTrue())
	Expect(reader.Name).To(BeEmpty())

	var entries []archiveEntry
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}

		Expect(err).NotTo(HaveOccurred())
		if err != nil {
			return entries
		}

		contents, err := io.ReadAll(archive)
		Expect(err).NotTo(HaveOccurred())
		if err != nil {
			return entries
		}

		Expect(header.Uid).To(Equal(0))
		Expect(header.Gid).To(Equal(0))
		Expect(header.ModTime.Unix()).To(Equal(int64(0)))
		Expect(header.Uname).To(BeEmpty())
		Expect(header.Gname).To(BeEmpty())

		entries = append(entries, archiveEntry{header: *header, data: string(contents)})
	}

	return entries
}

// TestArtifact runs the artifact package's Ginkgo specs.
func TestArtifact(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Artifact Suite")
}

var _ = Describe("Prepare", func() {
	// Prepare permits build-command resolution to happen inside the developer image.
	DescribeTable("accepts source trees without script requirements",
		func(contents string, mode os.FileMode) {
			source := GinkgoT().TempDir()
			if mode != 0 {
				writeFile(filepath.Join(source, "build.sh"), contents, mode)
			}

			Expect(Prepare(source)).To(Succeed())
		},
		Entry("empty source", "", os.FileMode(0)),
		Entry("non-executable file", "build configuration\n", os.FileMode(0644)),
		Entry("native executable", "\x7fELF\x00\x00", os.FileMode(0755)),
		Entry("empty executable", "", os.FileMode(0755)),
	)

	It("accepts internal symlinks and dangling links", func() {
		source := sourceTree()
		link("build.sh", filepath.Join(source, "build script.sh"))
		link("missing", filepath.Join(source, "dangling"))

		Expect(Prepare(source)).To(Succeed())
	})

	DescribeTable("requires a real source directory",
		func(kind string) {
			source := filepath.Join(GinkgoT().TempDir(), "source")
			if kind == "regular file" {
				writeFile(source, "source", 0644)
			}

			Expect(Prepare(source)).To(HaveOccurred())
		},
		Entry("missing directory", "missing"),
		Entry("regular file", "regular file"),
	)

	It("rejects a symlink root with a trailing slash", func() {
		source := sourceTree()
		link(source, source+"-link")

		Expect(Prepare(source + "-link/")).To(HaveOccurred())
	})

	DescribeTable("rejects unsafe source trees",
		func(kind string) {
			source := sourceTree()
			switch kind {
			case "sibling":
				sibling := source + "-evil"
				writeFile(filepath.Join(sibling, "outside"), "outside", 0644)
				link("../source-evil", filepath.Join(source, "escape"))
			case "absolute":
				link(filepath.Join(source, "build.sh"), filepath.Join(source, "absolute"))
			case "cycle":
				link("b", filepath.Join(source, "a"))
				link("a", filepath.Join(source, "b"))
			case "fifo":
				Expect(syscall.Mkfifo(filepath.Join(source, "fifo"), 0600)).To(Succeed())
			case "symlink root":
				link(source, source+"-link")
				source += "-link"
			}

			Expect(Prepare(source)).To(HaveOccurred())
		},
		Entry("sibling escape", "sibling"),
		Entry("absolute symlink", "absolute"),
		Entry("symlink cycle", "cycle"),
		Entry("FIFO", "fifo"),
		Entry("symlink root", "symlink root"),
	)
})

var _ = Describe("StageOutput", func() {
	It("exports only deliverables and preserves modes and links", func() {
		source := sourceTree()
		writeFile(filepath.Join(source, "out", "bin", "hello"), "deliverable", 0751)
		Expect(os.Chmod(filepath.Join(source, "out", "bin"), 0750)).To(Succeed())

		link("bin/hello", filepath.Join(source, "out", "link"))
		link("missing", filepath.Join(source, "out", "dangling"))
		writeFile(filepath.Join(source, "changed-source"), "must not be exported", 0644)
		writeFile(filepath.Join(source, ".skip-trusted-artifacts"), "", 0644)
		Expect(syscall.Mkfifo(filepath.Join(source, "build-cache-fifo"), 0600)).To(Succeed())
		link("/outside", filepath.Join(source, "build-cache-link"))

		destination := GinkgoT().TempDir()
		Expect(StageOutput(source, destination, "")).To(Succeed())

		entries, err := os.ReadDir(destination)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Name()).To(Equal("out"))
		Expect(string(readFile(filepath.Join(destination, "out", "bin", "hello")))).To(Equal("deliverable"))

		for name, want := range map[string]os.FileMode{"bin": 0750, "bin/hello": 0751} {
			info, err := os.Stat(filepath.Join(destination, "out", name))
			Expect(err).NotTo(HaveOccurred())
			if err != nil {
				continue
			}

			Expect(info.Mode().Perm()).To(Equal(want), "mode for %s", name)
		}

		for name, want := range map[string]string{"link": "bin/hello", "dangling": "missing"} {
			got, err := os.Readlink(filepath.Join(destination, "out", name))
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want), "link %s", name)
		}
	})

	DescribeTable("rejects invalid output before copying",
		func(kind string) {
			source := sourceTree()
			output := filepath.Join(source, "out")
			if kind != "missing" {
				Expect(os.Mkdir(output, 0755)).To(Succeed())
			}

			if kind != "missing" && kind != "empty" && kind != "directories only" && kind != "links only" {
				writeFile(filepath.Join(output, "hello"), "hello", 0644)
			}

			switch kind {
			case "directories only":
				Expect(os.Mkdir(filepath.Join(output, "empty"), 0755)).To(Succeed())
			case "links only":
				link("missing", filepath.Join(output, "link"))
			case "fifo":
				Expect(syscall.Mkfifo(filepath.Join(output, "fifo"), 0600)).To(Succeed())
			case "escape":
				link("../build.sh", filepath.Join(output, "escape"))
			case "absolute":
				link(filepath.Join(output, "hello"), filepath.Join(output, "link"))
			case "cycle":
				link("link", filepath.Join(output, "link"))
			case "output symlink":
				Expect(os.Rename(output, filepath.Join(source, "other"))).To(Succeed())
				link("other", output)
			case "source symlink":
				link(source, source+"-link")
				source += "-link"
			}

			destination := GinkgoT().TempDir()
			Expect(StageOutput(source, destination, "")).To(HaveOccurred())

			entries, err := os.ReadDir(destination)
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(BeEmpty(), "invalid output was staged")
		},
		Entry("missing output", "missing"),
		Entry("empty output", "empty"),
		Entry("directories only", "directories only"),
		Entry("links only", "links only"),
		Entry("FIFO", "fifo"),
		Entry("escaping symlink", "escape"),
		Entry("absolute symlink", "absolute"),
		Entry("symlink cycle", "cycle"),
		Entry("output symlink", "output symlink"),
		Entry("source symlink", "source symlink"),
	)

	It("writes the trusted platform outside user output", func() {
		source := sourceTree()
		writeFile(filepath.Join(source, "out", "hello"), "deliverable", 0644)
		writeFile(filepath.Join(source, ".konflux-platform"), "forged", 0644)
		destination := GinkgoT().TempDir()

		Expect(StageOutput(source, destination, "linux/arm64")).To(Succeed())
		Expect(string(readFile(filepath.Join(destination, ".konflux-platform")))).To(Equal("linux/arm64\n"))
		Expect(string(readFile(filepath.Join(source, ".konflux-platform")))).To(Equal("forged"))
	})

	DescribeTable("rejects invalid platform labels before staging",
		func(platform string) {
			source := sourceTree()
			writeFile(filepath.Join(source, "out", "hello"), "hello", 0644)
			destination := GinkgoT().TempDir()

			Expect(StageOutput(source, destination, platform)).To(HaveOccurred())

			entries, err := os.ReadDir(destination)
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(BeEmpty(), "invalid platform staged output")
		},
		Entry("leading slash", "/linux/amd64"),
		Entry("parent directory", "../amd64"),
		Entry("embedded parent directory", "linux/../amd64"),
		Entry("space", "linux arm64"),
		Entry("newline", "linux\narm64"),
		Entry("dot", "."),
		Entry("leading dash", "-linux"),
	)
})

var _ = Describe("Pack", func() {
	It("maps platforms without assuming input order", func() {
		root := GinkgoT().TempDir()
		inputTree(root, "0", "linux/arm64", "arm")
		inputTree(root, "1", "linux/amd64", "amd")
		Expect(pack(root, "platform")).To(Succeed())

		entries := archiveEntries(filepath.Join(root, "payload.tar.gz"))
		var names []string
		contents := map[string]string{}
		for _, entry := range entries {
			names = append(names, strings.TrimSuffix(entry.header.Name, "/"))
			contents[entry.header.Name] = entry.data
		}

		Expect(names).To(Equal([]string{"linux-amd64", "linux-amd64/hello", "linux-arm64", "linux-arm64/hello"}))
		Expect(contents["linux-arm64/hello"]).To(Equal("arm"))
		Expect(contents["linux-amd64/hello"]).To(Equal("amd"))
	})

	It("preserves links and modes and produces a deterministic flat archive", func() {
		root := GinkgoT().TempDir()
		source := inputTree(root, "0", "", "hello")
		writeFile(filepath.Join(source, "out", "bin", "tool"), "tool", 0751)
		link("hello", filepath.Join(source, "out", "link"))
		link("../hello", filepath.Join(source, "out", "bin", "link"))
		link("bin", filepath.Join(source, "out", "dir-link"))
		link("missing", filepath.Join(source, "out", "dangling"))
		Expect(pack(root, "flat")).To(Succeed())

		archive := filepath.Join(root, "payload.tar.gz")
		first := readFile(archive)
		old := time.Unix(123, 0)
		Expect(os.Chtimes(filepath.Join(source, "out", "hello"), old, old)).To(Succeed())
		Expect(pack(root, "flat")).To(Succeed())
		Expect(readFile(archive)).To(Equal(first), "archive changed when only input timestamps changed")

		var names []string
		links := map[string]string{}
		for _, entry := range archiveEntries(archive) {
			names = append(names, strings.TrimSuffix(entry.header.Name, "/"))
			if entry.header.Typeflag == tar.TypeSymlink {
				links[entry.header.Name] = entry.header.Linkname
			}

			if entry.header.Name == "bin/tool" {
				Expect(entry.header.Mode).To(Equal(int64(0751)))
			}
		}

		Expect(names).To(Equal([]string{"bin", "bin/link", "bin/tool", "dangling", "dir-link", "hello", "link"}))
		Expect(links).To(Equal(map[string]string{
			"bin/link": "../hello",
			"dangling": "missing",
			"dir-link": "bin",
			"link":     "hello",
		}))
	})

	DescribeTable("rejects invalid input without creating an archive",
		func(kind string) {
			root := GinkgoT().TempDir()
			source := inputTree(root, "0", "linux/arm64", "hello")
			output := filepath.Join(source, "out")
			layout := "flat"
			switch kind {
			case "empty inputs":
				Expect(os.RemoveAll(source)).To(Succeed())
			case "empty output":
				Expect(os.Remove(filepath.Join(output, "hello"))).To(Succeed())
			case "escape":
				link("../../outside", filepath.Join(output, "escape"))
			case "absolute":
				link(filepath.Join(output, "hello"), filepath.Join(output, "link"))
			case "cycle":
				link("link", filepath.Join(output, "link"))
			case "fifo":
				Expect(syscall.Mkfifo(filepath.Join(output, "fifo"), 0600)).To(Succeed())
			case "source symlink", "output symlink", "input root symlink":
				path := source
				if kind == "output symlink" {
					path = output
				} else if kind == "input root symlink" {
					path = filepath.Dir(source)
				}

				moved := filepath.Join(root, "moved")
				Expect(os.Rename(path, moved)).To(Succeed())
				link(moved, path)
			case "multiple flat":
				inputTree(root, "1", "linux/amd64", "second")
			case "unknown layout":
				layout = "unknown"
			case "duplicate platforms":
				layout = "platform"
				inputTree(root, "1", "linux-arm64", "second")
			case "missing marker", "symlink marker", "invalid platform":
				layout = "platform"
				marker := filepath.Join(source, ".konflux-platform")
				Expect(os.Remove(marker)).To(Succeed())

				if kind == "symlink marker" {
					writeFile(filepath.Join(source, "marker"), "linux/arm64\n", 0644)
					link("marker", marker)
				} else if kind == "invalid platform" {
					writeFile(marker, "linux/../arm64", 0644)
				}
			}

			Expect(pack(root, layout)).To(HaveOccurred())

			_, err := os.Lstat(filepath.Join(root, "payload.tar.gz"))
			Expect(os.IsNotExist(err)).To(BeTrue(), "archive exists after invalid input: %v", err)
		},
		Entry("empty inputs", "empty inputs"),
		Entry("empty output", "empty output"),
		Entry("escaping symlink", "escape"),
		Entry("absolute symlink", "absolute"),
		Entry("symlink cycle", "cycle"),
		Entry("FIFO", "fifo"),
		Entry("source symlink", "source symlink"),
		Entry("output symlink", "output symlink"),
		Entry("input root symlink", "input root symlink"),
		Entry("multiple flat inputs", "multiple flat"),
		Entry("unknown layout", "unknown layout"),
		Entry("duplicate platforms", "duplicate platforms"),
		Entry("missing marker", "missing marker"),
		Entry("symlink marker", "symlink marker"),
		Entry("invalid platform", "invalid platform"),
	)
})
