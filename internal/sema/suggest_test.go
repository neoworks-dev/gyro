package sema

import "testing"

// Which names get a guess, and what the guess says, is pinned by the
// fixtures in testdata/check. What is left here is the metric they rest
// on, which no fixture can reach.

func TestEditDistanceCountsEdits(t *testing.T) {
	cases := []struct {
		from, to string
		want     int
	}{
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"counter", "countr", 1},
		{"length", "lenght", 2},
		{"kitten", "sitting", 3},
	}
	for _, c := range cases {
		if got := editDistance(c.from, c.to); got != c.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", c.from, c.to, got, c.want)
		}
	}
}
