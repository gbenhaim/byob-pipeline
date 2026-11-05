package artifact

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type tarMember struct {
	name     string
	body     string
	mode     int64
	typeflag byte
	link     string
}

// writeGzipTar builds a gzip-compressed tar fixture and returns its path.
func writeGzipTar(members []tarMember) string {
	path := filepath.Join(GinkgoT().TempDir(), "payload.tar.gz")
	file, err := os.Create(path)
	Expect(err).NotTo(HaveOccurred())

	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	for _, member := range members {
		header := &tar.Header{
			Name:     member.name,
			Mode:     member.mode,
			Size:     int64(len(member.body)),
			Typeflag: member.typeflag,
			Linkname: member.link,
		}
		if member.typeflag != tar.TypeReg && member.typeflag != tar.TypeRegA {
			header.Size = 0
		}

		Expect(archive.WriteHeader(header)).To(Succeed())
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			_, err := archive.Write([]byte(member.body))
			Expect(err).NotTo(HaveOccurred())
		}
	}

	Expect(archive.Close()).To(Succeed())
	Expect(compressed.Close()).To(Succeed())
	Expect(file.Close()).To(Succeed())
	return path
}

// emptyDestination returns a new empty directory for extraction.
func emptyDestination() string {
	path := filepath.Join(GinkgoT().TempDir(), "dest")
	Expect(os.Mkdir(path, 0755)).To(Succeed())
	return path
}

var _ = Describe("ExtractArchive", func() {
	It("restores a flat archive as files in the destination", func() {
		archive := writeGzipTar([]tarMember{{
			name: "hello.txt", body: "hello\n", mode: 0644, typeflag: tar.TypeReg,
		}})
		destination := emptyDestination()

		Expect(ExtractArchive(archive, destination)).To(Succeed())
		Expect(string(readFile(filepath.Join(destination, "hello.txt")))).To(Equal("hello\n"))
	})

	It("keeps platform directories from the archive", func() {
		archive := writeGzipTar([]tarMember{
			{name: "linux-amd64/hello.txt", body: "amd\n", mode: 0644, typeflag: tar.TypeReg},
			{name: "linux-arm64/hello.txt", body: "arm\n", mode: 0644, typeflag: tar.TypeReg},
		})
		destination := emptyDestination()

		Expect(ExtractArchive(archive, destination)).To(Succeed())
		Expect(string(readFile(filepath.Join(destination, "linux-amd64", "hello.txt")))).To(Equal("amd\n"))
		Expect(string(readFile(filepath.Join(destination, "linux-arm64", "hello.txt")))).To(Equal("arm\n"))
	})

	It("restores modes and relative links from a packed archive", func() {
		root := GinkgoT().TempDir()
		source := inputTree(root, "0", "", "hello")
		writeFile(filepath.Join(source, "out", "bin", "tool"), "tool", 0751)
		link("hello", filepath.Join(source, "out", "link"))
		link("../hello", filepath.Join(source, "out", "bin", "link"))
		Expect(pack(root, "flat")).To(Succeed())
		destination := filepath.Join(root, "unpacked")
		Expect(os.Mkdir(destination, 0755)).To(Succeed())

		Expect(ExtractArchive(filepath.Join(root, "payload.tar.gz"), destination)).To(Succeed())
		Expect(string(readFile(filepath.Join(destination, "hello")))).To(Equal("hello"))

		info, err := os.Lstat(filepath.Join(destination, "bin", "tool"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0751)))

		target, err := os.Readlink(filepath.Join(destination, "link"))
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal("hello"))
		target, err = os.Readlink(filepath.Join(destination, "bin", "link"))
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal("../hello"))
	})

	It("keeps an existing destination unchanged when it is not empty", func() {
		destination := emptyDestination()
		writeFile(filepath.Join(destination, "existing"), "keep", 0644)
		archive := writeGzipTar([]tarMember{{
			name: "hello.txt", body: "hello\n", mode: 0644, typeflag: tar.TypeReg,
		}})

		err := ExtractArchive(archive, destination)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("empty"))
		Expect(string(readFile(filepath.Join(destination, "existing")))).To(Equal("keep"))
		_, statErr := os.Lstat(filepath.Join(destination, "hello.txt"))
		Expect(os.IsNotExist(statErr)).To(BeTrue())
	})

	It("rejects a symlink destination without writing into its target", func() {
		parent := GinkgoT().TempDir()
		real := filepath.Join(parent, "real")
		Expect(os.Mkdir(real, 0755)).To(Succeed())
		destination := filepath.Join(parent, "link")
		Expect(os.Symlink(real, destination)).To(Succeed())
		archive := writeGzipTar([]tarMember{{
			name: "hello.txt", body: "hello\n", mode: 0644, typeflag: tar.TypeReg,
		}})

		Expect(ExtractArchive(archive, destination)).To(HaveOccurred())
		entries, err := os.ReadDir(real)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty())
	})

	DescribeTable("rejects an unsafe archive and leaves the destination empty",
		func(members []tarMember, message string) {
			parent := GinkgoT().TempDir()
			destination := filepath.Join(parent, "dest")
			Expect(os.Mkdir(destination, 0755)).To(Succeed())

			err := ExtractArchive(writeGzipTar(members), destination)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(message))

			_, statErr := os.Lstat(filepath.Join(parent, "outside.txt"))
			Expect(os.IsNotExist(statErr)).To(BeTrue())
			entries, readErr := os.ReadDir(destination)
			Expect(readErr).NotTo(HaveOccurred())
			Expect(entries).To(BeEmpty())
		},
		Entry("parent path", []tarMember{
			{name: "hello.txt", body: "hello\n", mode: 0644, typeflag: tar.TypeReg},
			{name: "../outside.txt", body: "nope", mode: 0644, typeflag: tar.TypeReg},
		}, "invalid archive path"),
		Entry("absolute path", []tarMember{
			{name: "/tmp/outside.txt", body: "nope", mode: 0644, typeflag: tar.TypeReg},
		}, "invalid archive path"),
		Entry("absolute symlink", []tarMember{
			{name: "link", mode: 0777, typeflag: tar.TypeSymlink, link: "/tmp/outside.txt"},
		}, "symlink escapes"),
		Entry("escaping symlink", []tarMember{
			{name: "bin/link", mode: 0777, typeflag: tar.TypeSymlink, link: "../../outside.txt"},
		}, "symlink escapes"),
		Entry("path through a symlink", []tarMember{
			{name: "alias", mode: 0777, typeflag: tar.TypeSymlink, link: "real"},
			{name: "alias/secret", body: "nope", mode: 0644, typeflag: tar.TypeReg},
		}, "non-directory"),
		Entry("hard link", []tarMember{
			{name: "hello.txt", body: "hello\n", mode: 0644, typeflag: tar.TypeReg},
			{name: "hard", mode: 0644, typeflag: tar.TypeLink, link: "hello.txt"},
		}, "unsupported archive entry"),
		Entry("fifo", []tarMember{
			{name: "fifo", mode: 0644, typeflag: tar.TypeFifo},
		}, "unsupported archive entry"),
		Entry("directory only", []tarMember{
			{name: "linux-amd64", mode: 0755, typeflag: tar.TypeDir},
		}, "no regular files"),
	)
})
