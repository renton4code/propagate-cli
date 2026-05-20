package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"propagate/backend/internal/domain"
)

type SQLStore struct {
	db *sql.DB
}

func NewSQLStore(db *sql.DB) *SQLStore {
	return &SQLStore{db: db}
}

func (s *SQLStore) ReserveNonce(ctx context.Context, publicKeySHA string, nonce string, expiresAt time.Time) error {
	if _, err := s.db.ExecContext(ctx, `delete from request_nonces where expires_at <= now()`); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		insert into request_nonces (public_key_sha, nonce, expires_at)
		values ($1, $2, $3)
		on conflict do nothing
	`, publicKeySHA, nonce, expiresAt)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrReplayRejected
	}
	return nil
}

func (s *SQLStore) CreateTeamSetup(ctx context.Context, request domain.TeamSetupRequest, configHash string) (domain.SetupResult, error) {
	fingerprint, err := domain.Fingerprint(request)
	if err != nil {
		return domain.SetupResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SetupResult{}, err
	}
	defer tx.Rollback()

	result, found, err := existingSetupResult(ctx, tx, request.OperationID, fingerprint)
	if err != nil {
		return domain.SetupResult{}, err
	}
	if found {
		return result, nil
	}

	teamID, err := setupTeamID(request.ConfigSnapshot)
	if err != nil {
		return domain.SetupResult{}, err
	}
	configSnapshot, configHash, err := domain.CanonicalSetupConfigSnapshot(request, teamID)
	if err != nil {
		return domain.SetupResult{}, err
	}

	if _, err := tx.ExecContext(ctx, `
		insert into teams (id, name, current_config_revision, created_by_key_sha)
		values ($1, $2, 1, $3)
	`, teamID, request.TeamName, request.FirstAdmin.PublicKeySHA); err != nil {
		return domain.SetupResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into setup_operations (operation_id, request_fingerprint, team_id)
		values ($1, $2, $3)
	`, request.OperationID, fingerprint, teamID); err != nil {
		return domain.SetupResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into members (
			team_id, handle, public_key_sha, signing_public_key, encryption_public_key,
			management, status, approved_by_key_sha, approved_at
		)
		values ($1, $2, $3, $4, $5, true, 'active', $3, now())
	`, teamID, request.FirstAdmin.Handle, request.FirstAdmin.PublicKeySHA, request.FirstAdmin.SigningPublicKey, request.FirstAdmin.EncryptionPublicKey); err != nil {
		return domain.SetupResult{}, err
	}

	scopeIDs := map[string]int64{}
	for _, scope := range request.Scopes {
		var scopeID int64
		if err := tx.QueryRowContext(ctx, `
			insert into scopes (team_id, name, kind)
			values ($1, $2, $3)
			returning id
		`, teamID, scope.Name, scopeKind(scope.Name)).Scan(&scopeID); err != nil {
			return domain.SetupResult{}, err
		}
		scopeIDs[scope.Name] = scopeID
		for _, path := range scope.EnvFiles {
			if _, err := tx.ExecContext(ctx, `
				insert into env_file_mappings (team_id, scope_id, path, config_revision, active)
				values ($1, $2, $3, 1, true)
			`, teamID, scopeID, path); err != nil {
				return domain.SetupResult{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			insert into scope_access_rules (
				team_id, scope_id, subject_type, subject_value, permission, config_revision, active
			)
			values ($1, $2, 'member', $3, 'write', 1, true)
		`, teamID, scopeID, request.FirstAdmin.PublicKeySHA); err != nil {
			return domain.SetupResult{}, err
		}
	}

	for _, envelope := range request.ScopeKeyEnvelopes {
		if _, err := tx.ExecContext(ctx, `
			insert into scope_key_envelopes (
				team_id, scope_id, recipient_key_sha, scope_key_version, encrypted_scope_key,
				algorithm, created_by_key_sha, config_revision
			)
			values ($1, $2, $3, $4, $5, $6, $7, 1)
		`, teamID, scopeIDs[envelope.Scope], envelope.RecipientKeySHA, envelope.ScopeKeyVersion, envelope.EncryptedScopeKey, envelope.Algorithm, request.FirstAdmin.PublicKeySHA); err != nil {
			return domain.SetupResult{}, err
		}
	}

	for _, version := range request.EncryptedSecretVersions {
		var variableID int64
		if err := tx.QueryRowContext(ctx, `
			insert into secret_variables (team_id, scope_id, env_file_path, name)
			values ($1, $2, $3, $4)
			returning id
		`, teamID, scopeIDs[version.Scope], version.EnvFilePath, version.Name).Scan(&variableID); err != nil {
			return domain.SetupResult{}, err
		}

		var versionID int64
		if err := tx.QueryRowContext(ctx, `
			insert into secret_versions (
				variable_id, version_number, ciphertext, nonce, algorithm,
				scope_key_version, created_by_key_sha, operation_id
			)
			values ($1, 1, $2, $3, $4, $5, $6, $7)
			returning id
		`, variableID, version.Ciphertext, version.Nonce, version.Algorithm, version.ScopeKeyVersion, request.FirstAdmin.PublicKeySHA, request.OperationID).Scan(&versionID); err != nil {
			return domain.SetupResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			update secret_variables
			set current_version_id = $1, updated_at = now()
			where id = $2
		`, versionID, variableID); err != nil {
			return domain.SetupResult{}, err
		}
	}

	if _, err := tx.ExecContext(ctx, `
		insert into team_config_revisions (
			team_id, revision_number, config_hash, config_snapshot, pushed_by_key_sha, operation_id
		)
		values ($1, 1, $2, $3::jsonb, $4, $5)
	`, teamID, configHash, []byte(configSnapshot), request.FirstAdmin.PublicKeySHA, request.OperationID); err != nil {
		return domain.SetupResult{}, err
	}

	metadata, err := json.Marshal(map[string]any{
		"operation_id":              request.OperationID,
		"encrypted_variables_count": len(request.EncryptedSecretVersions),
		"envelopes_count":           len(request.ScopeKeyEnvelopes),
		"scopes_count":              len(request.Scopes),
		"cli_version":               request.Client.CLIVersion,
		"client_kind":               request.Client.ClientKind,
		"agent_adapter":             request.Client.AgentAdapter,
	})
	if err != nil {
		return domain.SetupResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into audit_events (
			team_id, actor_key_sha, actor_handle, event_type, config_revision, metadata
		)
		values ($1, $2, $3, 'team_created', 1, $4::jsonb)
	`, teamID, request.FirstAdmin.PublicKeySHA, request.FirstAdmin.Handle, metadata); err != nil {
		return domain.SetupResult{}, err
	}

	result = domain.SetupResult{
		TeamID:                  teamID,
		ConfigRevision:          "rev_00001",
		ConfigHash:              configHash,
		ScopesCreated:           scopeNames(request.Scopes),
		EncryptedVariablesCount: len(request.EncryptedSecretVersions),
		EnvelopesCount:          len(request.ScopeKeyEnvelopes),
	}
	if err := tx.Commit(); err != nil {
		return domain.SetupResult{}, err
	}
	return result, nil
}

