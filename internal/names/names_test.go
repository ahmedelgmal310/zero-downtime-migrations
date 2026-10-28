package names

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestSplitJoin(t *testing.T) {
	cases := []struct{ full, first, last string }{
		{"Ada Lovelace", "Ada", "Lovelace"},
		{"Mary Ann Smith", "Mary", "Ann Smith"},
		{"Cher", "Cher", ""},
		{"Tim Berners Lee", "Tim", "Berners Lee"},
		{"A  B", "A", " B"},
		{"Ünal Çelik", "Ünal", "Çelik"},
	}
	for _, c := range cases {
		first, last := Split(c.full)
		if first != c.first || last != c.last {
			t.Errorf("Split(%q) = (%q, %q), want (%q, %q)", c.full, first, last, c.first, c.last)
		}
		if got := Join(first, last); got != c.full {
			t.Errorf("Join(Split(%q)) = %q", c.full, got)
		}
	}
}

func TestValidate(t *testing.T) {
	valid := []string{"Ada", "Ada Lovelace", "Mary Ann Smith", strings.Repeat("x", MaxLen)}
	for _, n := range valid {
		if err := Validate(n); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", n, err)
		}
	}
	invalid := []string{"", " Ada", "Ada ", "Ada\t", "\nAda", strings.Repeat("x", MaxLen+1), "bad\xff", "nul\x00byte"}
	for _, n := range invalid {
		if err := Validate(n); err == nil {
			t.Errorf("Validate(%q) = nil, want error", n)
		}
	}
}

func FuzzRoundTrip(f *testing.F) {
	for _, s := range []string{"Ada Lovelace", "Cher", "a  b c", "x y"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if Validate(s) != nil {
			return
		}
		if got := Join(Split(s)); got != s {
			t.Fatalf("Join(Split(%q)) = %q", s, got)
		}
	})
}

func TestSeedName(t *testing.T) {
	if got := SeedName(50); got != SeedFirst[50%len(SeedFirst)] {
		t.Errorf("SeedName(50) = %q, want a single word", got)
	}
	if got := SeedName(1); got != "Alan Lovelace" {
		t.Errorf("SeedName(1) = %q", got)
	}
	for i := int64(1); i <= 5000; i++ {
		n := SeedName(i)
		if err := Validate(n); err != nil {
			t.Fatalf("SeedName(%d) = %q is not a valid name: %v", i, n, err)
		}
	}
}

// The load test verifies every read against a JavaScript copy of SeedName.
func TestSeedNameMatchesLoadTest(t *testing.T) {
	b, err := os.ReadFile("../../loadtest/load.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for name, want := range map[string][]string{"SEED_FIRST": SeedFirst, "SEED_LAST": SeedLast} {
		m := regexp.MustCompile(`(?s)const ` + name + ` = \[(.*?)\];`).FindStringSubmatch(js)
		if m == nil {
			t.Fatalf("%s not found in load.js", name)
		}
		got := regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1)
		if len(got) != len(want) {
			t.Fatalf("%s: %d entries in load.js, %d in Go", name, len(got), len(want))
		}
		for i := range want {
			if got[i][1] != want[i] {
				t.Errorf("%s[%d] = %q in load.js, %q in Go", name, i, got[i][1], want[i])
			}
		}
	}
	if !strings.Contains(js, "i % 50 === 0") {
		t.Error("load.js seedName no longer has the single-word rule of SeedName")
	}
}
