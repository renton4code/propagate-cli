package signing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	HeaderPublicKeySHA = "X-Propagate-Public-Key-SHA"
	HeaderTimestamp    = "X-Propagate-Timestamp"
	HeaderNonce        = "X-Propagate-Nonce"
	HeaderCLIVersion   = "X-Propagate-CLI-Version"
	HeaderOperationID  = "X-Propagate-Operation-ID"
	HeaderSignature    = "X-Propagate-Signature"
)

type Metadata struct {
	PublicKeySHA string
	Timestamp    string
	Nonce        string
	CLIVersion   string
	OperationID  string
	Signature    string
}

func MetadataFromHeaders(h http.Header) Metadata {
	return Metadata{
		PublicKeySHA: strings.TrimSpace(h.Get(HeaderPublicKeySHA)),
		Timestamp:    strings.TrimSpace(h.Get(HeaderTimestamp)),
		Nonce:        strings.TrimSpace(h.Get(HeaderNonce)),
		CLIVersion:   strings.TrimSpace(h.Get(HeaderCLIVersion)),
		OperationID:  strings.TrimSpace(h.Get(HeaderOperationID)),
		Signature:    strings.TrimSpace(h.Get(HeaderSignature)),
	}
}

func (m Metadata) Validate(operationID string) error {
	switch {
	case m.PublicKeySHA == "":
		return errors.New("missing public key SHA")
	case m.Timestamp == "":
		return errors.New("missing timestamp")
	case m.Nonce == "":
		return errors.New("missing nonce")
	case m.CLIVersion == "":
		return errors.New("missing CLI version")
	case m.Signature == "":
		return errors.New("missing signature")
	}
	if m.OperationID != "" && operationID != "" && m.OperationID != operationID {
		return errors.New("operation ID header does not match request body")
	}
	return nil
}

func Canonical(method, path, rawQuery string, body []byte, metadata Metadata) string {
	sum := sha256.Sum256(body)
	return strings.Join([]string{
		strings.ToUpper(method),
		path,
		rawQuery,
		hex.EncodeToString(sum[:]),
		metadata.Timestamp,
		metadata.Nonce,
		metadata.PublicKeySHA,
		metadata.CLIVersion,
		metadata.OperationID,
	}, "\n")
}

func Verify(publicKey ed25519.PublicKey, canonical string, signatureBase64 string) error {
	signature, err := base64.StdEncoding.DecodeString(signatureBase64)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("invalid signature length: got %d bytes", len(signature))
	}
	if !ed25519.Verify(publicKey, []byte(canonical), signature) {
		return errors.New("signature verification failed")
	}
	return nil
}

func PublicKeySHA(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ParseOpenSSHEd25519PublicKey(value string) (ed25519.PublicKey, error) {
	parts := strings.Fields(value)
	if len(parts) < 2 || parts[0] != "ssh-ed25519" {
		return nil, errors.New("expected ssh-ed25519 public key")
	}
	raw, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode OpenSSH public key: %w", err)
	}
	keyType, rest, err := readSSHString(raw)
	if err != nil {
		return nil, err
	}
	if string(keyType) != "ssh-ed25519" {
		return nil, errors.New("OpenSSH public key type is not ssh-ed25519")
	}
	key, rest, err := readSSHString(rest)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, errors.New("OpenSSH public key has trailing bytes")
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid Ed25519 public key length: got %d bytes", len(key))
	}
	return ed25519.PublicKey(key), nil
}

func OpenSSHEd25519PublicKey(publicKey ed25519.PublicKey, comment string) string {
	var buf []byte
	buf = appendSSHString(buf, []byte("ssh-ed25519"))
	buf = appendSSHString(buf, publicKey)

	out := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(buf)
	comment = strings.TrimSpace(comment)
	if comment != "" {
		out += " " + comment
	}
	return out
}

func readSSHString(data []byte) ([]byte, []byte, error) {
	if len(data) < 4 {
		return nil, nil, errors.New("truncated SSH string length")
	}
	size := binary.BigEndian.Uint32(data[:4])
	if size > uint32(len(data)-4) {
		return nil, nil, errors.New("truncated SSH string data")
	}
	start := 4
	end := start + int(size)
	return data[start:end], data[end:], nil
}

func appendSSHString(dst []byte, value []byte) []byte {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	dst = append(dst, size[:]...)
	dst = append(dst, value...)
	return dst
}
