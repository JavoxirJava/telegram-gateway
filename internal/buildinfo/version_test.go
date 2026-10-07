package buildinfo

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseVersionMatchesManifest(t *testing.T) {
	b, err := os.ReadFile("../../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != Version {
		t.Fatal("VERSION must match the runtime release version")
	}
}
