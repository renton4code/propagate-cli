package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"propagate/backend/internal/domain"

	"golang.org/x/crypto/bcrypt"
)

func (s *SQLStore) CreateTeamInvite(ctx context.Context, teamID string, actor domain.Member, request domain.CreateTeamInviteRequest) (domain.CreateTeamInviteResult, error) {
	member, err := s.GetMember(ctx, teamID, actor.PublicKeySHA)
	if err != nil {
		return domain.CreateTeamInviteResult{}, err
	}
	if member.Role != "admins" {
		return domain.CreateTeamInviteResult{}, ErrPermissionDenied
	}

	role := strings.TrimSpace(request.RequestedRole)
	if role == "" {
		role = "developers"
	}
	var scopesArg interface{}
	if len(request.RequestedScopes) > 0 {
		scopesJSON, mErr := json.Marshal(request.RequestedScopes)
		if mErr != nil {
			return domain.CreateTeamInviteResult{}, mErr
		}
		scopesArg = scopesJSON
	}

	pin, err := domain.GenerateInvitePIN()
	if err != nil {
		return domain.CreateTeamInviteResult{}, err
	}
	normPIN, err := domain.NormalizeInvitePIN(pin)
	if err != nil {
		return domain.CreateTeamInviteResult{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(normPIN), bcrypt.DefaultCost)
	if err != nil {
		return domain.CreateTeamInviteResult{}, err
	}

	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return domain.CreateTeamInviteResult{}, err
	}
	inviteID := "inv_" + hex.EncodeToString(raw)

	var teamCheck int
	err = s.db.QueryRowContext(ctx, `select 1 from teams where id = $1`, teamID).Scan(&teamCheck)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.CreateTeamInviteResult{}, ErrNotFound
		}
		return domain.CreateTeamInviteResult{}, err
	}

	_, err = s.db.ExecContext(ctx, `
		insert into team_invites (
			id, team_id, label, pin_verifier, status, failed_pin_attempts,
			requested_role, requested_scopes, created_by_key_sha
		)
		values ($1, $2, $3, $4, 'active', 0, $5, $6::jsonb, $7)
	`, inviteID, teamID, strings.TrimSpace(request.Label), string(hash), role, scopesArg, actor.PublicKeySHA)
	if err != nil {
		return domain.CreateTeamInviteResult{}, err
	}

	return domain.CreateTeamInviteResult{
		InviteID: inviteID,
		PIN:      pin,
		Label:    strings.TrimSpace(request.Label),
	}, nil
}

func (s *SQLStore) ListJoinerInvites(ctx context.Context, teamID string) (domain.JoinerInvitesData, error) {
	var teamCheck int
	err := s.db.QueryRowContext(ctx, `select 1 from teams where id = $1`, teamID).Scan(&teamCheck)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.JoinerInvitesData{Invites: []domain.JoinerInviteRow{}}, nil
		}
		return domain.JoinerInvitesData{}, err
	}

	rows, err := s.db.QueryContext(ctx, `
		select id, label, created_at
		from team_invites
		where team_id = $1 and status = 'active'
		order by created_at asc
	`, teamID)
	if err != nil {
		return domain.JoinerInvitesData{}, err
	}
	defer rows.Close()

	var out []domain.JoinerInviteRow
	for rows.Next() {
		var id, label string
		var createdAt time.Time
		if err := rows.Scan(&id, &label, &createdAt); err != nil {
			return domain.JoinerInvitesData{}, err
		}
		out = append(out, domain.JoinerInviteRow{
			InviteID:  id,
			Label:     label,
			CreatedAt: createdAt.UTC().Format(time.RFC3339),
		})
	}
	return domain.JoinerInvitesData{Invites: out}, rows.Err()
}

