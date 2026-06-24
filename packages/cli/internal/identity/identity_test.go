package identity

import "testing"

func TestEnsureSeedsProfileDefaultAPIURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := DefaultAPIURL
	DefaultAPIURL = "http://default.test"
	t.Cleanup(func() {
		DefaultAPIURL = old
	})

	if _, err := Ensure("alice@example.com"); err != nil {
		t.Fatal(err)
	}
	profile, err := LoadProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.DefaultAPIURL != "http://default.test" {
		t.Fatalf("default_api_url = %q", profile.DefaultAPIURL)
	}
}
