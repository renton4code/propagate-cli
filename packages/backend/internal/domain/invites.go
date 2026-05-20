package domain

import (
	"fmt"
	"strings"
)

type RelayScopeKey struct {
	Scope             string `json:"scope"`
	EncryptedScopeKey string `json:"encrypted_scope_key"`
	Algorithm         string `json:"algorithm"`
	ScopeKeyVersion   int    `json:"scope_key_version"`
	RelayKeyVersion   int    `json:"relay_key_version"`
}

type CreateTeamInviteRequest struct {
	OperationID         string            `json:"operation_id"`
	Label               string            `json:"label"`
	RequestedManagement bool              `json:"requested_management,omitempty"`
	RequestedScopes     map[string]string `json:"requested_scopes,omitempty"`
	ScopeKeyBundle      []RelayScopeKey   `json:"scope_key_bundle,omitempty"`
	Client              ClientMetadata    `json:"client,omitempty"`
}

func (r CreateTeamInviteRequest) Validate() error {
	if strings.TrimSpace(r.OperationID) == "" {
		return fmt.Errorf("operation_id is required")
	}
	if strings.TrimSpace(r.Label) == "" {
		return fmt.Errorf("label is required")
	}
	if len(r.Label) > 200 {
		return fmt.Errorf("label is too long")
	}
	for scope, perm := range r.RequestedScopes {
		if err := ValidateScopeName(scope); err != nil {
			return err
		}
		if err := ValidatePermission(perm); err != nil {
			return err
		}
	}
	return nil
}

type CreateTeamInviteResult struct {
	InviteID string `json:"invite_id"`
	PIN      string `json:"pin,omitempty"`
	Label    string `json:"label"`
}

type JoinerInviteRow struct {
	InviteID  string `json:"invite_id"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
}

type JoinerInvitesData struct {
	Invites []JoinerInviteRow `json:"invites"`
}

type AdminInviteRow struct {
	InviteID          string `json:"invite_id"`
	Label             string `json:"label"`
	Status            string `json:"status"`
	FailedPINAttempts int    `json:"failed_pin_attempts"`
	CreatedAt         string `json:"created_at"`
	RedeemedAt        string `json:"redeemed_at,omitempty"`
	RedeemedByKeySHA  string `json:"redeemed_by_key_sha,omitempty"`
}

type AdminInvitesData struct {
	Invites []AdminInviteRow `json:"invites"`
}

type InvitePINRequest struct {
	OperationID         string            `json:"operation_id"`
	PIN                 string            `json:"pin"`
	Joiner              PublicIdentity    `json:"joiner"`
	Handle              string            `json:"handle"`
	RequestedManagement bool              `json:"requested_management,omitempty"`
	RequestedScopes     map[string]string `json:"requested_scopes,omitempty"`
	Client              ClientMetadata    `json:"client,omitempty"`
}

func (r InvitePINRequest) Validate() error {
	if strings.TrimSpace(r.OperationID) == "" {
		return fmt.Errorf("operation_id is required")
	}
	if _, err := NormalizeInvitePIN(r.PIN); err != nil {
		return err
	}
	if err := r.Joiner.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Handle) == "" {
		return fmt.Errorf("handle is required")
	}
	for scope, perm := range r.RequestedScopes {
		if err := ValidateScopeName(scope); err != nil {
			return err
		}
		if err := ValidatePermission(perm); err != nil {
			return err
		}
	}
	return nil
}

type InvitePINResult struct {
	RedemptionID      string             `json:"redemption_id"`
	InviteID          string             `json:"invite_id"`
	ServerTime        string             `json:"server_time"`
	PreApproved       bool               `json:"pre_approved,omitempty"`
	ScopeKeyEnvelopes []ScopeKeyEnvelope `json:"scope_key_envelopes,omitempty"`
	Member            *Member            `json:"member,omitempty"`
}