func existingSetupResult(ctx context.Context, tx *sql.Tx, operationID string, fingerprint string) (domain.SetupResult, bool, error) {
	var teamID string
	var existingFingerprint string
	err := tx.QueryRowContext(ctx, `
		select team_id, request_fingerprint
		from setup_operations
		where operation_id = $1
	`, operationID).Scan(&teamID, &existingFingerprint)
	if err == sql.ErrNoRows {
		return domain.SetupResult{}, false, nil
	}
	if err != nil {
		return domain.SetupResult{}, false, err
	}
	if existingFingerprint != fingerprint {
		return domain.SetupResult{}, false, ErrIdempotencyConflict
	}
	result, err := setupResultForTeam(ctx, tx, teamID)
	if err != nil {
		return domain.SetupResult{}, false, err
	}
	return result, true, nil
}

func setupResultForTeam(ctx context.Context, tx *sql.Tx, teamID string) (domain.SetupResult, error) {
	var revision int
	var configHash string
	if err := tx.QueryRowContext(ctx, `
		select revision_number, config_hash
		from team_config_revisions
		where team_id = $1
		order by revision_number desc
		limit 1
	`, teamID).Scan(&revision, &configHash); err != nil {
		return domain.SetupResult{}, err
	}

	rows, err := tx.QueryContext(ctx, `
		select name
		from scopes
		where team_id = $1
		order by id
	`, teamID)
	if err != nil {
		return domain.SetupResult{}, err
	}
	defer rows.Close()
	var scopes []string
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			return domain.SetupResult{}, err
		}
		scopes = append(scopes, scope)
	}
	if err := rows.Err(); err != nil {
		return domain.SetupResult{}, err
	}

	var encryptedCount int
	if err := tx.QueryRowContext(ctx, `
		select count(*)
		from secret_versions sv
		join secret_variables v on v.id = sv.variable_id
		where v.team_id = $1
	`, teamID).Scan(&encryptedCount); err != nil {
		return domain.SetupResult{}, err
	}
	var envelopeCount int
	if err := tx.QueryRowContext(ctx, `
		select count(*)
		from scope_key_envelopes
		where team_id = $1
	`, teamID).Scan(&envelopeCount); err != nil {
		return domain.SetupResult{}, err
	}

	return domain.SetupResult{
		TeamID:                  teamID,
		ConfigRevision:          fmt.Sprintf("rev_%05d", revision),
		ConfigHash:              configHash,
		ScopesCreated:           scopes,
		EncryptedVariablesCount: encryptedCount,
		EnvelopesCount:          envelopeCount,
	}, nil
}

func scopeKind(name string) string {
	switch name {
	case "dev", "staging", "prod", "other":
		return "builtin"
	default:
		return "custom"
	}
}

func (s *SQLStore) GetMember(ctx context.Context, teamID string, publicKeySHA string) (domain.Member, error) {
	var member domain.Member
	err := s.db.QueryRowContext(ctx, `
		select handle, public_key_sha, signing_public_key, encryption_public_key, management, status
		from members
		where team_id = $1 and public_key_sha = $2 and status = 'active'
	`, teamID, publicKeySHA).Scan(
		&member.Handle,
		&member.PublicKeySHA,
		&member.SigningPublicKey,
		&member.EncryptionPublicKey,
		&member.Management,
		&member.Status,
	)
	if err == sql.ErrNoRows {
		return domain.Member{}, ErrPermissionDenied
	}
	if err != nil {
		return domain.Member{}, err
	}
	return member, nil
}

