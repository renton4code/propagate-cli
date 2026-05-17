package apiclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"propagate/cli/internal/identity"
)

const (
	HeaderPublicKeySHA = "X-Propagate-Public-Key-SHA"
	HeaderTimestamp    = "X-Propagate-Timestamp"
	HeaderNonce        = "X-Propagate-Nonce"
	HeaderCLIVersion   = "X-Propagate-CLI-Version"
	HeaderOperationID  = "X-Propagate-Operation-ID"
	HeaderSignature    = "X-Propagate-Signature"
)

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
	CLIVersion string
	Now        func() time.Time
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

type SetupResult struct {
	TeamID                  string   `json:"team_id"`
	ConfigRevision          string   `json:"config_revision"`
	ConfigHash              string   `json:"config_hash"`
	ScopesCreated           []string `json:"scopes_created"`
	EncryptedVariablesCount int      `json:"encrypted_variables_count"`
	EnvelopesCount          int      `json:"envelopes_count"`
}

type ConfigData struct {
	ConfigRevision string          `json:"config_revision"`
	ConfigHash     string          `json:"config_hash"`
	ConfigSnapshot json.RawMessage `json:"config_snapshot"`
	ServerTime     string          `json:"server_time,omitempty"`
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
	Permission   string `json:"permission,omitempty"`
}

type ScopeKeyEnvelope struct {
	Scope             string `json:"scope"`
	RecipientKeySHA   string `json:"recipient_key_sha"`
	ScopeKeyVersion   int    `json:"scope_key_version"`
	EncryptedScopeKey string `json:"encrypted_scope_key"`
	Algorithm         string `json:"algorithm"`
}

type ClientMetadata struct {
	CLIVersion string `json:"cli_version,omitempty"`
	ClientKind string `json:"client_kind,omitempty"`
}

