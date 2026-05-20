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
	if !domain.MemberCanManage(member) {
		return domain.CreateTeamInviteResult{}, ErrPermissionDenied
	}

	var scopesArg interface{}
	if len(request.RequestedScopes) > 0 {
		scopesJSON, mErr := json.Marshal(request.RequestedScopes)
		if mErr != nil {
			return domain.CreateTeamInviteResult{}, mErr
		}
		scopesArg = scopesJSON
	}
	var bundleArg interface{}
	if len(request.ScopeKeyBundle) > 0 {
		bundleJSON, mErr := json.Marshal(request.ScopeKeyBundle)
		if mErr != nil {
			return domain.CreateTeamInviteResult{}, mErr
		}
		bundleArg = bundleJSON
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
			requested_management, requested_scopes, created_by_key_sha,
			encrypted_scope_key_bundle, relay_key_version
		)
		values ($1, $2, $3, $4, 'active', 0, $5, $6::jsonb, $7, $8::jsonb, $9)
	`, inviteID, teamID, strings.TrimSpace(request.Label), string(hash), request.RequestedManagement, scopesArg, actor.PublicKeySHA, bundleArg, relayKeyVersion(request.ScopeKeyBundle))
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

func (s *SQLStore) GetInviteScopeKeyBundle(ctx context.Context, teamID string, inviteID string) ([]domain.RelayScopeKey, error) {
	var bundleRaw sql.NullString
	var status string
	err := s.db.QueryRowContext(ctx, `
		select status, encrypted_scope_key_bundle
		from team_invites
		where team_id = $1 and id = $2
	`, teamID, inviteID).Scan(&status, &bundleRaw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if status != "active" {
		return nil, ErrInviteNotActive
	}
	if !bundleRaw.Valid || bundleRaw.String == "" {
		return nil, nil
	}
	var bundle []domain.RelayScopeKey
	if err := json.Unmarshal([]byte(bundleRaw.String), &bundle); err != nil {
		return nil, err
	}
	return bundle, nil
}

func (s *SQLStore) SubmitInvitePIN(ctx context.Context, teamID string, inviteID string, request domain.InvitePINRequest, serverTime string, envelopes []domain.ScopeKeyEnvelope) (domain.InvitePINResult, error) {
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
		preApproved := len(envelopes) > 0
		if preApproved {
			var reqMgmt bool
			var reqScopes sql.NullString
			_ = tx.QueryRowContext(ctx, `
				select requested_management, requested_scopes
				from team_invites where team_id = $1 and id = $2
			`, teamID, inviteID).Scan(&reqMgmt, &reqScopes)

			if _, err := tx.ExecContext(ctx, `
				insert into members (
					team_id, handle, public_key_sha, signing_public_key, encryption_public_key,
					management, status, approved_at
				)
				values ($1, $2, $3, $4, $5, $6, 'active', now())
				on conflict (team_id, public_key_sha) do update set
					handle = excluded.handle,
					signing_public_key = excluded.signing_public_key,
					encryption_public_key = excluded.encryption_public_key,
					management = excluded.management,
					status = 'active',
					approved_at = now()
			`, teamID, request.Handle, request.Joiner.PublicKeySHA, request.Joiner.SigningPublicKey, request.Joiner.EncryptionPublicKey, reqMgmt); err != nil {
				return domain.InvitePINResult{}, err
			}

			if reqScopes.Valid && reqScopes.String != "" {
				var scopes map[string]string
				if json.Unmarshal([]byte(reqScopes.String), &scopes) == nil {
					for scopeName, permission := range scopes {
						var scopeID int64
						err := tx.QueryRowContext(ctx, `select id from scopes where team_id = $1 and name = $2`, teamID, scopeName).Scan(&scopeID)
						if err != nil {
							continue
						}
						_, _ = tx.ExecContext(ctx, `
							insert into scope_access_rules (team_id, scope_id, subject_type, subject_value, permission, config_revision, active)
							values ($1, $2, 'member', $3, $4, (select current_config_revision from teams where id = $1), true)
						`, teamID, scopeID, request.Joiner.PublicKeySHA, permission)
					}
				}
			}

			for _, env := range envelopes {
				var scopeID int64
				err := tx.QueryRowContext(ctx, `select id from scopes where team_id = $1 and name = $2`, teamID, env.Scope).Scan(&scopeID)
				if err != nil {
					continue
				}
				_, _ = tx.ExecContext(ctx, `
					insert into scope_key_envelopes (team_id, scope_id, recipient_key_sha, scope_key_version, encrypted_scope_key, algorithm, created_by_key_sha, config_revision)
					values ($1, $2, $3, $4, $5, $6, $3, (select current_config_revision from teams where id = $1))
				`, teamID, scopeID, env.RecipientKeySHA, env.ScopeKeyVersion, env.EncryptedScopeKey, env.Algorithm)
			}
		}
		if err := tx.Commit(); err != nil {
			return domain.InvitePINResult{}, err
		}
		return domain.InvitePINResult{
			RedemptionID:      redemptionID,
			InviteID:          inviteID,
			ServerTime:        serverTime,
			PreApproved:       preApproved,
			ScopeKeyEnvelopes: envelopes,
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
	if !domain.MemberCanManage(member) {
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
	if !domain.MemberCanManage(member) {
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

func relayKeyVersion(bundle []domain.RelayScopeKey) interface{} {
	if len(bundle) == 0 {
		return nil
	}
	return bundle[0].RelayKeyVersion
}
