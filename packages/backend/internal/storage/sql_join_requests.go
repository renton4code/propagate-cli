package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"propagate/backend/internal/domain"
)

func (s *SQLStore) CreateJoinRequest(ctx context.Context, teamID string, request domain.JoinRequestSubmission, serverTime string) error {
	scopesJSON, err := json.Marshal(request.RequestedScopes)
	if err != nil {
		return err
	}
	role := request.RequestedRole
	if role == "" {
		role = "developers"
	}
	_, err = s.db.ExecContext(ctx, `
		insert into members (team_id, handle, public_key_sha, signing_public_key, encryption_public_key,
			role, management, status, requested_role, requested_management, requested_scopes, created_at)
		values ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $9, $10, $11)
	`, teamID, request.Joiner.Handle, request.Joiner.PublicKeySHA, request.Joiner.SigningPublicKey, request.Joiner.EncryptionPublicKey,
		role, false, role, request.RequestedManagement, scopesJSON, serverTime)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrJoinRequestDuplicate
		}
		return err
	}
	return nil
}

func (s *SQLStore) ListPendingJoinRequests(ctx context.Context, teamID string, actor domain.Member) (domain.PendingJoinRequestsData, error) {
	if !domain.MemberCanManage(actor) {
		return domain.PendingJoinRequestsData{}, ErrPermissionDenied
	}
	requests, err := listPendingJoinRequestsSQL(ctx, s.db, teamID)
	if err != nil {
		return domain.PendingJoinRequestsData{}, err
	}
	if requests == nil {
		requests = []domain.JoinRequestRow{}
	}
	return domain.PendingJoinRequestsData{Requests: requests}, nil
}

func (s *SQLStore) ApproveJoinRequest(ctx context.Context, teamID string, publicKeySHA string, actor domain.Member, request domain.ApproveJoinRequestBody) (domain.ApproveJoinResult, error) {
	if !domain.MemberCanManage(actor) {
		return domain.ApproveJoinResult{}, ErrPermissionDenied
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ApproveJoinResult{}, err
	}
	defer tx.Rollback()

	var handle string
	err = tx.QueryRowContext(ctx, `
		select handle from members
		where team_id = $1 and public_key_sha = $2 and status = 'pending'
		for update
	`, teamID, publicKeySHA).Scan(&handle)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.ApproveJoinResult{}, ErrJoinRequestNotFound
		}
		return domain.ApproveJoinResult{}, err
	}

	role := request.GrantedRole
	if role == "" {
		role = "developers"
	}

	_, err = tx.ExecContext(ctx, `
		update members
		set status = 'active', role = $3, management = $4, approved_by_key_sha = $5, approved_at = now()
		where team_id = $1 and public_key_sha = $2 and status = 'pending'
	`, teamID, publicKeySHA, role, request.GrantedManagement, actor.PublicKeySHA)
	if err != nil {
		return domain.ApproveJoinResult{}, err
	}

	for scope, permission := range request.GrantedScopes {
		var scopeID string
		err = tx.QueryRowContext(ctx, `select id from scopes where team_id = $1 and name = $2`, teamID, scope).Scan(&scopeID)
		if err != nil {
			return domain.ApproveJoinResult{}, fmt.Errorf("scope %s: %w", scope, err)
		}
		_, err = tx.ExecContext(ctx, `
			insert into scope_access_rules (team_id, scope_id, subject_type, subject_value, permission, granted_at_revision, active)
			values ($1, $2, 'member', $3, $4, (select revision from teams where id = $1), true)
			on conflict (team_id, scope_id, subject_type, subject_value) do update set permission = excluded.permission, active = true
		`, teamID, scopeID, publicKeySHA, permission)
		if err != nil {
			return domain.ApproveJoinResult{}, err
		}
	}

	for _, envelope := range request.ScopeKeyEnvelopes {
		var scopeID string
		err = tx.QueryRowContext(ctx, `select id from scopes where team_id = $1 and name = $2`, teamID, envelope.Scope).Scan(&scopeID)
		if err != nil {
			return domain.ApproveJoinResult{}, fmt.Errorf("scope %s envelope: %w", envelope.Scope, err)
		}
		_, err = tx.ExecContext(ctx, `
			insert into scope_key_envelopes (team_id, scope_id, recipient_key_sha, scope_key_version, encrypted_scope_key, algorithm)
			values ($1, $2, $3, $4, $5, $6)
			on conflict (team_id, scope_id, recipient_key_sha) do update
				set scope_key_version = excluded.scope_key_version,
					encrypted_scope_key = excluded.encrypted_scope_key,
					algorithm = excluded.algorithm
		`, teamID, scopeID, envelope.RecipientKeySHA, envelope.ScopeKeyVersion, envelope.EncryptedScopeKey, envelope.Algorithm)
		if err != nil {
			return domain.ApproveJoinResult{}, err
		}
	}

	_, err = tx.ExecContext(ctx, `update teams set revision = revision + 1 where id = $1`, teamID)
	if err != nil {
		return domain.ApproveJoinResult{}, err
	}

	var newRevision int
	err = tx.QueryRowContext(ctx, `select revision from teams where id = $1`, teamID).Scan(&newRevision)
	if err != nil {
		return domain.ApproveJoinResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.ApproveJoinResult{}, err
	}

	return domain.ApproveJoinResult{
		MemberPublicKeySHA: publicKeySHA,
		ConfigRevision:     domain.RevisionString(newRevision),
	}, nil
}

func (s *SQLStore) DeclineJoinRequest(ctx context.Context, teamID string, publicKeySHA string, actor domain.Member, request domain.DeclineJoinRequestBody) error {
	if !domain.MemberCanManage(actor) {
		return ErrPermissionDenied
	}
	result, err := s.db.ExecContext(ctx, `
		update members
		set status = 'declined', declined_by_key_sha = $3, declined_at = now()
		where team_id = $1 and public_key_sha = $2 and status = 'pending'
	`, teamID, publicKeySHA, actor.PublicKeySHA)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrJoinRequestNotFound
	}
	return nil
}

func listPendingJoinRequestsSQL(ctx context.Context, db *sql.DB, teamID string) ([]domain.JoinRequestRow, error) {
	rows, err := db.QueryContext(ctx, `
		select handle, public_key_sha, signing_public_key, encryption_public_key,
			requested_role, requested_management, requested_scopes, created_at
		from members
		where team_id = $1 and status = 'pending'
		order by created_at asc
	`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []domain.JoinRequestRow
	for rows.Next() {
		var row domain.JoinRequestRow
		var scopesJSON []byte
		var createdAt sql.NullTime
		if err := rows.Scan(&row.Handle, &row.PublicKeySHA, &row.SigningPublicKey, &row.EncryptionPublicKey,
			&row.RequestedRole, &row.RequestedManagement, &scopesJSON, &createdAt); err != nil {
			return nil, err
		}
		if scopesJSON != nil {
			_ = json.Unmarshal(scopesJSON, &row.RequestedScopes)
		}
		if createdAt.Valid {
			row.CreatedAt = createdAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		requests = append(requests, row)
	}
	return requests, rows.Err()
}

func isUniqueViolation(err error) bool {
	return err != nil && (contains(err.Error(), "unique constraint") || contains(err.Error(), "duplicate key") || contains(err.Error(), "UNIQUE constraint"))
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
