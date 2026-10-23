package np

import "testing"

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"0671234567":       "380671234567",
		"+380671234567":    "380671234567",
		"380 67 123 45 67": "380671234567",
		"(067) 123-45-67":  "380671234567",
		"671234567":        "380671234567",
	}
	for in, want := range cases {
		if got := normalizePhone(in); got != want {
			t.Errorf("normalizePhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitFullName(t *testing.T) {
	cases := []struct {
		full                string
		first, last, middle string
	}{
		{"Іваненко Іван Петрович", "Іван", "Іваненко", "Петрович"},
		{"Іваненко Іван", "Іван", "Іваненко", ""},
		{"Іваненко", "Іваненко", "Іваненко", ""},
	}
	for _, c := range cases {
		first, last, middle := splitFullName(c.full)
		if first != c.first || last != c.last || middle != c.middle {
			t.Errorf("splitFullName(%q) = (%q,%q,%q), want (%q,%q,%q)",
				c.full, first, last, middle, c.first, c.last, c.middle)
		}
	}
}
