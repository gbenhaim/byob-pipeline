package system

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

// TestUnpackTaskRunManifest checks the TaskRun shape without a cluster.
func TestUnpackTaskRunManifest(t *testing.T) {
	previous := namespace
	t.Cleanup(func() { namespace = previous })
	namespace = "byob-system"
	digest := strings.Repeat("a", 64)
	reference := "registry-service.kind-registry/byob-system/output@sha256:" + digest
	manifest := unpackTaskRun("unpack-1", reference, []string{"hello.txt", "linux-amd64/hello.txt"})

	for _, snippet := range []string{
		"name: unpack-build-output",
		"value: \"" + reference + "\"",
		"value: /var/workdir/output",
		"mountPath: /var/workdir",
		"mountPath: /var/run/byob-registry-auth",
		"subPath: ca-bundle.crt",
		"$(steps.show.results.FILES)",
		"$(step.results.FILES.path)",
		"- \"hello.txt\"",
		"- \"linux-amd64/hello.txt\"",
		"serviceAccountName: byob-build",
	} {
		if !strings.Contains(manifest, snippet) {
			t.Errorf("manifest missing %q\n%s", snippet, manifest)
		}
	}

	if strings.Contains(manifest, "$(cat") {
		t.Errorf("script uses a Tekton substitution for cat\n%s", manifest)
	}

	unpack := manifest[strings.Index(manifest, "- name: unpack"):strings.Index(manifest, "- name: show")]
	if strings.Contains(unpack, "volumeMounts:") {
		t.Errorf("unpack step declares volumeMounts\n%s", unpack)
	}
}

// TestManifestReferenceStripsOutputTag checks the digest reference the StepAction accepts.
func TestManifestReferenceStripsOutputTag(t *testing.T) {
	RegisterTestingT(t)
	digest := "sha256:" + strings.Repeat("ab", 32)
	got := manifestReference("registry-service.kind-registry/byob-system/output:byob-1", digest)
	want := "registry-service.kind-registry/byob-system/output@" + digest
	if got != want {
		t.Fatalf("manifest reference %q, want %q", got, want)
	}

	got = manifestReference("localhost:5000/byob-system/output:byob-1", digest)
	want = "localhost:5000/byob-system/output@" + digest
	if got != want {
		t.Fatalf("manifest reference %q, want %q", got, want)
	}
}
