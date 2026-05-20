package config

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type snapshotScope struct {
	EnvFiles  []string              `json:"env_files"`
	Variables []VariableDeclaration `json:"variables,omitempty"`
}

type snapshotTeam struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

type cloudSnapshot struct {
	Version int                      `json:"version"`
	Team    snapshotTeam             `json:"team"`
	Scopes  map[string]snapshotScope `json:"scopes"`
	Members []Member                 `json:"members"`
}

func ParseSnapshot(raw json.RawMessage, cloudRevision string) (ParsedProject, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ParsedProject{}, errors.New("config snapshot is empty")
	}
	if err := validateMetadataOnlyJSON(raw); err != nil {
		return ParsedProject{}, err
	}

	var snapshot cloudSnapshot
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&snapshot); err != nil {
		return ParsedProject{}, err
	}
	if snapshot.Version != 1 {
		return ParsedProject{}, fmt.Errorf("unsupported config version %d", snapshot.Version)
	}
	if strings.TrimSpace(snapshot.Team.ID) == "" {
		return ParsedProject{}, errors.New("team.id is required")
	}
	if strings.TrimSpace(snapshot.Team.Name) == "" {
		return ParsedProject{}, errors.New("team.name is required")
	}
	project := ParsedProject{
		Version:          snapshot.Version,
		TeamID:           snapshot.Team.ID,
		TeamName:         snapshot.Team.Name,
		CloudRevision:    cloudRevision,
		SyncStatus:       "synced",
		ActiveMemberSHAs: map[string]bool{},
	}
	if project.CloudRevision == "" {
		project.CloudRevision = LocalRevision
		project.SyncStatus = LocalSyncState
	}

	for _, name := range sortedSnapshotScopes(snapshot.Scopes) {
		scope := snapshot.Scopes[name]
		if err := ValidateScopeName(name); err != nil {
			return ParsedProject{}, err
		}
		files := append([]string(nil), scope.EnvFiles...)
		for _, file := range files {
			if err := validateEnvPath(file); err != nil {
				return ParsedProject{}, fmt.Errorf("scope %s: %w", name, err)
			}
		}
		knownPaths := map[string]bool{}
		for _, file := range files {
			knownPaths[file] = true
		}
		variables := sortedVariableDeclarations(scope.Variables)
		for idx, variable := range variables {
			if err := ValidateVariableDeclaration(variable, knownPaths); err != nil {
				return ParsedProject{}, fmt.Errorf("scope %s variable %d: %w", name, idx+1, err)
			}
		}
		project.Scopes = append(project.Scopes, ScopeSummary{
			Name:      name,
			EnvFiles:  files,
			Variables: variables,
		})
	}

	project.Members = append([]Member(nil), snapshot.Members...)
	for idx, member := range project.Members {
		member = NormalizeMemberAccess(member, project.Scopes)
		project.Members[idx] = member
		if err := ValidateMember(member); err != nil {
			return ParsedProject{}, fmt.Errorf("member %d: %w", idx+1, err)
		}
		project.ActiveMemberSHAs[member.PublicKeySHA] = true
	}

	return project, nil
}

func validateMetadataOnlyJSON(raw json.RawMessage) error {
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return err
	}
	return walkMetadata(value, "")
}

func walkMetadata(value any, path string) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if forbiddenMetadataKey(key) {
				return fmt.Errorf("forbidden field %q", childPath)
			}
			if err := walkMetadata(child, childPath); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range typed {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			if err := walkMetadata(child, childPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func forbiddenMetadataKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	switch normalized {
	case "value", "values", "env_value", "plaintext", "plaintext_value", "masked", "masked_value", "default", "default_value", "example", "example_value", "private_key", "signing_private_key", "encryption_private_key", "token", "access_token", "secret":
		return true
	default:
		return false
	}
}

func validatePublicIdentity(handle, publicKeySHA, signingPublicKey, encryptionPublicKey string) error {
	if strings.TrimSpace(handle) == "" {
		return fmt.Errorf("handle is required")
	}
	if strings.TrimSpace(publicKeySHA) == "" {
		return fmt.Errorf("public key SHA is required")
	}
	signingPublic, err := parseOpenSSHEd25519PublicKey(signingPublicKey)
	if err != nil {
		return fmt.Errorf("signing public key is invalid: %w", err)
	}
	if got := publicKeySHAFor(signingPublic); got != publicKeySHA {
		return fmt.Errorf("public key SHA mismatch: got %s from signing public key, config says %s", got, publicKeySHA)
	}
	if err := validateX25519PublicKey(encryptionPublicKey); err != nil {
		return fmt.Errorf("encryption public key is invalid: %w", err)
	}
	return nil
}

func parseOpenSSHEd25519PublicKey(value string) (ed25519.PublicKey, error) {
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

func publicKeySHAFor(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateX25519PublicKey(value string) error {
	if !strings.HasPrefix(value, "x25519:") {
		return errors.New("expected x25519: prefix")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "x25519:"))
	if err != nil {
		return fmt.Errorf("decode x25519 public key: %w", err)
	}
	if len(raw) != 32 {
		return fmt.Errorf("invalid x25519 public key length: got %d bytes", len(raw))
	}
	return nil
}

func sortedSnapshotScopes(values map[string]snapshotScope) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return orderedScopeNames(keys)
}

func orderedScopeNames(names []string) []string {
	rank := map[string]int{"dev": 0, "staging": 1, "prod": 2, "other": 3}
	sortKey := func(name string) string {
		return filepath.ToSlash(name)
	}
	out := append([]string(nil), names...)
	sort.Slice(out, func(i, j int) bool {
		left, lok := rank[out[i]]
		right, rok := rank[out[j]]
		if lok && rok {
			return left < right
		}
		if lok {
			return true
		}
		if rok {
			return false
		}
		return sortKey(out[i]) < sortKey(out[j])
	})
	return out
}
