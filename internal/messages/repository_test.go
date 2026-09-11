package messages

import "testing"

func TestNormalizeLimit(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "default for zero", in: 0, want: 30},
		{name: "default for negative", in: -1, want: 30},
		{name: "keeps normal value", in: 50, want: 50},
		{name: "caps large value", in: 1000, want: 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeLimit(tt.in); got != tt.want {
				t.Fatalf("normalizeLimit(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestEscapeLike(t *testing.T) {
	got := escapeLike(`100%_done\ok`)
	want := `100\%\_done\\ok`
	if got != want {
		t.Fatalf("escapeLike() = %q, want %q", got, want)
	}
}
