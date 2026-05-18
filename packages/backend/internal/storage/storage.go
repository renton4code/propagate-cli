package storage

import (
	"context"
	"errors"
	"time"

	"propagate/backend/internal/domain"
)

var (
	ErrReplayRejected      = errors.New("replay rejected")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	ErrNotFound            = errors.New("not found")
	ErrPermissionDenied    = errors.New("permission denied")
	ErrRevisionConflict    = errors.New("revision conflict")
	ErrSecretConflict      = errors.New("secret version conflict")
	ErrValidation          = errors.New("validation error")
	ErrInvitePINInvalid    = errors.New("invite pin invalid")
	ErrInviteLocked        = errors.New("invite locked after failed pin attempts")
	ErrInviteNotActive     = errors.New("invite is not active")
)

type Store interface {
	ReserveNonce(ctx context.Context, publicKeySHA string, nonce string, expiresAt time.Time) error
	CreateTeamSetup(ctx context.Context, request domain.TeamSetupRequest, configHash string) (domain.SetupResult, error)
	GetMember(ctx context.Context, teamID string, publicKeySHA string) (domain.Member, error)
	ConfigStatus(ctx context.Context, teamID string, actor domain.Member, localRevision string, localConfigHash string) (domain.ConfigStatusData, error)
	GetConfig(ctx context.Context, teamID string, actor domain.Member, serverTime string) (domain.ConfigData, error)
	PushConfig(ctx context.Context, teamID string, actor domain.Member, request domain.ConfigPushRequest) (domain.ConfigPushResult, error)
	GetKeyEnvelope(ctx context.Context, teamID string, scope string, actor domain.Member) (domain.ScopeEnvelopeData, error)
	PullBundle(ctx context.Context, teamID string, scope string, actor domain.Member) (domain.PullBundleData, error)
	EnvPush(ctx context.Context, teamID string, scope string, actor domain.Member, request domain.EnvPushRequest) (domain.EnvPushResult, error)
	EnvStatus(ctx context.Context, teamID string, scope string, actor domain.Member) (domain.EnvStatusData, error)
	RecordPullEvent(ctx context.Context, teamID string, actor domain.Member, request domain.PullEventRequest) (domain.PullEventResult, error)
	TeamStatus(ctx context.Context, teamID string, actor domain.Member) (domain.TeamStatusData, error)
	CreateTeamInvite(ctx context.Context, teamID string, actor domain.Member, request domain.CreateTeamInviteRequest) (domain.CreateTeamInviteResult, error)
	ListJoinerInvites(ctx context.Context, teamID string) (domain.JoinerInvitesData, error)
	GetInviteScopeKeyBundle(ctx context.Context, teamID string, inviteID string) ([]domain.RelayScopeKey, error)
	SubmitInvitePIN(ctx context.Context, teamID string, inviteID string, request domain.InvitePINRequest, serverTime string, envelopes []domain.ScopeKeyEnvelope) (domain.InvitePINResult, error)
	ListAdminInvites(ctx context.Context, teamID string, actor domain.Member) (domain.AdminInvitesData, error)
	RevokeTeamInvite(ctx context.Context, teamID string, inviteID string, actor domain.Member) error
}
