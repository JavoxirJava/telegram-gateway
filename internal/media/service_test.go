package media

import "testing"

func TestObjectKey(t *testing.T) {
	key, err := ObjectKey("account-123", "media-456")
	if err != nil {
		t.Fatal(err)
	}
	if key != "accounts/account-123/media/media-456" {
		t.Fatalf("key = %q", key)
	}
}

func TestObjectKeyRejectsPathSeparators(t *testing.T) {
	cases := [][2]string{
		{"../account", "media"},
		{"account", "../media"},
		{"account\\other", "media"},
	}
	for _, tt := range cases {
		if _, err := ObjectKey(tt[0], tt[1]); err == nil {
			t.Fatalf("expected invalid key for account=%q media=%q", tt[0], tt[1])
		}
	}
}