func (s *SQLStore) ConfigStatus(ctx context.Context, teamID string, actor domain.Member, localRevision string, localConfigHash string) (domain.ConfigStatusData, error) {
	var revision int
	var hash string
	var membersCount int
	var scopesCount int
	if err := s.db.QueryRowContext(ctx, `
		select t.current_config_revision, r.config_hash,
			(select count(*) from members m where m.team_id = t.id and m.status = 'active'),
			(select count(*) from scopes sc where sc.team_id = t.id and sc.archived_at is null)
		from teams t
		join team_config_revisions r on r.team_id = t.id and r.revision_number = t.current_config_revision
		where t.id = $1
	`, teamID).Scan(&revision, &hash, &membersCount, &scopesCount); err != nil {
		if err == sql.ErrNoRows {
			return domain.ConfigStatusData{}, ErrNotFound
		}
		return domain.ConfigStatusData{}, err
	}
	cloudRevision := domain.RevisionString(revision)
	state := "unknown"
	action := "pull"
	if localRevision == cloudRevision && localConfigHash == hash {
		state = "equal"
		action = "none"
	} else if localRevision == cloudRevision {
		state = "local_ahead"
		action = "push"
	} else if localNum, err := domain.RevisionNumber(localRevision); err == nil && localNum < revision {
		state = "cloud_ahead"
		action = "pull"
	} else if localRevision != "" || localConfigHash != "" {
		state = "conflict"
		action = "resolve_conflict"
	}
	return domain.ConfigStatusData{
		LocalRevision:     localRevision,
		CloudRevision:     cloudRevision,
		LocalConfigHash:   localConfigHash,
		CloudConfigHash:   hash,
		State:             state,
		RecommendedAction: action,
		SafeSummary: map[string]any{
			"members_count": membersCount,
			"scopes_count":  scopesCount,
			"actor":         actor.PublicKeySHA,
		},
	}, nil
}

func (s *SQLStore) GetConfig(ctx context.Context, teamID string, actor domain.Member, serverTime string) (domain.ConfigData, error) {
	var revision int
	var hash string
	var snapshot []byte
	if err := s.db.QueryRowContext(ctx, `
		select r.revision_number, r.config_hash, r.config_snapshot
		from teams t
		join team_config_revisions r on r.team_id = t.id and r.revision_number = t.current_config_revision
		where t.id = $1
	`, teamID).Scan(&revision, &hash, &snapshot); err != nil {
		if err == sql.ErrNoRows {
			return domain.ConfigData{}, ErrNotFound
		}
		return domain.ConfigData{}, err
	}
	return domain.ConfigData{
		ConfigRevision: domain.RevisionString(revision),
		ConfigHash:     hash,
		ConfigSnapshot: append(json.RawMessage(nil), snapshot...),
		ServerTime:     serverTime,
	}, nil
}

