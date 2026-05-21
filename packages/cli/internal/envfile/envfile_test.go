package envfile

import "testing"

func TestMaskAlwaysUsesTwoStars(t *testing.T) {
	tests := map[string]string{
		"":       "",
		"a":      "**",
		"ab":     "a**",
		"abc":    "a**c",
		"abcdef": "a**f",
	}
	for value, want := range tests {
		if got := Mask(value); got != want {
			t.Fatalf("Mask(%q) = %q, want %q", value, got, want)
		}
	}
}