// JoinerInviteRow is a non-secret invite listing entry for join-by-invite-code.
type JoinerInviteRow struct {
	InviteID  string `json:"invite_id"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
}

// JoinerInvitesData is returned by the unauthenticated join invites list endpoint.
type JoinerInvitesData struct {
	Invites []JoinerInviteRow `json:"invites"`
}

// CreateTeamInviteRequest creates a PIN-backed invite (admin only).
type CreateTeamInviteRequest struct {
	OperationID     string            `json:"operation_id"`
	Label             string            `json:"label"`
	RequestedRole     string            `json:"requested_role,omitempty"`
	RequestedScopes map[string]string `json:"requested_scopes,omitempty"`
	Client            ClientMetadata    `json:"client,omitempty"`
}

// CreateTeamInviteResult includes the PIN once at creation time.
type CreateTeamInviteResult struct {
	InviteID string `json:"invite_id"`
	PIN      string `json:"pin,omitempty"`
	Label    string `json:"label"`
}

// InvitePINRequest redeems an invite for a joiner's identity (signed).
type InvitePINRequest struct {
	OperationID     string            `json:"operation_id"`
	PIN             string            `json:"pin"`
	Joiner          PublicIdentity    `json:"joiner"`
	Handle          string            `json:"handle"`
	RequestedRole   string            `json:"requested_role,omitempty"`
	RequestedScopes map[string]string `json:"requested_scopes,omitempty"`
	Client          ClientMetadata    `json:"client,omitempty"`
}

// InvitePINResult is returned after a successful PIN verification.
type InvitePINResult struct {
	RedemptionID string `json:"redemption_id"`
	InviteID     string `json:"invite_id"`
	ServerTime   string `json:"server_time"`
}

// AdminInviteRow is an operational view of invite state (admin only).
type AdminInviteRow struct {
	InviteID           string `json:"invite_id"`
	Label              string `json:"label"`
	Status             string `json:"status"`
	FailedPINAttempts  int    `json:"failed_pin_attempts"`
	CreatedAt          string `json:"created_at"`
	RedeemedAt         string `json:"redeemed_at,omitempty"`
	RedeemedByKeySHA   string `json:"redeemed_by_key_sha,omitempty"`
}

// AdminInvitesData lists invites for a team (admin only).
type AdminInvitesData struct {
	Invites []AdminInviteRow `json:"invites"`
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

type EnvStatusData struct {
	Scope            ScopeRef              `json:"scope"`
	ConfigRevision   string                `json:"config_revision"`
	Variables        []VariableMetadata    `json:"variables,omitempty"`
	EncryptedValues  []SecretVersionRecord `json:"encrypted_values,omitempty"`
	ScopeKeyEnvelope *ScopeKeyEnvelope     `json:"scope_key_envelope,omitempty"`
	CanRead          bool                  `json:"can_read"`
}

type PublicIdentity struct {
	Handle              string `json:"handle"`
	PublicKeySHA        string `json:"public_key_sha"`
	SigningPublicKey    string `json:"signing_public_key"`
	EncryptionPublicKey string `json:"encryption_public_key"`
}

type Member struct {
	PublicIdentity
	Role   string `json:"role"`
	Status string `json:"status"`
}

type TeamSummary struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ConfigRevision string `json:"config_revision,omitempty"`
	ConfigHash     string `json:"config_hash,omitempty"`
}

type TeamStatusData struct {
	Team                  TeamSummary         `json:"team"`
	Actor                 Member              `json:"actor"`
	Members               map[string][]Member `json:"members"`
	PendingOrRecentAccess json.RawMessage     `json:"pending_or_recent_access,omitempty"`
	LastPulls             []PullActivity      `json:"last_pulls,omitempty"`
	NeverPulled           []Member            `json:"never_pulled,omitempty"`
}

type PullActivity struct {
	MemberPublicKeySHA string `json:"member_public_key_sha"`
	Handle             string `json:"handle,omitempty"`
	Scope              string `json:"scope,omitempty"`
	LastPulledAt       string `json:"last_pulled_at"`
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

type APIError struct {
	StatusCode int
	Code       string
	Message    string
	Retryable  bool
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

func (c Client) ConfigStatus(ctx context.Context, ident identity.Identity, teamID string, localRevision string, localHash string) (ConfigStatusData, error) {
	query := url.Values{}
	if localRevision != "" {
		query.Set("local_revision", localRevision)
	}
	if localHash != "" {
		query.Set("local_config_hash", localHash)
	}
	var out ConfigStatusData
	if err := c.do(ctx, ident, http.MethodGet, "/v1/teams/"+teamID+"/config/status", query.Encode(), nil, "", &out); err != nil {
		return ConfigStatusData{}, err
	}
	return out, nil
}

func (c Client) SetupTeam(ctx context.Context, ident identity.Identity, request TeamSetupRequest) (SetupResult, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return SetupResult{}, err
	}
	var out SetupResult
	if err := c.do(ctx, ident, http.MethodPost, "/v1/teams/setup", "", body, request.OperationID, &out); err != nil {
		return SetupResult{}, err
	}
	return out, nil
}

func (c Client) GetConfig(ctx context.Context, ident identity.Identity, teamID string) (ConfigData, error) {
	var out ConfigData
	if err := c.do(ctx, ident, http.MethodGet, "/v1/teams/"+teamID+"/config", "", nil, "", &out); err != nil {
		return ConfigData{}, err
	}
	return out, nil
}

func (c Client) PushConfig(ctx context.Context, ident identity.Identity, teamID string, request ConfigPushRequest) (ConfigPushResult, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return ConfigPushResult{}, err
	}
	var out ConfigPushResult
	if err := c.do(ctx, ident, http.MethodPost, "/v1/teams/"+teamID+"/config/push", "", body, request.OperationID, &out); err != nil {
		return ConfigPushResult{}, err
	}
	return out, nil
}

func (c Client) KeyEnvelope(ctx context.Context, ident identity.Identity, teamID string, scope string) (ScopeEnvelopeData, error) {
	var out ScopeEnvelopeData
	endpoint := "/v1/teams/" + url.PathEscape(teamID) + "/scopes/" + url.PathEscape(scope) + "/key-envelope"
	if err := c.do(ctx, ident, http.MethodGet, endpoint, "", nil, "", &out); err != nil {
		return ScopeEnvelopeData{}, err
	}
	return out, nil
}

func (c Client) PullBundle(ctx context.Context, ident identity.Identity, teamID string, scope string) (PullBundleData, error) {
	var out PullBundleData
	endpoint := "/v1/teams/" + url.PathEscape(teamID) + "/scopes/" + url.PathEscape(scope) + "/pull-bundle"
	if err := c.do(ctx, ident, http.MethodGet, endpoint, "", nil, "", &out); err != nil {
		return PullBundleData{}, err
	}
	return out, nil
}

func (c Client) RecordPullEvent(ctx context.Context, ident identity.Identity, teamID string, request PullEventRequest) (PullEventResult, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return PullEventResult{}, err
	}
	var out PullEventResult
	endpoint := "/v1/teams/" + url.PathEscape(teamID) + "/events/pull"
	if err := c.do(ctx, ident, http.MethodPost, endpoint, "", body, "", &out); err != nil {
		return PullEventResult{}, err
	}
	return out, nil
}

func (c Client) EnvPush(ctx context.Context, ident identity.Identity, teamID string, scope string, request EnvPushRequest) (EnvPushResult, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return EnvPushResult{}, err
	}
	var out EnvPushResult
	endpoint := "/v1/teams/" + url.PathEscape(teamID) + "/scopes/" + url.PathEscape(scope) + "/env/push"
	if err := c.do(ctx, ident, http.MethodPost, endpoint, "", body, request.OperationID, &out); err != nil {
		return EnvPushResult{}, err
	}
	return out, nil
}

func (c Client) EnvStatus(ctx context.Context, ident identity.Identity, teamID string, scope string) (EnvStatusData, error) {
	var out EnvStatusData
	endpoint := "/v1/teams/" + url.PathEscape(teamID) + "/scopes/" + url.PathEscape(scope) + "/env/status"
	if err := c.do(ctx, ident, http.MethodGet, endpoint, "", nil, "", &out); err != nil {
		return EnvStatusData{}, err
	}
	return out, nil
}

func (c Client) TeamStatus(ctx context.Context, ident identity.Identity, teamID string) (TeamStatusData, error) {
	var out TeamStatusData
	endpoint := "/v1/teams/" + url.PathEscape(teamID) + "/status"
	if err := c.do(ctx, ident, http.MethodGet, endpoint, "", nil, "", &out); err != nil {
		return TeamStatusData{}, err
	}
	return out, nil
}

func (c Client) GetJoinInvitesPublic(ctx context.Context, teamID string) (JoinerInvitesData, error) {
	var out JoinerInvitesData
	endpoint := "/v1/teams/" + url.PathEscape(teamID) + "/join/invites"
	if err := c.doPublic(ctx, http.MethodGet, endpoint, "", &out); err != nil {
		return JoinerInvitesData{}, err
	}
	return out, nil
}

func (c Client) SubmitInvitePIN(ctx context.Context, ident identity.Identity, teamID, inviteID string, request InvitePINRequest) (InvitePINResult, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return InvitePINResult{}, err
	}
	var out InvitePINResult
	endpoint := "/v1/teams/" + url.PathEscape(teamID) + "/join/invites/" + url.PathEscape(inviteID) + "/pin"
	if err := c.do(ctx, ident, http.MethodPost, endpoint, "", body, request.OperationID, &out); err != nil {
		return InvitePINResult{}, err
	}
	return out, nil
}

func (c Client) CreateTeamInvite(ctx context.Context, ident identity.Identity, teamID string, request CreateTeamInviteRequest) (CreateTeamInviteResult, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return CreateTeamInviteResult{}, err
	}
	var out CreateTeamInviteResult
	endpoint := "/v1/teams/" + url.PathEscape(teamID) + "/invites"
	if err := c.do(ctx, ident, http.MethodPost, endpoint, "", body, request.OperationID, &out); err != nil {
		return CreateTeamInviteResult{}, err
	}
	return out, nil
}

func (c Client) ListAdminInvites(ctx context.Context, ident identity.Identity, teamID string) (AdminInvitesData, error) {
	var out AdminInvitesData
	endpoint := "/v1/teams/" + url.PathEscape(teamID) + "/invites"
	if err := c.do(ctx, ident, http.MethodGet, endpoint, "", nil, "", &out); err != nil {
		return AdminInvitesData{}, err
	}
	return out, nil
}

func (c Client) RevokeTeamInvite(ctx context.Context, ident identity.Identity, teamID, inviteID string) error {
	body := []byte("{}")
	endpoint := "/v1/teams/" + url.PathEscape(teamID) + "/invites/" + url.PathEscape(inviteID) + "/revoke"
	return c.do(ctx, ident, http.MethodPost, endpoint, "", body, "", nil)
}

func (c Client) doPublic(ctx context.Context, method, endpoint, rawQuery string, out any) error {
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	target, err := c.resolveURL(endpoint, rawQuery)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return &APIError{Code: "cloud_unavailable", Message: err.Error(), Retryable: true}
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return &APIError{Code: "cloud_unavailable", Message: err.Error(), Retryable: true}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp.StatusCode, payload)
	}
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return &APIError{StatusCode: resp.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message, Retryable: envelope.Error.Retryable}
		}
		return &APIError{StatusCode: resp.StatusCode, Code: "cloud_unavailable", Message: "API returned an unsuccessful response", Retryable: true}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}

func (c Client) do(ctx context.Context, ident identity.Identity, method string, endpoint string, rawQuery string, body []byte, operationID string, out any) error {
	if len(body) == 0 {
		body = []byte{}
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	cliVersion := strings.TrimSpace(c.CLIVersion)
	if cliVersion == "" {
		cliVersion = "unknown"
	}
	target, err := c.resolveURL(endpoint, rawQuery)
	if err != nil {
		return err
	}

	timestamp := c.Now().UTC().Format(time.RFC3339)
	nonce, err := nonce()
	if err != nil {
		return err
	}
	metadata := signingMetadata{
		PublicKeySHA: ident.PublicKeySHA,
		Timestamp:    timestamp,
		Nonce:        nonce,
		CLIVersion:   cliVersion,
		OperationID:  operationID,
	}
	canonical := canonical(method, target.Path, target.RawQuery, body, metadata)
	signature, err := sign(ident, canonical)
	if err != nil {
		return err
	}
	metadata.Signature = signature

	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set(HeaderPublicKeySHA, metadata.PublicKeySHA)
	req.Header.Set(HeaderTimestamp, metadata.Timestamp)
	req.Header.Set(HeaderNonce, metadata.Nonce)
	req.Header.Set(HeaderCLIVersion, metadata.CLIVersion)
	req.Header.Set(HeaderOperationID, metadata.OperationID)
	req.Header.Set(HeaderSignature, metadata.Signature)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return &APIError{Code: "cloud_unavailable", Message: err.Error(), Retryable: true}
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return &APIError{Code: "cloud_unavailable", Message: err.Error(), Retryable: true}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp.StatusCode, payload)
	}
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return &APIError{StatusCode: resp.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message, Retryable: envelope.Error.Retryable}
		}
		return &APIError{StatusCode: resp.StatusCode, Code: "cloud_unavailable", Message: "API returned an unsuccessful response", Retryable: true}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}

func (c Client) resolveURL(endpoint string, rawQuery string) (*url.URL, error) {
	base := strings.TrimSpace(c.BaseURL)
	if base == "" {
		return nil, fmt.Errorf("API URL is required")
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("API URL must include scheme and host")
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	parsed.Path = basePath + endpoint
	parsed.RawQuery = rawQuery
	return parsed, nil
}

type signingMetadata struct {
	PublicKeySHA string
	Timestamp    string
	Nonce        string
	CLIVersion   string
	OperationID  string
	Signature    string
}

func canonical(method, path, rawQuery string, body []byte, metadata signingMetadata) string {
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

func sign(ident identity.Identity, canonical string) (string, error) {
	seed, err := base64.StdEncoding.DecodeString(ident.SigningPrivateKey)
	if err != nil {
		return "", fmt.Errorf("decode signing private key: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return "", fmt.Errorf("invalid signing private key length: got %d bytes", len(seed))
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	return base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(canonical))), nil
}

func nonce() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func decodeAPIError(status int, payload []byte) error {
	var envelope struct {
		Error *struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err == nil && envelope.Error != nil {
		return &APIError{StatusCode: status, Code: envelope.Error.Code, Message: envelope.Error.Message, Retryable: envelope.Error.Retryable}
	}
	return &APIError{StatusCode: status, Code: "cloud_unavailable", Message: fmt.Sprintf("API returned HTTP %d", status), Retryable: status >= 500}
}