func (s *SQLStore) PushConfig(ctx context.Context, teamID string, actor domain.Member, request domain.ConfigPushRequest) (domain.ConfigPushResult, error) {
	if !domain.MemberCanManage(actor) {
		return domain.ConfigPushResult{}, ErrPermissionDenied
	}
	fingerprint, err := domain.FingerprintConfigPush(request)
	if err != nil {
		return domain.ConfigPushResult{}, err
	}
	hash, err := domain.ConfigHash(request.TargetConfigSnapshot)
	if err != nil {
		return domain.ConfigPushResult{}, err
	}
	expected, err := domain.RevisionNumber(request.ExpectedConfigRevision)
	if err != nil {
		return domain.ConfigPushResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ConfigPushResult{}, err
	}
	defer tx.Rollback()

	var existingRevision int
	var existingHash string
	var existingFingerprint sql.NullString
	err = tx.QueryRowContext(ctx, `
		select revision_number, config_hash, request_fingerprint
		from team_config_revisions
		where team_id = $1 and operation_id = $2
	`, teamID, request.OperationID).Scan(&existingRevision, &existingHash, &existingFingerprint)
	if err == nil {
		if existingFingerprint.Valid && existingFingerprint.String != fingerprint {
			return domain.ConfigPushResult{}, ErrIdempotencyConflict
		}
		return domain.ConfigPushResult{
			OldRevision:      request.ExpectedConfigRevision,
			NewRevision:      domain.RevisionString(existingRevision),
			ConfigHash:       existingHash,
			AppliedDecisions: request.Decisions,
			EnvelopesCount:   len(request.ScopeKeyEnvelopes),
			AuditEventsCount: 0,
		}, nil
	}
	if err != sql.ErrNoRows {
		return domain.ConfigPushResult{}, err
	}

	var current int
	if err := tx.QueryRowContext(ctx, `select current_config_revision from teams where id = $1 for update`, teamID).Scan(&current); err != nil {
		if err == sql.ErrNoRows {
			return domain.ConfigPushResult{}, ErrNotFound
		}
		return domain.ConfigPushResult{}, err
	}
	if current != expected {
		return domain.ConfigPushResult{}, ErrRevisionConflict
	}
	newRevision := current + 1
	if err := applyConfigSnapshotSQL(ctx, tx, teamID, newRevision, request.TargetConfigSnapshot); err != nil {
		return domain.ConfigPushResult{}, err
	}
	if err := applyApprovedAccessDecisionsSQL(ctx, tx, teamID, newRevision, request.Decisions.Approved); err != nil {
		return domain.ConfigPushResult{}, err
	}
	if err := applyDeclinedDecisionsSQL(ctx, tx, teamID, actor, request.Decisions.Declined); err != nil {
		return domain.ConfigPushResult{}, err
	}
	for _, envelope := range request.ScopeKeyEnvelopes {
		scopeID, err := scopeID(ctx, tx, teamID, envelope.Scope)
		if err != nil {
			return domain.ConfigPushResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			insert into scope_key_envelopes (
				team_id, scope_id, recipient_key_sha, scope_key_version, encrypted_scope_key,
				algorithm, created_by_key_sha, config_revision
			)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
		`, teamID, scopeID, envelope.RecipientKeySHA, envelope.ScopeKeyVersion, envelope.EncryptedScopeKey, envelope.Algorithm, actor.PublicKeySHA, newRevision); err != nil {
			return domain.ConfigPushResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		insert into team_config_revisions (
			team_id, revision_number, config_hash, config_snapshot, pushed_by_key_sha, operation_id, request_fingerprint
		)
		values ($1, $2, $3, $4::jsonb, $5, $6, $7)
	`, teamID, newRevision, hash, []byte(request.TargetConfigSnapshot), actor.PublicKeySHA, request.OperationID, fingerprint); err != nil {
		return domain.ConfigPushResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		update teams set current_config_revision = $2, updated_at = now() where id = $1
	`, teamID, newRevision); err != nil {
		return domain.ConfigPushResult{}, err
	}
	if _, err := insertAudit(ctx, tx, teamID, actor, "config_pushed", sql.NullInt64{}, map[string]any{"operation_id": request.OperationID}, newRevision); err != nil {
		return domain.ConfigPushResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ConfigPushResult{}, err
	}
	return domain.ConfigPushResult{
		OldRevision:      domain.RevisionString(current),
		NewRevision:      domain.RevisionString(newRevision),
		ConfigHash:       hash,
		AppliedDecisions: request.Decisions,
		EnvelopesCount:   len(request.ScopeKeyEnvelopes),
		AuditEventsCount: 1,
	}, nil
}

func (s *SQLStore) GetKeyEnvelope(ctx context.Context, teamID string, scopeName string, actor domain.Member) (domain.ScopeEnvelopeData, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ScopeEnvelopeData{}, err
	}
	defer tx.Rollback()
	teamRevision, scopeID, err := requireScopePermission(ctx, tx, teamID, scopeName, actor, "read")
	if err != nil {
		return domain.ScopeEnvelopeData{}, err
	}
	var envelope domain.ScopeKeyEnvelope
	if err := tx.QueryRowContext(ctx, `
		select $2, recipient_key_sha, scope_key_version, encrypted_scope_key, algorithm
		from scope_key_envelopes
		where team_id = $1 and scope_id = $3 and recipient_key_sha = $4 and revoked_at is null
		order by scope_key_version desc, id desc
		limit 1
	`, teamID, scopeName, scopeID, actor.PublicKeySHA).Scan(&envelope.Scope, &envelope.RecipientKeySHA, &envelope.ScopeKeyVersion, &envelope.EncryptedScopeKey, &envelope.Algorithm); err != nil {
		if err == sql.ErrNoRows {
			return domain.ScopeEnvelopeData{}, ErrPermissionDenied
		}
		return domain.ScopeEnvelopeData{}, err
	}
	return domain.ScopeEnvelopeData{
		Scope:            domain.ScopeRef{ID: strconv.FormatInt(scopeID, 10), Name: scopeName},
		ConfigRevision:   domain.RevisionString(teamRevision),
		ScopeKeyVersion:  envelope.ScopeKeyVersion,
		ScopeKeyEnvelope: envelope,
		Algorithm:        envelope.Algorithm,
	}, nil
}

func (s *SQLStore) PullBundle(ctx context.Context, teamID string, scopeName string, actor domain.Member) (domain.PullBundleData, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PullBundleData{}, err
	}
	defer tx.Rollback()
	teamRevision, sid, err := requireScopePermission(ctx, tx, teamID, scopeName, actor, "read")
	if err != nil {
		return domain.PullBundleData{}, err
	}
	envelopeData, err := keyEnvelopeSQL(ctx, tx, teamID, scopeName, sid, actor.PublicKeySHA)
	if err != nil {
		return domain.PullBundleData{}, err
	}
	files, err := envFileMappingsSQL(ctx, tx, teamID, sid)
	if err != nil {
		return domain.PullBundleData{}, err
	}
	variables, versions, err := secretVersionsSQL(ctx, tx, teamID, sid)
	if err != nil {
		return domain.PullBundleData{}, err
	}
	return domain.PullBundleData{
		Scope:            domain.ScopeRef{ID: strconv.FormatInt(sid, 10), Name: scopeName},
		ConfigRevision:   domain.RevisionString(teamRevision),
		EnvFileMappings:  files,
		ScopeKeyEnvelope: envelopeData,
		Variables:        variables,
		SecretVersions:   versions,
	}, nil
}

func (s *SQLStore) EnvPush(ctx context.Context, teamID string, scopeName string, actor domain.Member, request domain.EnvPushRequest) (domain.EnvPushResult, error) {
	fingerprint, err := domain.FingerprintEnvPush(request)
	if err != nil {
		return domain.EnvPushResult{}, err
	}
	expected, err := domain.RevisionNumber(request.ExpectedConfigRevision)
	if err != nil {
		return domain.EnvPushResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.EnvPushResult{}, err
	}
	defer tx.Rollback()

	var opFingerprint string
	var opResult []byte
	err = tx.QueryRowContext(ctx, `
		select request_fingerprint, result
		from env_push_operations
		where team_id = $1 and operation_id = $2
	`, teamID, request.OperationID).Scan(&opFingerprint, &opResult)
	if err == nil {
		if opFingerprint != fingerprint {
			return domain.EnvPushResult{}, ErrIdempotencyConflict
		}
		var result domain.EnvPushResult
		if err := json.Unmarshal(opResult, &result); err != nil {
			return domain.EnvPushResult{}, err
		}
		return result, nil
	}
	if err != sql.ErrNoRows {
		return domain.EnvPushResult{}, err
	}

	teamRevision, sid, err := requireScopePermission(ctx, tx, teamID, scopeName, actor, "write")
	if err != nil {
		return domain.EnvPushResult{}, err
	}
	if teamRevision != expected {
		return domain.EnvPushResult{}, ErrRevisionConflict
	}

	conflicts, err := envPushConflictsSQL(ctx, tx, teamID, sid, request)
	if err != nil {
		return domain.EnvPushResult{}, err
	}
	if len(conflicts) > 0 {
		return domain.EnvPushResult{Conflicts: conflicts}, ErrSecretConflict
	}

	var created []domain.CreatedVersion
	for _, upsert := range request.Upserts {
		versionID, err := upsertSQL(ctx, tx, teamID, sid, actor.PublicKeySHA, request.OperationID, upsert)
		if err != nil {
			return domain.EnvPushResult{}, err
		}
		created = append(created, domain.CreatedVersion{EnvFilePath: upsert.EnvFilePath, Name: upsert.Name, VersionID: versionID})
	}
	var removed []domain.RemovedVariable
	for _, removal := range request.Removals {
		if _, err := tx.ExecContext(ctx, `
			update secret_variables
			set deleted_at = now(), updated_at = now()
			where team_id = $1 and scope_id = $2 and env_file_path = $3 and name = $4
		`, teamID, sid, removal.EnvFilePath, removal.Name); err != nil {
			return domain.EnvPushResult{}, err
		}
		removed = append(removed, domain.RemovedVariable{EnvFilePath: removal.EnvFilePath, Name: removal.Name})
	}
	newRevision := teamRevision
	configHash := ""
	if len(request.TargetConfigSnapshot) > 0 {
		var err error
		configHash, err = domain.ConfigHash(request.TargetConfigSnapshot)
		if err != nil {
			return domain.EnvPushResult{}, err
		}
		newRevision = teamRevision + 1
		if _, err := tx.ExecContext(ctx, `
			insert into team_config_revisions (
				team_id, revision_number, config_hash, config_snapshot, pushed_by_key_sha, operation_id, request_fingerprint
			)
			values ($1, $2, $3, $4::jsonb, $5, $6, $7)
		`, teamID, newRevision, configHash, []byte(request.TargetConfigSnapshot), actor.PublicKeySHA, request.OperationID, fingerprint); err != nil {
			return domain.EnvPushResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			update teams
			set current_config_revision = $2, updated_at = now()
			where id = $1
		`, teamID, newRevision); err != nil {
			return domain.EnvPushResult{}, err
		}
	}
	if _, err := insertAudit(ctx, tx, teamID, actor, "env_pushed", sql.NullInt64{Int64: sid, Valid: true}, map[string]any{
		"operation_id": request.OperationID,
		"upserts":      len(request.Upserts),
		"removals":     len(request.Removals),
	}, newRevision); err != nil {
		return domain.EnvPushResult{}, err
	}
	result := domain.EnvPushResult{CreatedVersions: created, RemovedVariables: removed, AuditEventsCount: 1}
	if len(request.TargetConfigSnapshot) > 0 {
		result.ConfigRevision = domain.RevisionString(newRevision)
		result.ConfigHash = configHash
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return domain.EnvPushResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into env_push_operations (team_id, operation_id, request_fingerprint, result)
		values ($1, $2, $3, $4::jsonb)
	`, teamID, request.OperationID, fingerprint, resultJSON); err != nil {
		return domain.EnvPushResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.EnvPushResult{}, err
	}
	return result, nil
}

func (s *SQLStore) EnvStatus(ctx context.Context, teamID string, scopeName string, actor domain.Member) (domain.EnvStatusData, error) {
	bundle, err := s.PullBundle(ctx, teamID, scopeName, actor)
	if err != nil {
		return domain.EnvStatusData{}, err
	}
	return domain.EnvStatusData{
		Scope:            bundle.Scope,
		ConfigRevision:   bundle.ConfigRevision,
		Variables:        bundle.Variables,
		EncryptedValues:  bundle.SecretVersions,
		ScopeKeyEnvelope: &bundle.ScopeKeyEnvelope,
		CanRead:          true,
	}, nil
}

func (s *SQLStore) RecordPullEvent(ctx context.Context, teamID string, actor domain.Member, request domain.PullEventRequest) (domain.PullEventResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PullEventResult{}, err
	}
	defer tx.Rollback()
	revision, sid, err := requireScopePermission(ctx, tx, teamID, request.Scope, actor, "read")
	if err != nil {
		return domain.PullEventResult{}, err
	}
	eventID, err := insertAudit(ctx, tx, teamID, actor, "env_pulled", sql.NullInt64{Int64: sid, Valid: true}, map[string]any{
		"variables_count": request.VariablesCount,
		"env_file_paths":  request.EnvFilePaths,
		"cli_version":     request.Client.CLIVersion,
		"client_kind":     request.Client.ClientKind,
	}, revision)
	if err != nil {
		return domain.PullEventResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.PullEventResult{}, err
	}
	return domain.PullEventResult{EventID: eventID, RecordedCount: 1}, nil
}

func (s *SQLStore) TeamStatus(ctx context.Context, teamID string, actor domain.Member) (domain.TeamStatusData, error) {
	config, err := s.GetConfig(ctx, teamID, actor, "")
	if err != nil {
		return domain.TeamStatusData{}, err
	}
	var teamName string
	if err := s.db.QueryRowContext(ctx, `select name from teams where id = $1`, teamID).Scan(&teamName); err != nil {
		return domain.TeamStatusData{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
		select handle, public_key_sha, signing_public_key, encryption_public_key, management, status
		from members
		where team_id = $1 and status = 'active'
		order by management desc, public_key_sha
	`, teamID)
	if err != nil {
		return domain.TeamStatusData{}, err
	}
	defer rows.Close()
	members := map[string][]domain.Member{}
	seenPulls := map[string]bool{}
	var all []domain.Member
	for rows.Next() {
		var member domain.Member
		if err := rows.Scan(&member.Handle, &member.PublicKeySHA, &member.SigningPublicKey, &member.EncryptionPublicKey, &member.Management, &member.Status); err != nil {
			return domain.TeamStatusData{}, err
		}
		scopes, err := memberScopesSQL(ctx, s.db, teamID, member.PublicKeySHA)
		if err != nil {
			return domain.TeamStatusData{}, err
		}
		member.Scopes = scopes
		members[memberGroup(member)] = append(members[memberGroup(member)], member)
		all = append(all, member)
	}
	if err := rows.Err(); err != nil {
		return domain.TeamStatusData{}, err
	}
	pulls, err := lastPullsSQL(ctx, s.db, teamID)
	if err != nil {
		return domain.TeamStatusData{}, err
	}
	for _, pull := range pulls {
		seenPulls[pull.MemberPublicKeySHA] = true
	}
	var never []domain.Member
	for _, member := range all {
		if !seenPulls[member.PublicKeySHA] {
			never = append(never, member)
		}
	}
	actorScopes, err := memberScopesSQL(ctx, s.db, teamID, actor.PublicKeySHA)
	if err != nil {
		return domain.TeamStatusData{}, err
	}
	actor.Scopes = actorScopes

	var pendingRequests []domain.JoinRequestRow
	if domain.MemberCanManage(actor) {
		pendingRequests, err = listPendingJoinRequestsSQL(ctx, s.db, teamID)
		if err != nil {
			return domain.TeamStatusData{}, err
		}
	}

	return domain.TeamStatusData{
		Team: domain.TeamSummary{
			ID:             teamID,
			Name:           teamName,
			ConfigRevision: config.ConfigRevision,
			ConfigHash:     config.ConfigHash,
		},
		Actor:               actor,
		Members:             members,
		PendingJoinRequests: pendingRequests,
		LastPulls:           pulls,
		NeverPulled:         never,
	}, nil
}

