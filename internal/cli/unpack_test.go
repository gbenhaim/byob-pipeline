package cli

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// writePayload writes a gzip-compressed tar and returns its path and digest.
func writePayload(members map[string]string) (string, string) {
	path := filepath.Join(GinkgoT().TempDir(), "payload.tar.gz")
	file, err := os.Create(path)
	Expect(err).NotTo(HaveOccurred())

	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	for name, body := range members {
		header := &tar.Header{
			Name:     name,
			Mode:     0644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		Expect(archive.WriteHeader(header)).To(Succeed())
		_, err := archive.Write([]byte(body))
		Expect(err).NotTo(HaveOccurred())
	}

	Expect(archive.Close()).To(Succeed())
	Expect(compressed.Close()).To(Succeed())
	Expect(file.Close()).To(Succeed())
	return path, fileDigest(path)
}

// fileDigest returns the sha256 digest of a fixture file.
func fileDigest(path string) string {
	sum := sha256.Sum256(mustReadFile(path))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// manifestDocument builds an OCI image manifest for one payload layer.
func manifestDocument(artifactType, mediaType, name, digest string, layers int) []byte {
	layer := map[string]any{
		"mediaType": mediaType,
		"digest":    digest,
		"size":      1,
		"annotations": map[string]string{
			"org.opencontainers.image.title": name,
		},
	}
	items := make([]any, 0, layers)
	for range layers {
		items = append(items, layer)
	}

	data, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"artifactType":  artifactType,
		"layers":        items,
	})
	Expect(err).NotTo(HaveOccurred())
	return data
}

// serveRegistry installs an oras fixture that records calls and serves one artifact.
func serveRegistry(manifest []byte, archive string) string {
	root := GinkgoT().TempDir()
	logPath := filepath.Join(root, "oras.log")
	manifestPath := filepath.Join(root, "manifest.json")
	Expect(os.WriteFile(manifestPath, manifest, 0644)).To(Succeed())
	fixtureCommand("oras", fmt.Sprintf(`printf 'env SSL_CERT_FILE=%%s\n' "${SSL_CERT_FILE-}" >> %q
printf 'env DOCKER_CONFIG=%%s\n' "${DOCKER_CONFIG-}" >> %q
printf '%%s\0' "$@" >> %q
printf '\n' >> %q
if [ "$1" = manifest ]; then
  cat %q
  exit 0
fi
if [ "$1" = blob ]; then
  out=""
  prev=""
  for arg in "$@"; do
    if [ "$prev" = --output ]; then
      out=$arg
    fi
    prev=$arg
  done
  cp %q "$out"
  exit 0
fi
exit 1
`, logPath, logPath, logPath, logPath, manifestPath, archive))
	return logPath
}

// orasCalls returns each recorded oras argv, ignoring environment lines.
func orasCalls(logPath string) [][]string {
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}

	Expect(err).NotTo(HaveOccurred())
	var calls [][]string
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "env ") {
			continue
		}

		args := strings.Split(line, "\x00")
		if len(args) > 0 && args[len(args)-1] == "" {
			args = args[:len(args)-1]
		}

		calls = append(calls, args)
	}

	return calls
}

// orasEnv returns a recorded environment value from the oras fixture log.
func orasEnv(logPath, key string) string {
	data, err := os.ReadFile(logPath)
	Expect(err).NotTo(HaveOccurred())
	prefix := "env " + key + "="
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}

	return ""
}