func (s *SQLStore) SubmitInvitePIN(ctx context.Context, teamID string, inviteID string, request domain.InvitePINRequest, serverTime string) (domain.InvitePINResult, error) {
	normPIN, err := domain.NormalizeInvitePIN(request.PIN)
	if err != nil {
		return domain.InvitePINResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.InvitePINResult{}, err
	}
	defer tx.Rollback()

	var verifier string
	var status string
	var failed int
	err = tx.QueryRowContext(ctx, `
		select pin_verifier, status, failed_pin_attempts
		from team_invites
		where team_id = $1 and id = $2
		for update
	`, teamID, inviteID).Scan(&verifier, &status, &failed)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.InvitePINResult{}, ErrNotFound
		}
		return domain.InvitePINResult{}, err
	}
	if status != "active" {
		return domain.InvitePINResult{}, ErrInviteNotActive
	}

	if bcrypt.CompareHashAndPassword([]byte(verifier), []byte(normPIN)) == nil {
		raw := make([]byte, 10)
		if _, rerr := rand.Read(raw); rerr != nil {
			return domain.InvitePINResult{}, rerr
		}
		redemptionID := "red_" + hex.EncodeToString(raw)
		if _, err := tx.ExecContext(ctx, `
			update team_invites
			set status = 'redeemed', redeemed_at = now(), redeemed_by_key_sha = $3
			where team_id = $1 and id = $2
		`, teamID, inviteID, request.Joiner.PublicKeySHA); err != nil {
			return domain.InvitePINResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.InvitePINResult{}, err
		}
		return domain.InvitePINResult{
			RedemptionID: redemptionID,
			InviteID:     inviteID,
			ServerTime:   serverTime,
		}, nil
	}

	nextFailed := failed + 1
	newStatus := "active"
	finalErr := ErrInvitePINInvalid
	if nextFailed >= 3 {
		newStatus = "invalidated_pin"
		finalErr = ErrInviteLocked
	}
	if _, err := tx.ExecContext(ctx, `
		update team_invites
		set failed_pin_attempts = $3, status = $4
		where team_id = $1 and id = $2
	`, teamID, inviteID, nextFailed, newStatus); err != nil {
		return domain.InvitePINResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.InvitePINResult{}, err
	}
	return domain.InvitePINResult{}, finalErr
}

func (s *SQLStore) ListAdminInvites(ctx context.Context, teamID string, actor domain.Member) (domain.AdminInvitesData, error) {
	member, err := s.GetMember(ctx, teamID, actor.PublicKeySHA)
	if err != nil {
		return domain.AdminInvitesData{}, err
	}
	if member.Role != "admins" {
		return domain.AdminInvitesData{}, ErrPermissionDenied
	}

	rows, err := s.db.QueryContext(ctx, `
		select id, label, status, failed_pin_attempts, created_at, redeemed_at, redeemed_by_key_sha
		from team_invites
		where team_id = $1
		order by created_at asc
	`, teamID)
	if err != nil {
		return domain.AdminInvitesData{}, err
	}
	defer rows.Close()

	var out []domain.AdminInviteRow
	for rows.Next() {
		var row domain.AdminInviteRow
		var createdAt time.Time
		var redeemedAt sql.NullTime
		var redeemedBy sql.NullString
		if err := rows.Scan(&row.InviteID, &row.Label, &row.Status, &row.FailedPINAttempts, &createdAt, &redeemedAt, &redeemedBy); err != nil {
			return domain.AdminInvitesData{}, err
		}
		row.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		if redeemedAt.Valid {
			row.RedeemedAt = redeemedAt.Time.UTC().Format(time.RFC3339)
		}
		if redeemedBy.Valid {
			row.RedeemedByKeySHA = redeemedBy.String
		}
		out = append(out, row)
	}
	return domain.AdminInvitesData{Invites: out}, rows.Err()
}

func (s *SQLStore) RevokeTeamInvite(ctx context.Context, teamID string, inviteID string, actor domain.Member) error {
	member, err := s.GetMember(ctx, teamID, actor.PublicKeySHA)
	if err != nil {
		return err
	}
	if member.Role != "admins" {
		return ErrPermissionDenied
	}
	res, err := s.db.ExecContext(ctx, `
		update team_invites
		set status = 'revoked'
		where team_id = $1 and id = $2 and status = 'active'
	`, teamID, inviteID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrInviteNotActive
	}
	return nil
}
