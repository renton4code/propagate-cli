package secretcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	shared "propagate/shared/secretcrypto"
)

const (
	EnvelopeAlgorithm = shared.EnvelopeAlgorithm
	ValueAlgorithm    = "propagate-secret-aesgcm-v1"
	DigestAlgorithm   = "hmac-sha-256:v1"

	scopeKeySize = shared.ScopeKeySize
	nonceSize    = 12
)

func GenerateScopeKey() ([]byte, error) {
	return shared.GenerateScopeKey()
}

func EncryptScopeKey(scopeKey []byte, recipientPublicKey string, scope string, recipientKeySHA string, scopeKeyVersion int) (string, error) {
	return shared.EncryptScopeKey(scopeKey, recipientPublicKey, scope, recipientKeySHA, scopeKeyVersion)
}

func DecryptScopeKey(encryptionPrivateKey string, encryptedScopeKey string, algorithm string, scope string, recipientKeySHA string, scopeKeyVersion int) ([]byte, error) {
	return shared.DecryptScopeKey(encryptionPrivateKey, encryptedScopeKey, algorithm, scope, recipientKeySHA, scopeKeyVersion)
}

func FingerprintValue(scopeKey []byte, teamID string, scope string, envFilePath string, name string, scopeKeyVersion int, plaintext string) string {
	mac := hmac.New(sha256.New, scopeKey)
	mac.Write([]byte(DigestAlgorithm))
	mac.Write([]byte{0})
	mac.Write(valueAAD(teamID, scope, envFilePath, name, scopeKeyVersion))
	mac.Write([]byte{0})
	mac.Write([]byte(plaintext))
	return DigestAlgorithm + ":" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func EncryptValue(scopeKey []byte, teamID string, scope string, envFilePath string, name string, scopeKeyVersion int, plaintext string) (ciphertext string, nonce string, err error) {
	aead, err := valueAEAD(scopeKey)
	if err != nil {
		return "", "", err
	}
	nonceRaw, err := shared.RandomNonce()
	if err != nil {
		return "", "", err
	}
	aad := valueAAD(teamID, scope, envFilePath, name, scopeKeyVersion)
	sealed := aead.Seal(nil, nonceRaw, []byte(plaintext), aad)
	return base64.StdEncoding.EncodeToString(sealed), base64.StdEncoding.EncodeToString(nonceRaw), nil
}

func DecryptValue(scopeKey []byte, teamID string, scope string, envFilePath string, name string, scopeKeyVersion int, ciphertext string, nonce string, algorithm string) (string, error) {
	if algorithm != ValueAlgorithm {
		return "", fmt.Errorf("unsupported env value algorithm %q", algorithm)
	}
	aead, err := valueAEAD(scopeKey)
	if err != nil {
		return "", err
	}
	nonceRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(nonce))
	if err != nil {
		return "", fmt.Errorf("decode env value nonce: %w", err)
	}
	if len(nonceRaw) != nonceSize {
		return "", fmt.Errorf("env value nonce has invalid length %d", len(nonceRaw))
	}
	ciphertextRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ciphertext))
	if err != nil {
		return "", fmt.Errorf("decode env value ciphertext: %w", err)
	}
	aad := valueAAD(teamID, scope, envFilePath, name, scopeKeyVersion)
	plaintext, err := aead.Open(nil, nonceRaw, ciphertextRaw, aad)
	if err != nil {
		return "", errors.New("env value could not be decrypted")
	}
	return string(plaintext), nil
}

func valueAEAD(scopeKey []byte) (cipher.AEAD, error) {
	if len(scopeKey) != scopeKeySize {
		return nil, fmt.Errorf("scope key must be %d bytes", scopeKeySize)
	}
	block, err := aes.NewCipher(scopeKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func valueAAD(teamID string, scope string, envFilePath string, name string, scopeKeyVersion int) []byte {
	return []byte(fmt.Sprintf("%s\nteam:%s\nscope:%s\npath:%s\nname:%s\nversion:%d", ValueAlgorithm, teamID, scope, envFilePath, name, scopeKeyVersion))
}
