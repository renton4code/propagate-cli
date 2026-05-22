package domain

import "testing"

func TestNormalizeInvitePIN(t *testing.T) {
	got, err := NormalizeInvitePIN("1234a")
	if err != nil {
		t.Fatal(err)
	}
	if want := "1234A"; got != want {
		t.Fatalf("NormalizeInvitePIN = %q, want %q", got, want)
	}
	for _, bad := range []string{"", "123", "12345", "12345A", "abcdA", "1234$"} {
		if _, err := NormalizeInvitePIN(bad); err == nil {
			t.Fatalf("NormalizeInvitePIN(%q) expected error", bad)
		}
	}
}

func TestGenerateInvitePINExcludesLetterO(t *testing.T) {
	for i := 0; i < 500; i++ {
		pin, err := GenerateInvitePIN()
		if err != nil {
			t.Fatal(err)
		}
		if pin[4] == 'O' {
			t.Fatalf("generated pin %q contains letter O", pin)
		}
	}
}

func TestGenerateInvitePINShape(t *testing.T) {
	pin, err := GenerateInvitePIN()
	if err != nil {
		t.Fatal(err)
	}
	norm, err := NormalizeInvitePIN(pin)
	if err != nil {
		t.Fatalf("generated pin %q invalid: %v", pin, err)
	}
	if norm != pin {
		t.Fatalf("generated pin %q normalizes to %q", pin, norm)
	}
}
