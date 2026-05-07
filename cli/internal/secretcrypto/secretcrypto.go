package secretcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	EnvelopeAlgorithm = "propagate-envelope-x25519-aesgcm-v1"
	ValueAlgorithm    = "propagate-secret-aesgcm-v1"
	DigestAlgorithm   = "hmac-sha-256:v1"

	scopeKeySize = 32
	nonceSize    = 12
)

func GenerateScopeKey() ([]byte, error) {
	key := make([]byte, scopeKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
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

func EncryptScopeKey(scopeKey []byte, recipientPublicKey string, scope string, recipientKeySHA string, scopeKeyVersion int) (string, error) {
	if len(scopeKey) != scopeKeySize {
		return "", fmt.Errorf("scope key must be %d bytes", scopeKeySize)
	}
	recipientRaw, err := parseX25519Key(recipientPublicKey)
	if err != nil {
		return "", err
	}
	recipient, err := ecdh.X25519().NewPublicKey(recipientRaw)
	if err != nil {
		return "", fmt.Errorf("invalid recipient public key: %w", err)
	}
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	shared, err := ephemeral.ECDH(recipient)
	if err != nil {
		return "", err
	}
	aad := envelopeAAD(scope, recipientKeySHA, scopeKeyVersion)
	aead, err := envelopeAEAD(shared, aad)
	if err != nil {
		return "", err
	}
	nonce, err := randomNonce()
	if err != nil {
		return "", err
	}
	ciphertext := aead.Seal(nil, nonce, scopeKey, aad)
	payload := append([]byte{}, ephemeral.PublicKey().Bytes()...)
	payload = append(payload, nonce...)
	payload = append(payload, ciphertext...)
	return base64.StdEncoding.EncodeToString(payload), nil
}

func DecryptScopeKey(encryptionPrivateKey string, encryptedScopeKey string, algorithm string, scope string, recipientKeySHA string, scopeKeyVersion int) ([]byte, error) {
	if algorithm != EnvelopeAlgorithm {
		return nil, fmt.Errorf("unsupported scope key envelope algorithm %q", algorithm)
	}
	privateRaw, err := parseX25519Key(encryptionPrivateKey)
	if err != nil {
		return nil, err
	}
	privateKey, err := ecdh.X25519().NewPrivateKey(privateRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid local encryption private key: %w", err)
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encryptedScopeKey))
	if err != nil {
		return nil, fmt.Errorf("decode scope key envelope: %w", err)
	}
	if len(payload) < 32+nonceSize {
		return nil, errors.New("scope key envelope is truncated")
	}
	ephemeralPublic, err := ecdh.X25519().NewPublicKey(payload[:32])
	if err != nil {
		return nil, fmt.Errorf("invalid scope key envelope public key: %w", err)
	}
	nonce := payload[32 : 32+nonceSize]
	ciphertext := payload[32+nonceSize:]
	shared, err := privateKey.ECDH(ephemeralPublic)
	if err != nil {
		return nil, err
	}
	aad := envelopeAAD(scope, recipientKeySHA, scopeKeyVersion)
	aead, err := envelopeAEAD(shared, aad)
	if err != nil {
		return nil, err
	}
	scopeKey, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, errors.New("scope key envelope could not be decrypted")
	}
	if len(scopeKey) != scopeKeySize {
		return nil, fmt.Errorf("decrypted scope key has invalid length %d", len(scopeKey))
	}
	return scopeKey, nil
}

func EncryptValue(scopeKey []byte, teamID string, scope string, envFilePath string, name string, scopeKeyVersion int, plaintext string) (ciphertext string, nonce string, err error) {
	aead, err := valueAEAD(scopeKey)
	if err != nil {
		return "", "", err
	}
	nonceRaw, err := randomNonce()
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

func envelopeAEAD(shared []byte, aad []byte) (cipher.AEAD, error) {
	key := hkdfSHA256(shared, []byte(EnvelopeAlgorithm), aad, scopeKeySize)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
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

func envelopeAAD(scope string, recipientKeySHA string, scopeKeyVersion int) []byte {
	return []byte(fmt.Sprintf("%s\nscope:%s\nrecipient:%s\nversion:%d", EnvelopeAlgorithm, scope, recipientKeySHA, scopeKeyVersion))
}

func valueAAD(teamID string, scope string, envFilePath string, name string, scopeKeyVersion int) []byte {
	return []byte(fmt.Sprintf("%s\nteam:%s\nscope:%s\npath:%s\nname:%s\nversion:%d", ValueAlgorithm, teamID, scope, envFilePath, name, scopeKeyVersion))
}

func parseX25519Key(value string) ([]byte, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(value), "x25519:")
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode x25519 key: %w", err)
	}
	if len(decoded) != 32 {
		return nil, fmt.Errorf("x25519 key has invalid length %d", len(decoded))
	}
	return decoded, nil
}

func randomNonce() ([]byte, error) {
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return nonce, nil
}

func hkdfSHA256(secret []byte, salt []byte, info []byte, size int) []byte {
	extract := hmac.New(sha256.New, salt)
	extract.Write(secret)
	prk := extract.Sum(nil)

	var out []byte
	var previous []byte
	counter := byte(1)
	for len(out) < size {
		expand := hmac.New(sha256.New, prk)
		expand.Write(previous)
		expand.Write(info)
		expand.Write([]byte{counter})
		previous = expand.Sum(nil)
		out = append(out, previous...)
		counter++
	}
	return out[:size]
}
