package domain

import (
	"crypto/rand"
	"fmt"
	"unicode"
)

// invitePINLetters is A–Z without O, which is easily confused with digit zero.
const invitePINLetters = "ABCDEFGHIJKLMNPQRSTUVWXYZ"

// GenerateInvitePIN returns four decimal digits and one Latin letter (A–Z, excluding O), e.g. "7391K".
func GenerateInvitePIN() (string, error) {
	var digits [4]byte
	if _, err := rand.Read(digits[:]); err != nil {
		return "", err
	}
	out := make([]byte, 5)
	for i := 0; i < 4; i++ {
		out[i] = '0' + digits[i]%10
	}
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	out[4] = invitePINLetters[b[0]%byte(len(invitePINLetters))]
	return string(out), nil
}

// NormalizeInvitePIN trims whitespace and uppercases the letter; validates shape \d{4}[A-Z].
func NormalizeInvitePIN(pin string) (string, error) {
	runes := []rune(pin)
	if len(runes) != 5 {
		return "", fmt.Errorf("invite pin must be 4 digits followed by one letter")
	}
	for i := 0; i < 4; i++ {
		if runes[i] < '0' || runes[i] > '9' {
			return "", fmt.Errorf("invite pin must be 4 digits followed by one letter")
		}
	}
	last := runes[4]
	if last >= 'a' && last <= 'z' {
		last = unicode.ToUpper(last)
	}
	if last < 'A' || last > 'Z' {
		return "", fmt.Errorf("invite pin must end with a letter A–Z")
	}
	return string(runes[:4]) + string(last), nil
}