func applyConfigSnapshotSQL(ctx context.Context, tx *sql.Tx, teamID string, revision int, raw json.RawMessage) error {
	var snapshot struct {
		Team struct {
			Name string `json:"name"`
		} `json:"team"`
		Members []struct {
			Handle              string            `json:"handle"`
			PublicKeySHA        string            `json:"public_key_sha"`
			SigningPublicKey    string            `json:"signing_public_key"`
			EncryptionPublicKey string            `json:"encryption_public_key"`
			Role                string            `json:"role"`
			Management          bool              `json:"management"`
			Scopes              map[string]string `json:"scopes"`
		} `json:"members"`
		Scopes map[string]struct {
			EnvFiles []string `json:"env_files"`
		} `json:"scopes"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}
	if snapshot.Team.Name != "" {
		if _, err := tx.ExecContext(ctx, `update teams set name = $2 where id = $1`, teamID, snapshot.Team.Name); err != nil {
			return err
		}
	}
	for _, member := range snapshot.Members {
		if member.PublicKeySHA == "" {
			continue
		}
		management := member.Management || member.Role == "admins"
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
				status = 'active'
		`, teamID, member.Handle, member.PublicKeySHA, member.SigningPublicKey, member.EncryptionPublicKey, management); err != nil {
			return err
		}
	}
	if len(snapshot.Scopes) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		update env_file_mappings set active = false where team_id = $1 and active = true
	`, teamID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		update scope_access_rules set active = false where team_id = $1 and active = true
	`, teamID); err != nil {
		return err
	}
	for name, scope := range snapshot.Scopes {
		var sid int64
		if err := tx.QueryRowContext(ctx, `
			insert into scopes (team_id, name, kind)
			values ($1, $2, $3)
			on conflict (team_id, name) do update set archived_at = null
			returning id
		`, teamID, name, scopeKind(name)).Scan(&sid); err != nil {
			return err
		}
		for _, path := range scope.EnvFiles {
			if _, err := tx.ExecContext(ctx, `
				insert into env_file_mappings (team_id, scope_id, path, config_revision, active)
				values ($1, $2, $3, $4, true)
			`, teamID, sid, path, revision); err != nil {
				return err
			}
		}
	}
	for _, member := range snapshot.Members {
		for scopeName, permission := range member.Scopes {
			sid, err := scopeID(ctx, tx, teamID, scopeName)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				insert into scope_access_rules (
					team_id, scope_id, subject_type, subject_value, permission, config_revision, active
				)
				values ($1, $2, 'member', $3, $4, $5, true)
			`, teamID, sid, member.PublicKeySHA, permission, revision); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyApprovedAccessDecisionsSQL(ctx context.Context, tx *sql.Tx, teamID string, revision int, decisions []domain.ConfigDecision) error {
	for _, decision := range decisions {
		if decision.Type != "scope_access_change" || decision.PublicKeySHA == "" || decision.Scope == "" || decision.Permission == "" {
			continue
		}
		sid, err := scopeID(ctx, tx, teamID, decision.Scope)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			insert into scope_access_rules (
				team_id, scope_id, subject_type, subject_value, permission, config_revision, active
			)
			values ($1, $2, 'member', $3, $4, $5, true)
		`, teamID, sid, decision.PublicKeySHA, decision.Permission, revision); err != nil {
			return err
		}
	}
	return nil
}

