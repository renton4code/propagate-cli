package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"propagate/backend/internal/signing"
)

var (
	scopeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	varNamePattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type VersionData struct {
	APIVersion            string          `json:"api_version"`
	MinCLIVersion         string          `json:"min_cli_version"`
	RecommendedCLIVersion string          `json:"recommended_cli_version,omitempty"`
	ServerTime            string          `json:"server_time"`
	Features              map[string]bool `json:"features,omitempty"`
}

type TeamSetupRequest struct {
	OperationID             string                   `json:"operation_id"`
	TeamName                string                   `json:"team_name"`
	FirstAdmin              PublicIdentity           `json:"first_admin"`
	ConfigSnapshot          json.RawMessage          `json:"config_snapshot"`
	Scopes                  []SetupScope             `json:"scopes"`
	EncryptedSecretVersions []EncryptedSecretVersion `json:"encrypted_secret_versions,omitempty"`
	ScopeKeyEnvelopes       []ScopeKeyEnvelope       `json:"scope_key_envelopes,omitempty"`
	Client                  ClientMetadata           `json:"client,omitempty"`
}

type PublicIdentity struct {
	Handle              string `json:"handle"`
	PublicKeySHA        string `json:"public_key_sha"`
	SigningPublicKey    string `json:"signing_public_key"`
	EncryptionPublicKey string `json:"encryption_public_key"`
}

type SetupScope struct {
	Name              string                `json:"name"`
	EnvFiles          []string              `json:"env_files,omitempty"`
	Variables         []VariableDeclaration `json:"variables,omitempty"`
	DefaultRoleAccess map[string]string     `json:"default_role_access,omitempty"`
}

type VariableDeclaration struct {
	Name        string `json:"name"`
	EnvFilePath string `json:"env_file_path"`
	Sensitivity string `json:"sensitivity"`
	Digest      string `json:"digest,omitempty"`
	Literal     string `json:"literal,omitempty"`
	Preview     string `json:"preview,omitempty"`
}

type EncryptedSecretVersion struct {
	Scope           string `json:"scope"`
	EnvFilePath     string `json:"env_file_path"`
	Name            string `json:"name"`
	Ciphertext      string `json:"ciphertext"`
	Nonce           string `json:"nonce"`
	Algorithm       string `json:"algorithm"`
	ScopeKeyVersion int    `json:"scope_key_version"`
}

type ScopeKeyEnvelope struct {
	Scope             string `json:"scope"`
	RecipientKeySHA   string `json:"recipient_key_sha"`
	ScopeKeyVersion   int    `json:"scope_key_version"`
	EncryptedScopeKey string `json:"encrypted_scope_key"`
	Algorithm         string `json:"algorithm"`
}

type ClientMetadata struct {
	CLIVersion   string `json:"cli_version,omitempty"`
	ClientKind   string `json:"client_kind,omitempty"`
	AgentAdapter string `json:"agent_adapter,omitempty"`
}

type SetupResult struct {
	TeamID                  string   `json:"team_id"`
	ConfigRevision          string   `json:"config_revision"`
	ConfigHash              string   `json:"config_hash"`
	ScopesCreated           []string `json:"scopes_created"`
	EncryptedVariablesCount int      `json:"encrypted_variables_count"`
	EnvelopesCount          int      `json:"envelopes_count"`
}

type StoredSetup struct {
	Request     TeamSetupRequest
	Result      SetupResult
	Fingerprint string
}

type Member struct {
	PublicIdentity
	Role       string            `json:"role,omitempty"`
	Management bool              `json:"management,omitempty"`
	Scopes     map[string]string `json:"scopes,omitempty"`
	Status     string            `json:"status"`
}

type TeamSummary struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ConfigRevision string `json:"config_revision,omitempty"`
	ConfigHash     string `json:"config_hash,omitempty"`
}

type ConfigStatusData struct {
	LocalRevision     string         `json:"local_revision,omitempty"`
	CloudRevision     string         `json:"cloud_revision"`
	LocalConfigHash   string         `json:"local_config_hash,omitempty"`
	CloudConfigHash   string         `json:"cloud_config_hash"`
	State             string         `json:"state"`
	RecommendedAction string         `json:"recommended_action"`
	SafeSummary       map[string]any `json:"safe_summary,omitempty"`
}

type ConfigData struct {
	ConfigRevision string          `json:"config_revision"`
	ConfigHash     string          `json:"config_hash"`
	ConfigSnapshot json.RawMessage `json:"config_snapshot"`
	ServerTime     string          `json:"server_time"`
}