var _ = Describe("unpack", func() {
	const artifactType = "application/vnd.konflux.build-output.v1"
	const layerType = "application/vnd.oci.image.layer.v1.tar+gzip"
	manifestDigest := "sha256:" + strings.Repeat("a", 64)

	// imageReference is the manifest digest reference for the fixture registry.
	imageReference := func() string {
		return "localhost:5000/team/output@" + manifestDigest
	}

	It("extracts a flat payload and fetches the layer blob", func() {
		archive, layerDigest := writePayload(map[string]string{"hello.txt": "hello\n"})
		logPath := serveRegistry(manifestDocument(artifactType, layerType, "payload.tar.gz", layerDigest, 1), archive)
		root := GinkgoT().TempDir()
		destination := filepath.Join(root, "out")
		auth := filepath.Join(root, "auth")
		Expect(os.Mkdir(auth, 0755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(auth, "config.json"), []byte("{}"), 0644)).To(Succeed())
		ca := filepath.Join(root, "ca.crt")
		Expect(os.WriteFile(ca, []byte("custom-ca"), 0644)).To(Succeed())
		GinkgoT().Setenv("CA_FILE", ca)
		GinkgoT().Setenv("SSL_CERT_FILE", "/system/certs.pem")

		stdout, stderr, err := runCLIStreams(
			"unpack",
			"--image="+imageReference(),
			"--destination="+destination,
			"--auth-dir="+auth,
			"--plain-http",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(ContainSubstring("Unpacking " + imageReference() + " as " + artifactType + " into " + destination))
		Expect(stderr).To(ContainSubstring("Unpacked " + imageReference() + " into " + destination))
		Expect(string(mustReadFile(filepath.Join(destination, "hello.txt")))).To(Equal("hello\n"))

		calls := orasCalls(logPath)
		Expect(calls).To(HaveLen(2))
		Expect(calls[0]).To(ContainElements("manifest", "fetch", "--no-tty", "--plain-http", "--registry-config", filepath.Join(auth, "config.json"), imageReference()))
		Expect(calls[1]).To(ContainElements("blob", "fetch", "--no-tty", "--plain-http", "localhost:5000/team/output@"+layerDigest))
		Expect(calls[1]).NotTo(ContainElement(imageReference()))
		Expect(orasEnv(logPath, "SSL_CERT_FILE")).To(Equal(ca))
		Expect(orasEnv(logPath, "DOCKER_CONFIG")).To(Equal(auth))
	})

	It("keeps platform directories", func() {
		archive, layerDigest := writePayload(map[string]string{
			"linux-amd64/hello.txt": "amd\n",
			"linux-arm64/hello.txt": "arm\n",
		})
		logPath := serveRegistry(manifestDocument(artifactType, layerType, "payload.tar.gz", layerDigest, 1), archive)
		GinkgoT().Setenv("DOCKER_CONFIG", "")
		destination := filepath.Join(GinkgoT().TempDir(), "out")

		_, stderr, err := runCLIStreams(
			"unpack",
			"--image="+imageReference(),
			"--destination="+destination,
			"--auth-dir="+filepath.Join(GinkgoT().TempDir(), "no-auth"),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(stderr).To(ContainSubstring("Unpacked " + imageReference()))
		Expect(string(mustReadFile(filepath.Join(destination, "linux-amd64", "hello.txt")))).To(Equal("amd\n"))
		Expect(string(mustReadFile(filepath.Join(destination, "linux-arm64", "hello.txt")))).To(Equal("arm\n"))

		calls := orasCalls(logPath)
		Expect(calls[0]).NotTo(ContainElement("--plain-http"))
		Expect(calls[0]).NotTo(ContainElement("--registry-config"))
		Expect(orasEnv(logPath, "DOCKER_CONFIG")).To(BeEmpty())
	})

	DescribeTable("rejects a manifest that does not match the build-output format",
		func(artifact, media, name string, layers int, message string) {
			archive, digest := writePayload(map[string]string{"hello.txt": "hello\n"})
			logPath := serveRegistry(manifestDocument(artifact, media, name, digest, layers), archive)
			destination := filepath.Join(GinkgoT().TempDir(), "out")

			_, stderr, err := runCLIStreams(
				"unpack",
				"--image="+imageReference(),
				"--destination="+destination,
				"--auth-dir="+filepath.Join(GinkgoT().TempDir(), "no-auth"),
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(message))
			Expect(stderr).NotTo(ContainSubstring("Unpacked "))
			entries, readErr := os.ReadDir(destination)
			Expect(readErr).NotTo(HaveOccurred())
			Expect(entries).To(BeEmpty())
			Expect(orasCalls(logPath)).To(HaveLen(1))
			Expect(orasCalls(logPath)[0]).To(ContainElement("manifest"))
		},
		Entry("artifact type", "application/vnd.oci.image.manifest.v1+json", layerType, "payload.tar.gz", 1, "artifact type"),
		Entry("layer name", artifactType, layerType, "other.tar.gz", 1, "layer name"),
		Entry("media type", artifactType, "application/vnd.oci.image.layer.v1.tar", "payload.tar.gz", 1, "media type"),
		Entry("layer count", artifactType, layerType, "payload.tar.gz", 2, "exactly one layer"),
	)

	It("rejects a downloaded layer whose digest does not match the manifest", func() {
		archive, _ := writePayload(map[string]string{"hello.txt": "hello\n"})
		logPath := serveRegistry(manifestDocument(artifactType, layerType, "payload.tar.gz", "sha256:"+strings.Repeat("b", 64), 1), archive)
		destination := filepath.Join(GinkgoT().TempDir(), "out")

		_, _, err := runCLIStreams(
			"unpack",
			"--image="+imageReference(),
			"--destination="+destination,
			"--auth-dir="+filepath.Join(GinkgoT().TempDir(), "no-auth"),
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("digest"))
		entries, readErr := os.ReadDir(destination)
		Expect(readErr).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty())
		Expect(orasCalls(logPath)).To(HaveLen(2))
	})

	It("rejects an archive path that escapes the destination", func() {
		parent := GinkgoT().TempDir()
		archive := filepath.Join(parent, "payload.tar.gz")
		file, err := os.Create(archive)
		Expect(err).NotTo(HaveOccurred())
		compressed := gzip.NewWriter(file)
		writer := tar.NewWriter(compressed)
		body := []byte("nope")
		Expect(writer.WriteHeader(&tar.Header{
			Name: "../outside.txt", Mode: 0644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		})).To(Succeed())
		_, err = writer.Write(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(writer.Close()).To(Succeed())
		Expect(compressed.Close()).To(Succeed())
		Expect(file.Close()).To(Succeed())

		destination := filepath.Join(parent, "out")
		serveRegistry(manifestDocument(artifactType, layerType, "payload.tar.gz", fileDigest(archive), 1), archive)
		_, _, err = runCLIStreams(
			"unpack",
			"--image="+imageReference(),
			"--destination="+destination,
			"--auth-dir="+filepath.Join(parent, "no-auth"),
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid archive path"))
		_, statErr := os.Lstat(filepath.Join(parent, "outside.txt"))
		Expect(os.IsNotExist(statErr)).To(BeTrue())
		entries, readErr := os.ReadDir(destination)
		Expect(readErr).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty())
	})

	It("does not fetch a manifest when the destination is not empty", func() {
		archive, digest := writePayload(map[string]string{"hello.txt": "hello\n"})
		logPath := serveRegistry(manifestDocument(artifactType, layerType, "payload.tar.gz", digest, 1), archive)
		destination := filepath.Join(GinkgoT().TempDir(), "out")
		Expect(os.Mkdir(destination, 0755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(destination, "existing"), []byte("keep"), 0644)).To(Succeed())

		_, _, err := runCLIStreams(
			"unpack",
			"--image="+imageReference(),
			"--destination="+destination,
			"--auth-dir="+filepath.Join(GinkgoT().TempDir(), "no-auth"),
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("empty"))
		Expect(string(mustReadFile(filepath.Join(destination, "existing")))).To(Equal("keep"))
		Expect(orasCalls(logPath)).To(BeEmpty())
	})

	DescribeTable("rejects an image reference that is not a manifest digest",
		func(image string) {
			destination := filepath.Join(GinkgoT().TempDir(), "out")
			_, _, err := runCLIStreams("unpack", "--image="+image, "--destination="+destination)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("registry/repo@sha256:<manifest-digest>"))
			_, statErr := os.Stat(destination)
			Expect(os.IsNotExist(statErr)).To(BeTrue())
		},
		Entry("trusted artifact", "oci:localhost:5000/team/output@"+manifestDigest),
		Entry("tag", "localhost:5000/team/output:latest"),
		Entry("tag and digest", "localhost:5000/team/output:latest@"+manifestDigest),
		Entry("short digest", "localhost:5000/team/output@sha256:abcd"),
	)

	It("is listed in the command help", func() {
		stdout, _, err := runCLIStreams("--help")
		Expect(err).NotTo(HaveOccurred())
		Expect(stdout).To(ContainSubstring("unpack"))
	})
})