func applyDeclinedDecisionsSQL(ctx context.Context, tx *sql.Tx, teamID string, actor domain.Member, decisions []domain.ConfigDecision) error {
	for _, decision := range decisions {
		if decision.PublicKeySHA == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			update members
			set status = 'declined', declined_by_key_sha = $3, declined_at = now()
			where team_id = $1 and public_key_sha = $2 and status = 'pending'
		`, teamID, decision.PublicKeySHA, actor.PublicKeySHA); err != nil {
			return err
		}
	}
	return nil
}

func scopeID(ctx context.Context, tx *sql.Tx, teamID string, scopeName string) (int64, error) {
	var sid int64
	if err := tx.QueryRowContext(ctx, `
		select id from scopes where team_id = $1 and name = $2 and archived_at is null
	`, teamID, scopeName).Scan(&sid); err != nil {
		if err == sql.ErrNoRows {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return sid, nil
}

func requireScopePermission(ctx context.Context, tx *sql.Tx, teamID string, scopeName string, actor domain.Member, required string) (int, int64, error) {
	var revision int
	if err := tx.QueryRowContext(ctx, `select current_config_revision from teams where id = $1`, teamID).Scan(&revision); err != nil {
		if err == sql.ErrNoRows {
			return 0, 0, ErrNotFound
		}
		return 0, 0, err
	}
	sid, err := scopeID(ctx, tx, teamID, scopeName)
	if err != nil {
		return 0, 0, err
	}
	permission := ""
	_ = tx.QueryRowContext(ctx, `
		select permission
		from scope_access_rules
		where team_id = $1 and scope_id = $2 and subject_type = 'member' and subject_value = $3 and active = true
		order by id desc
		limit 1
	`, teamID, sid, actor.PublicKeySHA).Scan(&permission)
	if permission == "" && actor.Management {
		permission = "admin"
	}
	if !domain.PermissionAllows(permission, required) {
		return 0, 0, ErrPermissionDenied
	}
	return revision, sid, nil
}

func keyEnvelopeSQL(ctx context.Context, tx *sql.Tx, teamID string, scopeName string, sid int64, recipient string) (domain.ScopeKeyEnvelope, error) {
	var envelope domain.ScopeKeyEnvelope
	if err := tx.QueryRowContext(ctx, `
		select $2, recipient_key_sha, scope_key_version, encrypted_scope_key, algorithm
		from scope_key_envelopes
		where team_id = $1 and scope_id = $3 and recipient_key_sha = $4 and revoked_at is null
		order by scope_key_version desc, id desc
		limit 1
	`, teamID, scopeName, sid, recipient).Scan(&envelope.Scope, &envelope.RecipientKeySHA, &envelope.ScopeKeyVersion, &envelope.EncryptedScopeKey, &envelope.Algorithm); err != nil {
		if err == sql.ErrNoRows {
			return domain.ScopeKeyEnvelope{}, ErrPermissionDenied
		}
		return domain.ScopeKeyEnvelope{}, err
	}
	return envelope, nil
}

func envFileMappingsSQL(ctx context.Context, tx *sql.Tx, teamID string, sid int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		select path from env_file_mappings
		where team_id = $1 and scope_id = $2 and active = true
		order by path
	`, teamID, sid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		files = append(files, path)
	}
	return files, rows.Err()
}

