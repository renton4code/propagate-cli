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

	ScopeKeySize = 32
	nonceSize    = 12
)

func GenerateScopeKey() ([]byte, error) {
	key := make([]byte, ScopeKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func EncryptScopeKey(scopeKey []byte, recipientPublicKey string, scope string, recipientKeySHA string, scopeKeyVersion int) (string, error) {
	if len(scopeKey) != ScopeKeySize {
		return "", fmt.Errorf("scope key must be %d bytes", ScopeKeySize)
	}
	recipientRaw, err := ParseX25519Key(recipientPublicKey)
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
	aad := EnvelopeAAD(scope, recipientKeySHA, scopeKeyVersion)
	aead, err := envelopeAEAD(shared, aad)
	if err != nil {
		return "", err
	}
	nonce, err := RandomNonce()
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
	privateRaw, err := ParseX25519Key(encryptionPrivateKey)
	if err != nil {
		return nil, err
	}
	privateKey, err := ecdh.X25519().NewPrivateKey(privateRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid local encryption private key: %w", err)
	}
	return DecryptScopeKeyWithPrivate(privateKey, encryptedScopeKey, scope, recipientKeySHA, scopeKeyVersion)
}

func DecryptScopeKeyWithPrivate(privateKey *ecdh.PrivateKey, encryptedScopeKey string, scope string, recipientKeySHA string, scopeKeyVersion int) ([]byte, error) {
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
	aad := EnvelopeAAD(scope, recipientKeySHA, scopeKeyVersion)
	aead, err := envelopeAEAD(shared, aad)
	if err != nil {
		return nil, err
	}
	scopeKey, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, errors.New("scope key envelope could not be decrypted")
	}
	if len(scopeKey) != ScopeKeySize {
		return nil, fmt.Errorf("decrypted scope key has invalid length %d", len(scopeKey))
	}
	return scopeKey, nil
}

func ParseX25519Key(value string) ([]byte, error) {
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

func FormatX25519PublicKey(key *ecdh.PublicKey) string {
	return "x25519:" + base64.StdEncoding.EncodeToString(key.Bytes())
}

func EnvelopeAAD(scope string, recipientKeySHA string, scopeKeyVersion int) []byte {
	return []byte(fmt.Sprintf("%s\nscope:%s\nrecipient:%s\nversion:%d", EnvelopeAlgorithm, scope, recipientKeySHA, scopeKeyVersion))
}

func RandomNonce() ([]byte, error) {
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return nonce, nil
}

func envelopeAEAD(shared []byte, aad []byte) (cipher.AEAD, error) {
	key := hkdfSHA256(shared, []byte(EnvelopeAlgorithm), aad, ScopeKeySize)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
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
