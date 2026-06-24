package identity

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"propagate/cli/internal/atomicfile"
)

const (
	DirName      = ".propagate"
	IdentityFile = "identity"
	ProfileFile  = "profile"
)

var DefaultAPIURL = "https://api.propagatecli.com/"

type Identity struct {
	FormatVersion        int    `json:"format_version"`
	Handle               string `json:"handle"`
	PublicKeySHA         string `json:"public_key_sha"`
	SigningPublicKey     string `json:"signing_public_key"`
	SigningPrivateKey    string `json:"signing_private_key"`
	EncryptionPublicKey  string `json:"encryption_public_key"`
	EncryptionPrivateKey string `json:"encryption_private_key"`
	CreatedAt            string `json:"created_at"`
}

type Profile struct {
	FormatVersion      int    `json:"format_version"`
	Handle             string `json:"handle"`
	DefaultAPIURL      string `json:"default_api_url,omitempty"`
	LastSeenCLIVersion string `json:"last_seen_cli_version,omitempty"`
	PreferredOutput    string `json:"preferred_output,omitempty"`
}

type Summary struct {
	Handle              string `json:"handle"`
	PublicKeySHA        string `json:"public_key_sha"`
	SigningPublicKey    string `json:"signing_public_key"`
	EncryptionPublicKey string `json:"encryption_public_key"`
}

type EnsureResult struct {
	Identity Identity
	Created  bool
	Dir      string
	Path     string
	Profile  string
}

func Ensure(handle string) (EnsureResult, error) {
	dir, err := Directory()
	if err != nil {
		return EnsureResult{}, err
	}
	path := filepath.Join(dir, IdentityFile)
	profilePath := filepath.Join(dir, ProfileFile)

	exists, err := atomicfile.Exists(path)
	if err != nil {
		return EnsureResult{}, fmt.Errorf("check identity: %w", err)
	}
	if exists {
		ident, err := Load()
		if err != nil {
			return EnsureResult{}, err
		}
		if err := ensureProfile(profilePath, ident.Handle); err != nil {
			return EnsureResult{}, err
		}
		return EnsureResult{
			Identity: ident,
			Created:  false,
			Dir:      dir,
			Path:     path,
			Profile:  profilePath,
		}, nil
	}

	if strings.TrimSpace(handle) == "" {
		return EnsureResult{}, errors.New("handle is required to create a local identity")
	}
	if err := ensureDirectory(dir); err != nil {
		return EnsureResult{}, err
	}

	ident, err := New(handle, time.Now().UTC())
	if err != nil {
		return EnsureResult{}, err
	}
	payload, err := json.MarshalIndent(ident, "", "  ")
	if err != nil {
		return EnsureResult{}, err
	}
	payload = append(payload, '\n')
	if err := atomicfile.Write(path, payload, 0o600); err != nil {
		return EnsureResult{}, fmt.Errorf("write identity: %w", err)
	}
	if err := ensureProfile(profilePath, ident.Handle); err != nil {
		return EnsureResult{}, err
	}

	return EnsureResult{
		Identity: ident,
		Created:  true,
		Dir:      dir,
		Path:     path,
		Profile:  profilePath,
	}, nil
}

func Directory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, DirName), nil
}