func secretVersionsSQL(ctx context.Context, tx *sql.Tx, teamID string, sid int64) ([]domain.VariableMetadata, []domain.SecretVersionRecord, error) {
	rows, err := tx.QueryContext(ctx, `
		select v.env_file_path, v.name, v.current_version_id, sv.ciphertext, sv.nonce,
			sv.algorithm, sv.scope_key_version, sv.created_by_key_sha, sv.created_at
		from secret_variables v
		join secret_versions sv on sv.id = v.current_version_id
		where v.team_id = $1 and v.scope_id = $2 and v.deleted_at is null
		order by v.env_file_path, v.name
	`, teamID, sid)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var variables []domain.VariableMetadata
	var versions []domain.SecretVersionRecord
	for rows.Next() {
		var envPath, name, ciphertext, nonce, algorithm, updatedBy string
		var currentID int64
		var scopeKeyVersion int
		var updatedAt time.Time
		if err := rows.Scan(&envPath, &name, &currentID, &ciphertext, &nonce, &algorithm, &scopeKeyVersion, &updatedBy, &updatedAt); err != nil {
			return nil, nil, err
		}
		versionID := versionIDString(currentID)
		variables = append(variables, domain.VariableMetadata{
			Name:             name,
			EnvFilePath:      envPath,
			CurrentVersionID: versionID,
			LastUpdatedBy:    updatedBy,
			LastUpdatedAt:    updatedAt.UTC().Format(time.RFC3339),
		})
		versions = append(versions, domain.SecretVersionRecord{
			Name:             name,
			EnvFilePath:      envPath,
			CurrentVersionID: versionID,
			Ciphertext:       ciphertext,
			Nonce:            nonce,
			Algorithm:        algorithm,
			ScopeKeyVersion:  scopeKeyVersion,
		})
	}
	return variables, versions, rows.Err()
}

