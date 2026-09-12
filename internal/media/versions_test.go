package media

import "testing"

func TestImmutableObjectVersionBoundary(t *testing.T) {
	base := "accounts/a/media/b"
	for _, v := range []string{base, base + "/versions/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"} {
		if !validObjectVersion(base, v) {
			t.Fatal(v)
		}
	}
	for _, v := range []string{base + "/versions/../../escape", base + "/versions/other", base + "/versions/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/file", "accounts/other/media/b/versions/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"} {
		if validObjectVersion(base, v) {
			t.Fatal(v)
		}
	}
}