func Load() (Identity, error) {
	dir, err := Directory()
	if err != nil {
		return Identity{}, err
	}
	if err := validateDirectory(dir); err != nil {
		return Identity{}, err
	}

	path := filepath.Join(dir, IdentityFile)
	info, err := os.Stat(path)
	if err != nil {
		return Identity{}, fmt.Errorf("read identity metadata: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Identity{}, fmt.Errorf("unsafe permissions on %s: expected owner-only file permissions", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Identity{}, fmt.Errorf("read identity: %w", err)
	}
	var ident Identity
	if err := json.Unmarshal(data, &ident); err != nil {
		return Identity{}, fmt.Errorf("parse identity: %w", err)
	}
	if err := ident.Validate(); err != nil {
		return Identity{}, err
	}
	return ident, nil
}

func LoadProfile() (Profile, error) {
	dir, err := Directory()
	if err != nil {
		return Profile{}, err
	}
	if err := validateDirectory(dir); err != nil {
		return Profile{}, err
	}

	path := filepath.Join(dir, ProfileFile)
	info, err := os.Stat(path)
	if err != nil {
		return Profile{}, fmt.Errorf("read profile metadata: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Profile{}, fmt.Errorf("unsafe permissions on %s: expected owner-only file permissions", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, fmt.Errorf("read profile: %w", err)
	}
	var profile Profile
	if err := json.Unmarshal(data, &profile); err != nil {
		return Profile{}, fmt.Errorf("parse profile: %w", err)
	}
	if profile.FormatVersion != 1 {
		return Profile{}, fmt.Errorf("unsupported profile format version %d", profile.FormatVersion)
	}
	return profile, nil
}

func New(handle string, now time.Time) (Identity, error) {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return Identity{}, errors.New("handle is required")
	}

	signingPublic, signingPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	encryptionPrivate, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	encryptionPublic := encryptionPrivate.PublicKey()

	return Identity{
		FormatVersion:        1,
		Handle:               handle,
		PublicKeySHA:         PublicKeySHA(signingPublic),
		SigningPublicKey:     OpenSSHEd25519PublicKey(signingPublic, handle),
		SigningPrivateKey:    base64.StdEncoding.EncodeToString(signingPrivate.Seed()),
		EncryptionPublicKey:  "x25519:" + base64.StdEncoding.EncodeToString(encryptionPublic.Bytes()),
		EncryptionPrivateKey: "x25519:" + base64.StdEncoding.EncodeToString(encryptionPrivate.Bytes()),
		CreatedAt:            now.Format(time.RFC3339),
	}, nil
}

func (i Identity) Validate() error {
	if i.FormatVersion != 1 {
		return fmt.Errorf("unsupported identity format version %d", i.FormatVersion)
	}
	if strings.TrimSpace(i.Handle) == "" {
		return errors.New("identity handle is empty")
	}
	seed, err := base64.StdEncoding.DecodeString(i.SigningPrivateKey)
	if err != nil {
		return fmt.Errorf("decode signing private key: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return fmt.Errorf("invalid signing private key length: got %d bytes", len(seed))
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	if got := PublicKeySHA(publicKey); got != i.PublicKeySHA {
		return fmt.Errorf("identity public key SHA mismatch: got %s from local private key, file says %s", got, i.PublicKeySHA)
	}
	if got := OpenSSHEd25519PublicKey(publicKey, i.Handle); !samePublicKey(got, i.SigningPublicKey) {
		return errors.New("identity signing public key does not match local private key")
	}

	encRaw, err := parseX25519Private(i.EncryptionPrivateKey)
	if err != nil {
		return err
	}
	encPrivate, err := ecdh.X25519().NewPrivateKey(encRaw)
	if err != nil {
		return fmt.Errorf("invalid encryption private key: %w", err)
	}
	if got := "x25519:" + base64.StdEncoding.EncodeToString(encPrivate.PublicKey().Bytes()); got != i.EncryptionPublicKey {
		return errors.New("identity encryption public key does not match local private key")
	}
	if _, err := time.Parse(time.RFC3339, i.CreatedAt); err != nil {
		return fmt.Errorf("invalid identity created_at: %w", err)
	}
	return nil
}

func (i Identity) Summary() Summary {
	return Summary{
		Handle:              i.Handle,
		PublicKeySHA:        i.PublicKeySHA,
		SigningPublicKey:    i.SigningPublicKey,
		EncryptionPublicKey: i.EncryptionPublicKey,
	}
}

func PublicKeySHA(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func OpenSSHEd25519PublicKey(publicKey ed25519.PublicKey, comment string) string {
	var buf []byte
	buf = appendSSHString(buf, []byte("ssh-ed25519"))
	buf = appendSSHString(buf, publicKey)

	key := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(buf)
	comment = strings.TrimSpace(comment)
	if comment != "" {
		key += " " + comment
	}
	return key
}

func appendSSHString(dst []byte, value []byte) []byte {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	dst = append(dst, size[:]...)
	dst = append(dst, value...)
	return dst
}

func samePublicKey(a, b string) bool {
	aFields := strings.Fields(a)
	bFields := strings.Fields(b)
	if len(aFields) < 2 || len(bFields) < 2 {
		return false
	}
	return aFields[0] == bFields[0] && aFields[1] == bFields[1]
}

func parseX25519Private(value string) ([]byte, error) {
	raw := strings.TrimPrefix(value, "x25519:")
	out, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode encryption private key: %w", err)
	}
	return out, nil
}

func ensureDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return validateDirectory(dir)
}

func validateDirectory(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("read %s metadata: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists but is not a directory", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("unsafe permissions on %s: expected owner-only directory permissions", dir)
	}
	return nil
}

func ensureProfile(path, handle string) error {
	exists, err := atomicfile.Exists(path)
	if err != nil {
		return fmt.Errorf("check profile: %w", err)
	}
	if exists {
		return nil
	}
	profile := Profile{
		FormatVersion: 1,
		Handle:        handle,
		DefaultAPIURL: DefaultAPIURL,
	}
	payload, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := atomicfile.Write(path, payload, 0o600); err != nil {
		return fmt.Errorf("write profile: %w", err)
	}
	return nil
}