func envPushConflictsSQL(ctx context.Context, tx *sql.Tx, teamID string, sid int64, request domain.EnvPushRequest) ([]domain.SecretConflict, error) {
	var conflicts []domain.SecretConflict
	for _, upsert := range request.Upserts {
		current, err := currentVersionIDSQL(ctx, tx, teamID, sid, upsert.EnvFilePath, upsert.Name)
		if err != nil {
			return nil, err
		}
		if upsert.ExpectedVersionID != "" && upsert.ExpectedVersionID != current {
			conflicts = append(conflicts, domain.SecretConflict{
				EnvFilePath:       upsert.EnvFilePath,
				Name:              upsert.Name,
				ExpectedVersionID: upsert.ExpectedVersionID,
				CurrentVersionID:  current,
			})
		}
	}
	for _, removal := range request.Removals {
		current, err := currentVersionIDSQL(ctx, tx, teamID, sid, removal.EnvFilePath, removal.Name)
		if err != nil {
			return nil, err
		}
		if removal.ExpectedVersionID != "" && removal.ExpectedVersionID != current {
			conflicts = append(conflicts, domain.SecretConflict{
				EnvFilePath:       removal.EnvFilePath,
				Name:              removal.Name,
				ExpectedVersionID: removal.ExpectedVersionID,
				CurrentVersionID:  current,
			})
		}
	}
	return conflicts, nil
}

func currentVersionIDSQL(ctx context.Context, tx *sql.Tx, teamID string, sid int64, envPath string, name string) (string, error) {
	var current sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		select current_version_id
		from secret_variables
		where team_id = $1 and scope_id = $2 and env_file_path = $3 and name = $4 and deleted_at is null
	`, teamID, sid, envPath, name).Scan(&current)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !current.Valid {
		return "", nil
	}
	return versionIDString(current.Int64), nil
}

func upsertSQL(ctx context.Context, tx *sql.Tx, teamID string, sid int64, actorSHA string, operationID string, upsert domain.EnvPushUpsert) (string, error) {
	var variableID int64
	if err := tx.QueryRowContext(ctx, `
		insert into secret_variables (team_id, scope_id, env_file_path, name, deleted_at)
		values ($1, $2, $3, $4, null)
		on conflict (team_id, scope_id, env_file_path, name) do update set
			deleted_at = null,
			updated_at = now()
		returning id
	`, teamID, sid, upsert.EnvFilePath, upsert.Name).Scan(&variableID); err != nil {
		return "", err
	}
	var versionNumber int
	if err := tx.QueryRowContext(ctx, `
		select coalesce(max(version_number), 0) + 1 from secret_versions where variable_id = $1
	`, variableID).Scan(&versionNumber); err != nil {
		return "", err
	}
	var versionID int64
	if err := tx.QueryRowContext(ctx, `
		insert into secret_versions (
			variable_id, version_number, ciphertext, nonce, algorithm,
			scope_key_version, created_by_key_sha, operation_id
		)
		values ($1, $2, $3, $4, $5, $6, $7, $8)
		returning id
	`, variableID, versionNumber, upsert.Ciphertext, upsert.Nonce, upsert.Algorithm, upsert.ScopeKeyVersion, actorSHA, operationID).Scan(&versionID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		update secret_variables
		set current_version_id = $1, updated_at = now()
		where id = $2
	`, versionID, variableID); err != nil {
		return "", err
	}
	return versionIDString(versionID), nil
}

func insertAudit(ctx context.Context, tx *sql.Tx, teamID string, actor domain.Member, eventType string, scopeID sql.NullInt64, metadata map[string]any, revision int) (string, error) {
	payload, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	var id int64
	if scopeID.Valid {
		err = tx.QueryRowContext(ctx, `
			insert into audit_events (
				team_id, actor_key_sha, actor_handle, event_type, scope_id, config_revision, metadata
			)
			values ($1, $2, $3, $4, $5, $6, $7::jsonb)
			returning id
		`, teamID, actor.PublicKeySHA, actor.Handle, eventType, scopeID.Int64, revision, payload).Scan(&id)
	} else {
		err = tx.QueryRowContext(ctx, `
			insert into audit_events (
				team_id, actor_key_sha, actor_handle, event_type, config_revision, metadata
			)
			values ($1, $2, $3, $4, $5, $6::jsonb)
			returning id
		`, teamID, actor.PublicKeySHA, actor.Handle, eventType, revision, payload).Scan(&id)
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("audit_%05d", id), nil
}

func lastPullsSQL(ctx context.Context, db *sql.DB, teamID string) ([]domain.PullActivity, error) {
	rows, err := db.QueryContext(ctx, `
		select e.actor_key_sha, e.actor_handle, coalesce(s.name, ''), max(e.created_at)
		from audit_events e
		left join scopes s on s.id = e.scope_id
		where e.team_id = $1 and e.event_type = 'env_pulled'
		group by e.actor_key_sha, e.actor_handle, s.name
		order by max(e.created_at) desc
	`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pulls []domain.PullActivity
	for rows.Next() {
		var item domain.PullActivity
		var pulledAt time.Time
		if err := rows.Scan(&item.MemberPublicKeySHA, &item.Handle, &item.Scope, &pulledAt); err != nil {
			return nil, err
		}
		item.LastPulledAt = pulledAt.UTC().Format(time.RFC3339)
		pulls = append(pulls, item)
	}
	return pulls, rows.Err()
}

func memberScopesSQL(ctx context.Context, db *sql.DB, teamID string, publicKeySHA string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `
		select s.name, r.permission
		from scope_access_rules r
		join scopes s on s.id = r.scope_id
		where r.team_id = $1
			and r.subject_type = 'member'
			and r.subject_value = $2
			and r.active = true
			and s.archived_at is null
		order by s.name, r.id
	`, teamID, publicKeySHA)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var scope, permission string
		if err := rows.Scan(&scope, &permission); err != nil {
			return nil, err
		}
		out[scope] = permission
	}
	return out, rows.Err()
}

func versionIDString(id int64) string {
	if id <= 0 {
		return ""
	}
	return fmt.Sprintf("ver_%05d", id)
}
