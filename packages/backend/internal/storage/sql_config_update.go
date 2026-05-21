package storage

import (
	"context"
	"database/sql"
	"encoding/json"

	"propagate/backend/internal/domain"
)

type snapshotMember struct {
	Handle              string            `json:"handle"`
	PublicKeySHA        string            `json:"public_key_sha"`
	SigningPublicKey    string            `json:"signing_public_key"`
	EncryptionPublicKey string            `json:"encryption_public_key"`
	Management          bool              `json:"management,omitempty"`
	Scopes              map[string]string `json:"scopes,omitempty"`
}

func appendMemberToConfigSnapshot(ctx context.Context, tx *sql.Tx, teamID string, operationID string, actorKeySHA string, newMember snapshotMember) (int, string, error) {
	var currentRevision int
	var snapshot []byte
	err := tx.QueryRowContext(ctx, `
		select t.current_config_revision, r.config_snapshot
		from teams t
		join team_config_revisions r on r.team_id = t.id and r.revision_number = t.current_config_revision
		where t.id = $1
	`, teamID).Scan(&currentRevision, &snapshot)
	if err != nil {
		return 0, "", err
	}

	var parsed struct {
		Version json.RawMessage   `json:"version"`
		Team    json.RawMessage   `json:"team"`
		Scopes  json.RawMessage   `json:"scopes"`
		Members []snapshotMember  `json:"members"`
		Rest    json.RawMessage   `json:"-"`
	}
	if err := json.Unmarshal(snapshot, &parsed); err != nil {
		return 0, "", err
	}

	parsed.Members = append(parsed.Members, newMember)

	// Rebuild the full snapshot preserving all fields
	var full map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &full); err != nil {
		return 0, "", err
	}
	membersJSON, err := json.Marshal(parsed.Members)
	if err != nil {
		return 0, "", err
	}
	full["members"] = membersJSON

	updatedSnapshot, err := json.Marshal(full)
	if err != nil {
		return 0, "", err
	}

	configHash, err := domain.ConfigHash(updatedSnapshot)
	if err != nil {
		return 0, "", err
	}

	newRevision := currentRevision + 1
	if _, err := tx.ExecContext(ctx, `
		insert into team_config_revisions (
			team_id, revision_number, config_hash, config_snapshot, pushed_by_key_sha, operation_id
		)
		values ($1, $2, $3, $4::jsonb, $5, $6)
	`, teamID, newRevision, configHash, updatedSnapshot, actorKeySHA, operationID); err != nil {
		return 0, "", err
	}

	if _, err := tx.ExecContext(ctx, `
		update teams set current_config_revision = $2, updated_at = now() where id = $1
	`, teamID, newRevision); err != nil {
		return 0, "", err
	}

	return newRevision, configHash, nil
}