type ConfigPushRequest struct {
	OperationID            string             `json:"operation_id"`
	ExpectedConfigRevision string             `json:"expected_config_revision"`
	TargetConfigSnapshot   json.RawMessage    `json:"target_config_snapshot"`
	Decisions              ConfigDecisions    `json:"decisions,omitempty"`
	ScopeKeyEnvelopes      []ScopeKeyEnvelope `json:"scope_key_envelopes,omitempty"`
	Client                 ClientMetadata     `json:"client,omitempty"`
}

type ConfigDecisions struct {
	Approved []ConfigDecision `json:"approved,omitempty"`
	Declined []ConfigDecision `json:"declined,omitempty"`
	Skipped  []ConfigDecision `json:"skipped,omitempty"`
}

type ConfigDecision struct {
	Type         string `json:"type,omitempty"`
	Handle       string `json:"handle,omitempty"`
	PublicKeySHA string `json:"public_key_sha,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Role         string `json:"role,omitempty"`
	Management   bool   `json:"management,omitempty"`
	Permission   string `json:"permission,omitempty"`
}

type ConfigPushResult struct {
	OldRevision      string          `json:"old_revision"`
	NewRevision      string          `json:"new_revision"`
	ConfigHash       string          `json:"config_hash"`
	AppliedDecisions ConfigDecisions `json:"applied_decisions,omitempty"`
	EnvelopesCount   int             `json:"envelopes_count"`
	AuditEventsCount int             `json:"audit_events_count"`
}

type ScopeRef struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

type ScopeEnvelopeData struct {
	Scope            ScopeRef         `json:"scope"`
	ConfigRevision   string           `json:"config_revision"`
	ScopeKeyVersion  int              `json:"scope_key_version"`
	ScopeKeyEnvelope ScopeKeyEnvelope `json:"scope_key_envelope"`
	Algorithm        string           `json:"algorithm"`
}

type PullBundleData struct {
	Scope            ScopeRef              `json:"scope"`
	ConfigRevision   string                `json:"config_revision"`
	EnvFileMappings  []string              `json:"env_file_mappings,omitempty"`
	ScopeKeyEnvelope ScopeKeyEnvelope      `json:"scope_key_envelope"`
	Variables        []VariableMetadata    `json:"variables,omitempty"`
	SecretVersions   []SecretVersionRecord `json:"secret_versions,omitempty"`
}

type VariableMetadata struct {
	Name             string `json:"name"`
	EnvFilePath      string `json:"env_file_path"`
	CurrentVersionID string `json:"current_version_id,omitempty"`
	LastUpdatedBy    string `json:"last_updated_by,omitempty"`
	LastUpdatedAt    string `json:"last_updated_at,omitempty"`
}

type SecretVersionRecord struct {
	Name             string `json:"name"`
	EnvFilePath      string `json:"env_file_path"`
	CurrentVersionID string `json:"current_version_id"`
	Ciphertext       string `json:"ciphertext"`
	Nonce            string `json:"nonce"`
	Algorithm        string `json:"algorithm"`
	ScopeKeyVersion  int    `json:"scope_key_version"`
}

type EnvPushRequest struct {
	OperationID            string           `json:"operation_id"`
	ExpectedConfigRevision string           `json:"expected_config_revision"`
	TargetConfigSnapshot   json.RawMessage  `json:"target_config_snapshot,omitempty"`
	Upserts                []EnvPushUpsert  `json:"upserts,omitempty"`
	Removals               []EnvPushRemoval `json:"removals,omitempty"`
	SafeCounts             SafeCounts       `json:"safe_counts,omitempty"`
	Client                 ClientMetadata   `json:"client,omitempty"`
}

type EnvPushUpsert struct {
	EnvFilePath       string `json:"env_file_path"`
	Name              string `json:"name"`
	Ciphertext        string `json:"ciphertext"`
	Nonce             string `json:"nonce"`
	Algorithm         string `json:"algorithm"`
	ScopeKeyVersion   int    `json:"scope_key_version"`
	ExpectedVersionID string `json:"expected_version_id,omitempty"`
}

type EnvPushRemoval struct {
	EnvFilePath       string `json:"env_file_path"`
	Name              string `json:"name"`
	ExpectedVersionID string `json:"expected_version_id,omitempty"`
}

type SafeCounts struct {
	Added   int `json:"added,omitempty"`
	Changed int `json:"changed,omitempty"`
	Removed int `json:"removed,omitempty"`
}

type EnvPushResult struct {
	CreatedVersions  []CreatedVersion  `json:"created_versions,omitempty"`
	RemovedVariables []RemovedVariable `json:"removed_variables,omitempty"`
	Conflicts        []SecretConflict  `json:"conflicts,omitempty"`
	ConfigRevision   string            `json:"config_revision,omitempty"`
	ConfigHash       string            `json:"config_hash,omitempty"`
	AuditEventsCount int               `json:"audit_events_count"`
}

type CreatedVersion struct {
	EnvFilePath string `json:"env_file_path"`
	Name        string `json:"name"`
	VersionID   string `json:"version_id"`
}

type RemovedVariable struct {
	EnvFilePath string `json:"env_file_path"`
	Name        string `json:"name"`
}

type SecretConflict struct {
	EnvFilePath       string `json:"env_file_path"`
	Name              string `json:"name"`
	ExpectedVersionID string `json:"expected_version_id,omitempty"`
	CurrentVersionID  string `json:"current_version_id,omitempty"`
}

type EnvStatusData struct {
	Scope            ScopeRef              `json:"scope"`
	ConfigRevision   string                `json:"config_revision"`
	Variables        []VariableMetadata    `json:"variables,omitempty"`
	EncryptedValues  []SecretVersionRecord `json:"encrypted_values,omitempty"`
	ScopeKeyEnvelope *ScopeKeyEnvelope     `json:"scope_key_envelope,omitempty"`
	CanRead          bool                  `json:"can_read"`
}

type PullEventRequest struct {
	Scope          string         `json:"scope"`
	EnvFilePaths   []string       `json:"env_file_paths,omitempty"`
	ConfigRevision string         `json:"config_revision"`
	VariablesCount int            `json:"variables_count"`
	Client         ClientMetadata `json:"client,omitempty"`
}

type PullEventResult struct {
	EventID       string `json:"event_id,omitempty"`
	RecordedCount int    `json:"recorded_count"`
}

type TeamStatusData struct {
	Team                  TeamSummary         `json:"team"`
	Actor                 Member              `json:"actor"`
	Members               map[string][]Member `json:"members"`
	PendingJoinRequests   []JoinRequestRow    `json:"pending_join_requests,omitempty"`
	PendingOrRecentAccess json.RawMessage     `json:"pending_or_recent_access,omitempty"`
	LastPulls             []PullActivity      `json:"last_pulls,omitempty"`
	NeverPulled           []Member            `json:"never_pulled,omitempty"`
}

type JoinRequestSubmission struct {
	OperationID         string            `json:"operation_id"`
	Joiner              PublicIdentity    `json:"joiner"`
	RequestedRole       string            `json:"requested_role,omitempty"`
	RequestedManagement bool              `json:"requested_management,omitempty"`
	RequestedScopes     map[string]string `json:"requested_scopes,omitempty"`
	Client              ClientMetadata    `json:"client,omitempty"`
}

type JoinRequestRow struct {
	Handle              string            `json:"handle"`
	PublicKeySHA        string            `json:"public_key_sha"`
	SigningPublicKey    string            `json:"signing_public_key"`
	EncryptionPublicKey string            `json:"encryption_public_key"`
	RequestedRole       string            `json:"requested_role,omitempty"`
	RequestedManagement bool              `json:"requested_management,omitempty"`
	RequestedScopes     map[string]string `json:"requested_scopes,omitempty"`
	CreatedAt           string            `json:"created_at"`
}

type PendingJoinRequestsData struct {
	Requests []JoinRequestRow `json:"requests"`
}

type ApproveJoinRequestBody struct {
	OperationID       string             `json:"operation_id"`
	ScopeKeyEnvelopes []ScopeKeyEnvelope `json:"scope_key_envelopes"`
	GrantedRole       string             `json:"granted_role,omitempty"`
	GrantedManagement bool               `json:"granted_management,omitempty"`
	GrantedScopes     map[string]string  `json:"granted_scopes,omitempty"`
	Client            ClientMetadata     `json:"client,omitempty"`
}

type ApproveJoinResult struct {
	MemberPublicKeySHA string `json:"member_public_key_sha"`
	ConfigRevision     string `json:"config_revision"`
	ConfigHash         string `json:"config_hash"`
}

type DeclineJoinRequestBody struct {
	OperationID string         `json:"operation_id"`
	Client      ClientMetadata `json:"client,omitempty"`
}

type PullActivity struct {
	MemberPublicKeySHA string `json:"member_public_key_sha"`
	Handle             string `json:"handle,omitempty"`
	Scope              string `json:"scope,omitempty"`
	LastPulledAt       string `json:"last_pulled_at"`
}

func (r TeamSetupRequest) Validate() error {
	if strings.TrimSpace(r.OperationID) == "" {
		return errors.New("operation_id is required")
	}
	if strings.TrimSpace(r.TeamName) == "" {
		return errors.New("team_name is required")
	}
	if err := r.FirstAdmin.Validate(); err != nil {
		return fmt.Errorf("first_admin: %w", err)
	}
	if len(bytes.TrimSpace(r.ConfigSnapshot)) == 0 {
		return errors.New("config_snapshot is required")
	}
	if err := ValidateMetadataOnlyJSON(r.ConfigSnapshot); err != nil {
		return fmt.Errorf("config_snapshot: %w", err)
	}
	if err := ValidateConfigSnapshot(r.ConfigSnapshot); err != nil {
		return fmt.Errorf("config_snapshot: %w", err)
	}
	if len(r.Scopes) == 0 {
		return errors.New("at least one scope is required")
	}
	seenScopes := map[string]bool{}
	for _, scope := range r.Scopes {
		if err := scope.Validate(); err != nil {
			return err
		}
		if seenScopes[scope.Name] {
			return fmt.Errorf("duplicate scope %q", scope.Name)
		}
		seenScopes[scope.Name] = true
	}
	for _, version := range r.EncryptedSecretVersions {
		if err := version.Validate(seenScopes); err != nil {
			return err
		}
	}
	for _, envelope := range r.ScopeKeyEnvelopes {
		if err := envelope.Validate(seenScopes); err != nil {
			return err
		}
	}
	return nil
}

func (r ConfigPushRequest) Validate() error {
	if strings.TrimSpace(r.OperationID) == "" {
		return errors.New("operation_id is required")
	}
	if _, err := RevisionNumber(r.ExpectedConfigRevision); err != nil {
		return fmt.Errorf("expected_config_revision: %w", err)
	}
	if len(bytes.TrimSpace(r.TargetConfigSnapshot)) == 0 {
		return errors.New("target_config_snapshot is required")
	}
	if err := ValidateMetadataOnlyJSON(r.TargetConfigSnapshot); err != nil {
		return fmt.Errorf("target_config_snapshot: %w", err)
	}
	if err := ValidateConfigSnapshot(r.TargetConfigSnapshot); err != nil {
		return fmt.Errorf("target_config_snapshot: %w", err)
	}
	for _, decision := range append(append([]ConfigDecision{}, r.Decisions.Approved...), append(r.Decisions.Declined, r.Decisions.Skipped...)...) {
		if err := decision.Validate(); err != nil {
			return err
		}
	}
	for _, envelope := range r.ScopeKeyEnvelopes {
		if err := envelope.ValidateShape(); err != nil {
			return err
		}
	}
	return nil
}

func (d ConfigDecision) Validate() error {
	if d.Type != "" {
		switch d.Type {
		case "join", "role_change", "scope_access_change":
		default:
			return fmt.Errorf("unsupported decision type %q", d.Type)
		}
	}
	if d.PublicKeySHA != "" && !strings.HasPrefix(d.PublicKeySHA, "sha256:") {
		return fmt.Errorf("invalid decision public_key_sha %q", d.PublicKeySHA)
	}
	if d.Scope != "" && !scopeNamePattern.MatchString(d.Scope) {
		return fmt.Errorf("invalid decision scope %q", d.Scope)
	}
	if d.Role != "" {
		switch d.Role {
		case "admins", "developers":
		default:
			return fmt.Errorf("unsupported decision role %q", d.Role)
		}
	}
	if d.Permission != "" && !validPermission(d.Permission) {
		return fmt.Errorf("unsupported decision permission %q", d.Permission)
	}
	return nil
}

func (r EnvPushRequest) Validate() error {
	if strings.TrimSpace(r.OperationID) == "" {
		return errors.New("operation_id is required")
	}
	if _, err := RevisionNumber(r.ExpectedConfigRevision); err != nil {
		return fmt.Errorf("expected_config_revision: %w", err)
	}
	if len(r.TargetConfigSnapshot) > 0 {
		if err := ValidateMetadataOnlyJSON(r.TargetConfigSnapshot); err != nil {
			return err
		}
		if err := ValidateConfigSnapshot(r.TargetConfigSnapshot); err != nil {
			return err
		}
	}
	if len(r.Upserts) == 0 && len(r.Removals) == 0 && len(r.TargetConfigSnapshot) == 0 {
		return errors.New("at least one upsert, removal, or target_config_snapshot is required")
	}
	for _, upsert := range r.Upserts {
		if err := upsert.Validate(); err != nil {
			return err
		}
	}
	for _, removal := range r.Removals {
		if err := removal.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (u EnvPushUpsert) Validate() error {
	if err := validateEnvPath(u.EnvFilePath); err != nil {
		return err
	}
	if !varNamePattern.MatchString(u.Name) {
		return fmt.Errorf("invalid variable name %q", u.Name)
	}
	if strings.TrimSpace(u.Ciphertext) == "" {
		return fmt.Errorf("env push upsert %s: ciphertext is required", u.Name)
	}
	if strings.TrimSpace(u.Nonce) == "" {
		return fmt.Errorf("env push upsert %s: nonce is required", u.Name)
	}
	if strings.TrimSpace(u.Algorithm) == "" {
		return fmt.Errorf("env push upsert %s: algorithm is required", u.Name)
	}
	if u.ScopeKeyVersion <= 0 {
		return fmt.Errorf("env push upsert %s: scope_key_version must be positive", u.Name)
	}
	return nil
}

func (r EnvPushRemoval) Validate() error {
	if err := validateEnvPath(r.EnvFilePath); err != nil {
		return err
	}
	if !varNamePattern.MatchString(r.Name) {
		return fmt.Errorf("invalid variable name %q", r.Name)
	}
	return nil
}

func (r PullEventRequest) Validate() error {
	if !scopeNamePattern.MatchString(r.Scope) {
		return fmt.Errorf("invalid scope name %q", r.Scope)
	}
	if _, err := RevisionNumber(r.ConfigRevision); err != nil {
		return fmt.Errorf("config_revision: %w", err)
	}
	if r.VariablesCount < 0 {
		return errors.New("variables_count cannot be negative")
	}
	for _, path := range r.EnvFilePaths {
		if err := validateEnvPath(path); err != nil {
			return err
		}
	}
	return nil
}

func (r JoinRequestSubmission) Validate() error {
	if strings.TrimSpace(r.OperationID) == "" {
		return errors.New("operation_id is required")
	}
	if err := r.Joiner.Validate(); err != nil {
		return fmt.Errorf("joiner: %w", err)
	}
	if r.RequestedRole != "" {
		switch r.RequestedRole {
		case "admins", "developers":
		default:
			return fmt.Errorf("unsupported requested_role %q", r.RequestedRole)
		}
	}
	for scope, permission := range r.RequestedScopes {
		if !scopeNamePattern.MatchString(scope) {
			return fmt.Errorf("invalid scope name %q", scope)
		}
		if !validPermission(permission) {
			return fmt.Errorf("unsupported permission %q for scope %q", permission, scope)
		}
	}
	return nil
}

func (r ApproveJoinRequestBody) Validate() error {
	if strings.TrimSpace(r.OperationID) == "" {
		return errors.New("operation_id is required")
	}
	if r.GrantedRole != "" {
		switch r.GrantedRole {
		case "admins", "developers":
		default:
			return fmt.Errorf("unsupported granted_role %q", r.GrantedRole)
		}
	}
	for scope, permission := range r.GrantedScopes {
		if !scopeNamePattern.MatchString(scope) {
			return fmt.Errorf("invalid scope name %q", scope)
		}
		if !validPermission(permission) {
			return fmt.Errorf("unsupported permission %q for scope %q", permission, scope)
		}
	}
	for _, envelope := range r.ScopeKeyEnvelopes {
		if err := envelope.ValidateShape(); err != nil {
			return err
		}
	}
	return nil
}

func (r DeclineJoinRequestBody) Validate() error {
	if strings.TrimSpace(r.OperationID) == "" {
		return errors.New("operation_id is required")
	}
	return nil
}

func (i PublicIdentity) Validate() error {
	if strings.TrimSpace(i.Handle) == "" {
		return errors.New("handle is required")
	}
	if strings.TrimSpace(i.PublicKeySHA) == "" {
		return errors.New("public_key_sha is required")
	}
	if strings.TrimSpace(i.SigningPublicKey) == "" {
		return errors.New("signing_public_key is required")
	}
	if strings.TrimSpace(i.EncryptionPublicKey) == "" {
		return errors.New("encryption_public_key is required")
	}
	publicKey, err := signing.ParseOpenSSHEd25519PublicKey(i.SigningPublicKey)
	if err != nil {
		return err
	}
	if got := signing.PublicKeySHA(publicKey); got != i.PublicKeySHA {
		return fmt.Errorf("public_key_sha mismatch: got %s from signing public key, request says %s", got, i.PublicKeySHA)
	}
	if !strings.HasPrefix(i.EncryptionPublicKey, "x25519:") {
		return errors.New("encryption_public_key must use x25519: format")
	}
	return nil
}

func (s SetupScope) Validate() error {
	if !scopeNamePattern.MatchString(s.Name) {
		return fmt.Errorf("invalid scope name %q", s.Name)
	}
	for _, path := range s.EnvFiles {
		if err := validateEnvPath(path); err != nil {
			return fmt.Errorf("scope %s: %w", s.Name, err)
		}
	}
	envFiles := map[string]bool{}
	for _, path := range s.EnvFiles {
		envFiles[path] = true
	}
	for _, variable := range s.Variables {
		if err := variable.Validate(envFiles); err != nil {
			return fmt.Errorf("scope %s: %w", s.Name, err)
		}
	}
	for role, permission := range s.DefaultRoleAccess {
		switch role {
		case "admins", "developers":
		default:
			return fmt.Errorf("scope %s: unsupported role %q", s.Name, role)
		}
		if !validPermission(permission) {
			return fmt.Errorf("scope %s: unsupported permission %q", s.Name, permission)
		}
	}
	return nil
}

func (v VariableDeclaration) Validate(envFiles map[string]bool) error {
	if !varNamePattern.MatchString(v.Name) {
		return fmt.Errorf("invalid variable name %q", v.Name)
	}
	if err := validateEnvPath(v.EnvFilePath); err != nil {
		return err
	}
	if len(envFiles) > 0 && !envFiles[v.EnvFilePath] {
		return fmt.Errorf("variable %s references unlisted env file %s", v.Name, v.EnvFilePath)
	}
	sensitivity := v.Sensitivity
	if sensitivity == "" {
		sensitivity = "sensitive"
	}
	switch sensitivity {
	case "sensitive":
		if v.Literal != "" || v.Preview != "" {
			return fmt.Errorf("sensitive variable %s cannot include literal or preview", v.Name)
		}
	case "non_sensitive":
		if v.Literal != "" && v.Preview != "" {
			return fmt.Errorf("non-sensitive variable %s cannot include both literal and preview", v.Name)
		}
	default:
		return fmt.Errorf("unsupported sensitivity %q", sensitivity)
	}
	if v.Digest != "" && !strings.Contains(v.Digest, ":") {
		return fmt.Errorf("variable %s digest must include an algorithm prefix", v.Name)
	}
	return nil
}

func (v EncryptedSecretVersion) Validate(scopes map[string]bool) error {
	if !scopes[v.Scope] {
		return fmt.Errorf("encrypted secret references unknown scope %q", v.Scope)
	}
	if err := validateEnvPath(v.EnvFilePath); err != nil {
		return fmt.Errorf("encrypted secret %s: %w", v.Name, err)
	}
	if !varNamePattern.MatchString(v.Name) {
		return fmt.Errorf("invalid variable name %q", v.Name)
	}
	if strings.TrimSpace(v.Ciphertext) == "" {
		return fmt.Errorf("encrypted secret %s: ciphertext is required", v.Name)
	}
	if strings.TrimSpace(v.Nonce) == "" {
		return fmt.Errorf("encrypted secret %s: nonce is required", v.Name)
	}
	if strings.TrimSpace(v.Algorithm) == "" {
		return fmt.Errorf("encrypted secret %s: algorithm is required", v.Name)
	}
	if v.ScopeKeyVersion <= 0 {
		return fmt.Errorf("encrypted secret %s: scope_key_version must be positive", v.Name)
	}
	return nil
}

func (e ScopeKeyEnvelope) Validate(scopes map[string]bool) error {
	if !scopes[e.Scope] {
		return fmt.Errorf("scope key envelope references unknown scope %q", e.Scope)
	}
	return e.ValidateShape()
}

func (e ScopeKeyEnvelope) ValidateShape() error {
	if !scopeNamePattern.MatchString(e.Scope) {
		return fmt.Errorf("invalid scope name %q", e.Scope)
	}
	if strings.TrimSpace(e.RecipientKeySHA) == "" {
		return errors.New("scope key envelope recipient_key_sha is required")
	}
	if strings.TrimSpace(e.EncryptedScopeKey) == "" {
		return errors.New("scope key envelope encrypted_scope_key is required")
	}
	if strings.TrimSpace(e.Algorithm) == "" {
		return errors.New("scope key envelope algorithm is required")
	}
	if e.ScopeKeyVersion <= 0 {
		return errors.New("scope key envelope scope_key_version must be positive")
	}
	return nil
}

func RevisionString(revision int) string {
	return fmt.Sprintf("rev_%05d", revision)
}

func RevisionNumber(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("revision is required")
	}
	value = strings.TrimPrefix(value, "rev_")
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid revision %q", value)
	}
	return parsed, nil
}

func PermissionAllows(actual string, required string) bool {
	return permissionRank(actual) >= permissionRank(required)
}

func MemberCanManage(member Member) bool {
	return member.Management || member.Role == "admins"
}

func permissionRank(permission string) int {
	switch permission {
	case "admin":
		return 3
	case "write":
		return 2
	case "read":
		return 1
	default:
		return 0
	}
}

func ConfigHash(snapshot json.RawMessage) (string, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, snapshot); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func CanonicalSetupConfigSnapshot(request TeamSetupRequest, teamID string) (json.RawMessage, string, error) {
	type canonicalScope struct {
		EnvFiles  []string              `json:"env_files"`
		Variables []VariableDeclaration `json:"variables,omitempty"`
	}
	type canonicalTeam struct {
		ID   string `json:"id,omitempty"`
		Name string `json:"name"`
	}
	type canonicalMember struct {
		Handle              string            `json:"handle"`
		PublicKeySHA        string            `json:"public_key_sha"`
		SigningPublicKey    string            `json:"signing_public_key"`
		EncryptionPublicKey string            `json:"encryption_public_key"`
		Management          bool              `json:"management,omitempty"`
		Scopes              map[string]string `json:"scopes,omitempty"`
	}
	type canonicalPending struct {
		Joins         []any `json:"joins"`
		AccessChanges []any `json:"access_changes"`
	}
	type canonicalSnapshot struct {
		Version int                       `json:"version"`
		Team    canonicalTeam             `json:"team"`
		Scopes  map[string]canonicalScope `json:"scopes"`
		Members []canonicalMember         `json:"members"`
		Pending canonicalPending          `json:"pending"`
	}

	scopes := map[string]canonicalScope{}
	adminScopes := map[string]string{}
	for _, scope := range request.Scopes {
		scopes[scope.Name] = canonicalScope{
			EnvFiles:  append([]string{}, scope.EnvFiles...),
			Variables: append([]VariableDeclaration{}, scope.Variables...),
		}
		adminScopes[scope.Name] = "write"
	}
	snapshot := canonicalSnapshot{
		Version: 1,
		Team: canonicalTeam{
			ID:   teamID,
			Name: request.TeamName,
		},
		Scopes: scopes,
		Members: []canonicalMember{{
			Handle:              request.FirstAdmin.Handle,
			PublicKeySHA:        request.FirstAdmin.PublicKeySHA,
			SigningPublicKey:    request.FirstAdmin.SigningPublicKey,
			EncryptionPublicKey: request.FirstAdmin.EncryptionPublicKey,
			Management:          true,
			Scopes:              adminScopes,
		}},
		Pending: canonicalPending{
			Joins:         []any{},
			AccessChanges: []any{},
		},
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return nil, "", err
	}
	if err := ValidateMetadataOnlyJSON(payload); err != nil {
		return nil, "", err
	}
	if err := ValidateConfigSnapshot(payload); err != nil {
		return nil, "", err
	}
	hash, err := ConfigHash(payload)
	if err != nil {
		return nil, "", err
	}
	return json.RawMessage(payload), hash, nil
}

func Fingerprint(request TeamSetupRequest) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func FingerprintConfigPush(request ConfigPushRequest) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func FingerprintEnvPush(request EnvPushRequest) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func ValidateMetadataOnlyJSON(raw json.RawMessage) error {
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return err
	}
	return walkMetadata(value, "")
}

func ValidateConfigSnapshot(raw json.RawMessage) error {
	var snapshot struct {
		Version int `json:"version"`
		Team    struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"team"`
		Scopes  map[string]SetupScope `json:"scopes"`
		Members []struct {
			Handle              string            `json:"handle"`
			PublicKeySHA        string            `json:"public_key_sha"`
			SigningPublicKey    string            `json:"signing_public_key"`
			EncryptionPublicKey string            `json:"encryption_public_key"`
			Role                string            `json:"role"`
			Management          bool              `json:"management"`
			Scopes              map[string]string `json:"scopes"`
		} `json:"members"`
		Pending struct {
			Joins []struct {
				Handle              string            `json:"handle"`
				PublicKeySHA        string            `json:"public_key_sha"`
				SigningPublicKey    string            `json:"signing_public_key"`
				EncryptionPublicKey string            `json:"encryption_public_key"`
				RequestedRole       string            `json:"requested_role"`
				RequestedManagement bool              `json:"requested_management"`
				RequestedScopes     map[string]string `json:"requested_scopes"`
			} `json:"joins"`
		} `json:"pending"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}
	if snapshot.Version != 1 {
		return fmt.Errorf("unsupported config version %d", snapshot.Version)
	}
	if strings.TrimSpace(snapshot.Team.Name) == "" {
		return errors.New("team.name is required")
	}
	for name, scope := range snapshot.Scopes {
		if scope.Name == "" {
			scope.Name = name
		}
		if scope.Name != name {
			return fmt.Errorf("scope key %q does not match scope.name %q", name, scope.Name)
		}
		if err := scope.Validate(); err != nil {
			return err
		}
	}
	for idx, member := range snapshot.Members {
		if err := (PublicIdentity{
			Handle:              member.Handle,
			PublicKeySHA:        member.PublicKeySHA,
			SigningPublicKey:    member.SigningPublicKey,
			EncryptionPublicKey: member.EncryptionPublicKey,
		}).Validate(); err != nil {
			return fmt.Errorf("member %d: %w", idx+1, err)
		}
		if err := ValidateRole(member.Role); err != nil {
			return fmt.Errorf("member %d: %w", idx+1, err)
		}
		for scope, permission := range member.Scopes {
			if !scopeNamePattern.MatchString(scope) {
				return fmt.Errorf("member %d: invalid scope %q", idx+1, scope)
			}
			if !validPermission(permission) {
				return fmt.Errorf("member %d: unsupported permission %q", idx+1, permission)
			}
		}
	}
	for idx, join := range snapshot.Pending.Joins {
		if err := (PublicIdentity{
			Handle:              join.Handle,
			PublicKeySHA:        join.PublicKeySHA,
			SigningPublicKey:    join.SigningPublicKey,
			EncryptionPublicKey: join.EncryptionPublicKey,
		}).Validate(); err != nil {
			return fmt.Errorf("pending join %d: %w", idx+1, err)
		}
		if err := ValidateRole(join.RequestedRole); err != nil {
			return fmt.Errorf("pending join %d: %w", idx+1, err)
		}
		for scope, permission := range join.RequestedScopes {
			if !scopeNamePattern.MatchString(scope) {
				return fmt.Errorf("pending join %d: invalid requested scope %q", idx+1, scope)
			}
			if !validPermission(permission) {
				return fmt.Errorf("pending join %d: unsupported requested permission %q", idx+1, permission)
			}
		}
	}
	return nil
}

func ConfigSnapshotTeamID(raw json.RawMessage) (string, error) {
	var snapshot struct {
		Team struct {
			ID string `json:"id"`
		} `json:"team"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return "", err
	}
	return strings.TrimSpace(snapshot.Team.ID), nil
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

func validateEnvPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("env file path is required")
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if filepath.IsAbs(path) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("env file path must be repo-relative and inside the worktree: %s", path)
	}
	return nil
}

func validPermission(permission string) bool {
	switch permission {
	case "none", "read", "write", "admin":
		return true
	default:
		return false
	}
}

// ValidateRole checks requested Propagate member roles.
func ValidateRole(role string) error {
	switch role {
	case "", "admins", "developers":
		return nil
	default:
		return fmt.Errorf("unsupported role %q", role)
	}
}

// ValidateScopeName checks scope keys in config and invite metadata.
func ValidateScopeName(name string) error {
	if !scopeNamePattern.MatchString(name) {
		return fmt.Errorf("invalid scope name %q", name)
	}
	return nil
}

// ValidatePermission checks scope access permission strings.
func ValidatePermission(permission string) error {
	if validPermission(permission) {
		return nil
	}
	return fmt.Errorf("unsupported permission %q", permission)
}
