package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"propagate/backend/internal/domain"

	"golang.org/x/crypto/bcrypt"
)

type MemoryStore struct {
	mu         sync.Mutex
	nonces     map[string]time.Time
	operations map[string]setupOperation
	teams      map[string]*memoryTeam
}

type setupOperation struct {
	fingerprint string
	result      domain.SetupResult
}

type memoryOperation[T any] struct {
	fingerprint string
	result      T
}

type memoryTeam struct {
	id             string
	name           string
	revision       int
	configHash     string
	configSnapshot json.RawMessage
	members        map[string]domain.Member
	scopes         map[string]*memoryScope
	configOps      map[string]memoryOperation[domain.ConfigPushResult]
	envOps         map[string]memoryOperation[domain.EnvPushResult]
	audit          []memoryAudit
	invites        map[string]*memoryInvite
}

type memoryInvite struct {
	id                  string
	label               string
	pinVerifier         []byte
	status              string
	failedAttempts      int
	requestedManagement bool
	requestedScopes     map[string]string
	scopeKeyBundle      []domain.RelayScopeKey
	createdBy           string
	createdAt           time.Time
	redeemedAt          time.Time
	redeemedByKeySHA    string
}

type memoryScope struct {
	id           string
	name         string
	envFiles     []string
	memberAccess map[string]string
	envelopes    map[string]domain.ScopeKeyEnvelope
	variables    map[string]*memoryVariable
}

type memoryVariable struct {
	envFilePath      string
	name             string
	currentVersionID string
	versions         []domain.SecretVersionRecord
	deleted          bool
	lastUpdatedBy    string
	lastUpdatedAt    string
}

type memoryAudit struct {
	id        string
	eventType string
	actor     domain.Member
	scope     string
	metadata  map[string]any
	created   time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		nonces:     map[string]time.Time{},
		operations: map[string]setupOperation{},
		teams:      map[string]*memoryTeam{},
	}
}

func (s *MemoryStore) ReserveNonce(_ context.Context, publicKeySHA string, nonce string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	for key, expiry := range s.nonces {
		if !expiry.After(now) {
			delete(s.nonces, key)
		}
	}

	key := publicKeySHA + "\x00" + nonce
	if _, exists := s.nonces[key]; exists {
		return ErrReplayRejected
	}
	s.nonces[key] = expiresAt
	return nil
}

func (s *MemoryStore) CreateTeamSetup(_ context.Context, request domain.TeamSetupRequest, configHash string) (domain.SetupResult, error) {
	fingerprint, err := domain.Fingerprint(request)
	if err != nil {
		return domain.SetupResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.operations[request.OperationID]; ok {
		if existing.fingerprint != fingerprint {
			return domain.SetupResult{}, ErrIdempotencyConflict
		}
		return existing.result, nil
	}

	teamID, err := setupTeamID(request.ConfigSnapshot)
	if err != nil {
		return domain.SetupResult{}, err
	}
	configSnapshot, configHash, err := domain.CanonicalSetupConfigSnapshot(request, teamID)
	if err != nil {
		return domain.SetupResult{}, err
	}
	team := &memoryTeam{
		id:             teamID,
		name:           request.TeamName,
		revision:       1,
		configHash:     configHash,
		configSnapshot: append(json.RawMessage(nil), configSnapshot...),
		members:        map[string]domain.Member{},
		scopes:         map[string]*memoryScope{},
		configOps:      map[string]memoryOperation[domain.ConfigPushResult]{},
		envOps:         map[string]memoryOperation[domain.EnvPushResult]{},
		invites:        map[string]*memoryInvite{},
	}
	team.members[request.FirstAdmin.PublicKeySHA] = domain.Member{
		PublicIdentity: request.FirstAdmin,
		Management:     true,
		Scopes:         setupMemberScopes(request.Scopes),
		Status:         "active",
	}

	for idx, scope := range request.Scopes {
		team.scopes[scope.Name] = &memoryScope{
			id:           fmt.Sprintf("scope_%d", idx+1),
			name:         scope.Name,
			envFiles:     append([]string(nil), scope.EnvFiles...),
			memberAccess: map[string]string{},
			envelopes:    map[string]domain.ScopeKeyEnvelope{},
			variables:    map[string]*memoryVariable{},
		}
	}
	for _, scope := range team.scopes {
		scope.memberAccess[request.FirstAdmin.PublicKeySHA] = "write"
	}

	for _, envelope := range request.ScopeKeyEnvelopes {
		scope := team.scopes[envelope.Scope]
		if scope == nil {
			continue
		}
		scope.envelopes[envelope.RecipientKeySHA] = envelope
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, version := range request.EncryptedSecretVersions {
		scope := team.scopes[version.Scope]
		if scope == nil {
			continue
		}
		key := variableKey(version.EnvFilePath, version.Name)
		versionID := "ver_00001"
		scope.variables[key] = &memoryVariable{
			envFilePath:      version.EnvFilePath,
			name:             version.Name,
			currentVersionID: versionID,
			versions: []domain.SecretVersionRecord{{
				Name:             version.Name,
				EnvFilePath:      version.EnvFilePath,
				CurrentVersionID: versionID,
				Ciphertext:       version.Ciphertext,
				Nonce:            version.Nonce,
				Algorithm:        version.Algorithm,
				ScopeKeyVersion:  version.ScopeKeyVersion,
			}},
			lastUpdatedBy: request.FirstAdmin.PublicKeySHA,
			lastUpdatedAt: now,
		}
	}

	result := domain.SetupResult{
		TeamID:                  teamID,
		ConfigRevision:          domain.RevisionString(1),
		ConfigHash:              configHash,
		ScopesCreated:           scopeNames(request.Scopes),
		EncryptedVariablesCount: len(request.EncryptedSecretVersions),
		EnvelopesCount:          len(request.ScopeKeyEnvelopes),
	}
	team.audit = append(team.audit, memoryAudit{
		id:        "audit_00001",
		eventType: "team_created",
		actor:     team.members[request.FirstAdmin.PublicKeySHA],
		metadata: map[string]any{
			"operation_id":              request.OperationID,
			"encrypted_variables_count": len(request.EncryptedSecretVersions),
			"envelopes_count":           len(request.ScopeKeyEnvelopes),
			"scopes_count":              len(request.Scopes),
		},
		created: time.Now().UTC(),
	})
	s.operations[request.OperationID] = setupOperation{fingerprint: fingerprint, result: result}
	s.teams[teamID] = team
	return result, nil
}

func (s *MemoryStore) GetMember(_ context.Context, teamID string, publicKeySHA string) (domain.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	team := s.teams[teamID]
	if team == nil {
		return domain.Member{}, ErrNotFound
	}
	member, ok := team.members[publicKeySHA]
	if !ok || member.Status != "active" {
		return domain.Member{}, ErrPermissionDenied
	}
	return member, nil
}

func (s *MemoryStore) ConfigStatus(_ context.Context, teamID string, actor domain.Member, localRevision string, localConfigHash string) (domain.ConfigStatusData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	team, err := s.teamForActor(teamID, actor)
	if err != nil {
		return domain.ConfigStatusData{}, err
	}
	cloudRevision := domain.RevisionString(team.revision)
	state := "unknown"
	action := "none"
	if localRevision == "" && localConfigHash == "" {
		state = "unknown"
		action = "pull"
	} else if localRevision == cloudRevision && localConfigHash == team.configHash {
		state = "equal"
		action = "none"
	} else if localRevision == cloudRevision {
		state = "local_ahead"
		action = "push"
	} else if localNum, err := domain.RevisionNumber(localRevision); err == nil && localNum < team.revision {
		state = "cloud_ahead"
		action = "pull"
	} else {
		state = "conflict"
		action = "resolve_conflict"
	}
	return domain.ConfigStatusData{
		LocalRevision:     localRevision,
		CloudRevision:     cloudRevision,
		LocalConfigHash:   localConfigHash,
		CloudConfigHash:   team.configHash,
		State:             state,
		RecommendedAction: action,
		SafeSummary: map[string]any{
			"members_count": len(team.members),
			"scopes_count":  len(team.scopes),
		},
	}, nil
}

func (s *MemoryStore) GetConfig(_ context.Context, teamID string, actor domain.Member, serverTime string) (domain.ConfigData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	team, err := s.teamForActor(teamID, actor)
	if err != nil {
		return domain.ConfigData{}, err
	}
	return domain.ConfigData{
		ConfigRevision: domain.RevisionString(team.revision),
		ConfigHash:     team.configHash,
		ConfigSnapshot: append(json.RawMessage(nil), team.configSnapshot...),
		ServerTime:     serverTime,
	}, nil
}

func (s *MemoryStore) PushConfig(_ context.Context, teamID string, actor domain.Member, request domain.ConfigPushRequest) (domain.ConfigPushResult, error) {
	fingerprint, err := domain.FingerprintConfigPush(request)
	if err != nil {
		return domain.ConfigPushResult{}, err
	}
	configHash, err := domain.ConfigHash(request.TargetConfigSnapshot)
	if err != nil {
		return domain.ConfigPushResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	team, err := s.teamForActor(teamID, actor)
	if err != nil {
		return domain.ConfigPushResult{}, err
	}
	if !domain.MemberCanManage(actor) {
		return domain.ConfigPushResult{}, ErrPermissionDenied
	}
	if existing, ok := team.configOps[request.OperationID]; ok {
		if existing.fingerprint != fingerprint {
			return domain.ConfigPushResult{}, ErrIdempotencyConflict
		}
		return existing.result, nil
	}
	expected, err := domain.RevisionNumber(request.ExpectedConfigRevision)
	if err != nil {
		return domain.ConfigPushResult{}, err
	}
	if expected != team.revision {
		return domain.ConfigPushResult{}, ErrRevisionConflict
	}

	applySnapshot(team, request.TargetConfigSnapshot)
	applyApprovedAccessDecisions(team, request.Decisions.Approved)
	for _, envelope := range request.ScopeKeyEnvelopes {
		scope := team.scopes[envelope.Scope]
		if scope == nil {
			continue
		}
		scope.envelopes[envelope.RecipientKeySHA] = envelope
	}

	oldRevision := team.revision
	team.revision++
	team.configHash = configHash
	team.configSnapshot = append(json.RawMessage(nil), request.TargetConfigSnapshot...)
	result := domain.ConfigPushResult{
		OldRevision:      domain.RevisionString(oldRevision),
		NewRevision:      domain.RevisionString(team.revision),
		ConfigHash:       configHash,
		AppliedDecisions: request.Decisions,
		EnvelopesCount:   len(request.ScopeKeyEnvelopes),
		AuditEventsCount: 1,
	}
	team.configOps[request.OperationID] = memoryOperation[domain.ConfigPushResult]{fingerprint: fingerprint, result: result}
	team.audit = append(team.audit, memoryAudit{
		id:        fmt.Sprintf("audit_%05d", len(team.audit)+1),
		eventType: "config_pushed",
		actor:     actor,
		metadata:  map[string]any{"operation_id": request.OperationID},
		created:   time.Now().UTC(),
	})
	return result, nil
}

func (s *MemoryStore) GetKeyEnvelope(_ context.Context, teamID string, scopeName string, actor domain.Member) (domain.ScopeEnvelopeData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	team, scope, err := s.scopeForActor(teamID, scopeName, actor, "read")
	if err != nil {
		return domain.ScopeEnvelopeData{}, err
	}
	envelope, ok := scope.envelopes[actor.PublicKeySHA]
	if !ok {
		return domain.ScopeEnvelopeData{}, ErrPermissionDenied
	}
	return domain.ScopeEnvelopeData{
		Scope:            domain.ScopeRef{ID: scope.id, Name: scope.name},
		ConfigRevision:   domain.RevisionString(team.revision),
		ScopeKeyVersion:  envelope.ScopeKeyVersion,
		ScopeKeyEnvelope: envelope,
		Algorithm:        envelope.Algorithm,
	}, nil
}

func (s *MemoryStore) PullBundle(_ context.Context, teamID string, scopeName string, actor domain.Member) (domain.PullBundleData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	team, scope, err := s.scopeForActor(teamID, scopeName, actor, "read")
	if err != nil {
		return domain.PullBundleData{}, err
	}
	envelope, ok := scope.envelopes[actor.PublicKeySHA]
	if !ok {
		return domain.PullBundleData{}, ErrPermissionDenied
	}
	var variables []domain.VariableMetadata
	var versions []domain.SecretVersionRecord
	for _, variable := range sortedVariables(scope.variables) {
		if variable.deleted {
			continue
		}
		variables = append(variables, variable.metadata())
		if len(variable.versions) > 0 {
			versions = append(versions, variable.versions[len(variable.versions)-1])
		}
	}
	return domain.PullBundleData{
		Scope:            domain.ScopeRef{ID: scope.id, Name: scope.name},
		ConfigRevision:   domain.RevisionString(team.revision),
		EnvFileMappings:  append([]string(nil), scope.envFiles...),
		ScopeKeyEnvelope: envelope,
		Variables:        variables,
		SecretVersions:   versions,
	}, nil
}

func (s *MemoryStore) EnvPush(_ context.Context, teamID string, scopeName string, actor domain.Member, request domain.EnvPushRequest) (domain.EnvPushResult, error) {
	fingerprint, err := domain.FingerprintEnvPush(request)
	if err != nil {
		return domain.EnvPushResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	team, scope, err := s.scopeForActor(teamID, scopeName, actor, "write")
	if err != nil {
		return domain.EnvPushResult{}, err
	}
	if existing, ok := team.envOps[request.OperationID]; ok {
		if existing.fingerprint != fingerprint {
			return domain.EnvPushResult{}, ErrIdempotencyConflict
		}
		return existing.result, nil
	}
	expected, err := domain.RevisionNumber(request.ExpectedConfigRevision)
	if err != nil {
		return domain.EnvPushResult{}, err
	}
	if expected != team.revision {
		return domain.EnvPushResult{}, ErrRevisionConflict
	}

	var conflicts []domain.SecretConflict
	for _, upsert := range request.Upserts {
		variable := scope.variables[variableKey(upsert.EnvFilePath, upsert.Name)]
		current := ""
		if variable != nil && !variable.deleted {
			current = variable.currentVersionID
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
		variable := scope.variables[variableKey(removal.EnvFilePath, removal.Name)]
		current := ""
		if variable != nil && !variable.deleted {
			current = variable.currentVersionID
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
	if len(conflicts) > 0 {
		return domain.EnvPushResult{Conflicts: conflicts}, ErrSecretConflict
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var created []domain.CreatedVersion
	for _, upsert := range request.Upserts {
		key := variableKey(upsert.EnvFilePath, upsert.Name)
		variable := scope.variables[key]
		if variable == nil {
			variable = &memoryVariable{envFilePath: upsert.EnvFilePath, name: upsert.Name}
			scope.variables[key] = variable
		}
		versionID := fmt.Sprintf("ver_%05d", len(variable.versions)+1)
		record := domain.SecretVersionRecord{
			Name:             upsert.Name,
			EnvFilePath:      upsert.EnvFilePath,
			CurrentVersionID: versionID,
			Ciphertext:       upsert.Ciphertext,
			Nonce:            upsert.Nonce,
			Algorithm:        upsert.Algorithm,
			ScopeKeyVersion:  upsert.ScopeKeyVersion,
		}
		variable.versions = append(variable.versions, record)
		variable.currentVersionID = versionID
		variable.deleted = false
		variable.lastUpdatedBy = actor.PublicKeySHA
		variable.lastUpdatedAt = now
		created = append(created, domain.CreatedVersion{EnvFilePath: upsert.EnvFilePath, Name: upsert.Name, VersionID: versionID})
	}

	var removed []domain.RemovedVariable
	for _, removal := range request.Removals {
		key := variableKey(removal.EnvFilePath, removal.Name)
		variable := scope.variables[key]
		if variable == nil {
			variable = &memoryVariable{envFilePath: removal.EnvFilePath, name: removal.Name}
			scope.variables[key] = variable
		}
		variable.deleted = true
		variable.lastUpdatedBy = actor.PublicKeySHA
		variable.lastUpdatedAt = now
		removed = append(removed, domain.RemovedVariable{EnvFilePath: removal.EnvFilePath, Name: removal.Name})
	}

	result := domain.EnvPushResult{CreatedVersions: created, RemovedVariables: removed, AuditEventsCount: 1}
	configRevision := team.revision
	if len(request.TargetConfigSnapshot) > 0 {
		hash, err := domain.ConfigHash(request.TargetConfigSnapshot)
		if err != nil {
			return domain.EnvPushResult{}, err
		}
		configRevision++
		team.revision = configRevision
		team.configHash = hash
		team.configSnapshot = append(json.RawMessage(nil), request.TargetConfigSnapshot...)
		result.ConfigRevision = domain.RevisionString(configRevision)
		result.ConfigHash = hash
	}
	team.envOps[request.OperationID] = memoryOperation[domain.EnvPushResult]{fingerprint: fingerprint, result: result}
	team.audit = append(team.audit, memoryAudit{
		id:        fmt.Sprintf("audit_%05d", len(team.audit)+1),
		eventType: "env_pushed",
		actor:     actor,
		scope:     scope.name,
		metadata:  map[string]any{"operation_id": request.OperationID, "upserts": len(request.Upserts), "removals": len(request.Removals)},
		created:   time.Now().UTC(),
	})
	return result, nil
}

func (s *MemoryStore) EnvStatus(_ context.Context, teamID string, scopeName string, actor domain.Member) (domain.EnvStatusData, error) {
	bundle, err := s.PullBundle(context.Background(), teamID, scopeName, actor)
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

func (s *MemoryStore) RecordPullEvent(_ context.Context, teamID string, actor domain.Member, request domain.PullEventRequest) (domain.PullEventResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, scope, err := s.scopeForActor(teamID, request.Scope, actor, "read")
	if err != nil {
		return domain.PullEventResult{}, err
	}
	team := s.teams[teamID]
	eventID := fmt.Sprintf("audit_%05d", len(team.audit)+1)
	team.audit = append(team.audit, memoryAudit{
		id:        eventID,
		eventType: "env_pulled",
		actor:     actor,
		scope:     scope.name,
		metadata:  map[string]any{"variables_count": request.VariablesCount, "env_file_paths": request.EnvFilePaths},
		created:   time.Now().UTC(),
	})
	return domain.PullEventResult{EventID: eventID, RecordedCount: 1}, nil
}

func (s *MemoryStore) TeamStatus(_ context.Context, teamID string, actor domain.Member) (domain.TeamStatusData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	team, err := s.teamForActor(teamID, actor)
	if err != nil {
		return domain.TeamStatusData{}, err
	}
	actor = team.members[actor.PublicKeySHA]
	actor.Scopes = memberScopes(team, actor.PublicKeySHA)
	membersByRole := map[string][]domain.Member{}
	for _, member := range team.members {
		member.Scopes = memberScopes(team, member.PublicKeySHA)
		membersByRole[memberGroup(member)] = append(membersByRole[memberGroup(member)], member)
	}
	for role := range membersByRole {
		sort.Slice(membersByRole[role], func(i, j int) bool {
			return membersByRole[role][i].PublicKeySHA < membersByRole[role][j].PublicKeySHA
		})
	}

	pulled := map[string]bool{}
	var lastPulls []domain.PullActivity
	for _, event := range team.audit {
		if event.eventType != "env_pulled" {
			continue
		}
		pulled[event.actor.PublicKeySHA] = true
		lastPulls = append(lastPulls, domain.PullActivity{
			MemberPublicKeySHA: event.actor.PublicKeySHA,
			Handle:             event.actor.Handle,
			Scope:              event.scope,
			LastPulledAt:       event.created.Format(time.RFC3339),
		})
	}
	var neverPulled []domain.Member
	for _, member := range team.members {
		if !pulled[member.PublicKeySHA] {
			member.Scopes = memberScopes(team, member.PublicKeySHA)
			neverPulled = append(neverPulled, member)
		}
	}
	var pendingRequests []domain.JoinRequestRow
	if domain.MemberCanManage(actor) {
		for _, member := range team.members {
			if member.Status != "pending" {
				continue
			}
			pendingRequests = append(pendingRequests, domain.JoinRequestRow{
				Handle:              member.Handle,
				PublicKeySHA:        member.PublicKeySHA,
				SigningPublicKey:    member.SigningPublicKey,
				EncryptionPublicKey: member.EncryptionPublicKey,
				RequestedManagement: member.Management,
			})
		}
	}

	return domain.TeamStatusData{
		Team: domain.TeamSummary{
			ID:             team.id,
			Name:           team.name,
			ConfigRevision: domain.RevisionString(team.revision),
			ConfigHash:     team.configHash,
		},
		Actor:               actor,
		Members:             membersByRole,
		PendingJoinRequests: pendingRequests,
		LastPulls:           lastPulls,
		NeverPulled:         neverPulled,
	}, nil
}

func (s *MemoryStore) teamForActor(teamID string, actor domain.Member) (*memoryTeam, error) {
	team := s.teams[teamID]
	if team == nil {
		return nil, ErrNotFound
	}
	current, ok := team.members[actor.PublicKeySHA]
	if !ok || current.Status != "active" {
		return nil, ErrPermissionDenied
	}
	return team, nil
}

func (s *MemoryStore) scopeForActor(teamID string, scopeName string, actor domain.Member, required string) (*memoryTeam, *memoryScope, error) {
	team, err := s.teamForActor(teamID, actor)
	if err != nil {
		return nil, nil, err
	}
	scope := team.scopes[scopeName]
	if scope == nil {
		return nil, nil, ErrNotFound
	}
	permission := effectivePermission(scope, actor)
	if !domain.PermissionAllows(permission, required) {
		return nil, nil, ErrPermissionDenied
	}
	return team, scope, nil
}

func effectivePermission(scope *memoryScope, actor domain.Member) string {
	if permission := scope.memberAccess[actor.PublicKeySHA]; permission != "" {
		return permission
	}
	if actor.Management {
		return "admin"
	}
	return ""
}

func applySnapshot(team *memoryTeam, raw json.RawMessage) {
	var snapshot struct {
		Team struct {
			Name string `json:"name"`
		} `json:"team"`
		Members []struct {
			Handle              string            `json:"handle"`
			PublicKeySHA        string            `json:"public_key_sha"`
			SigningPublicKey    string            `json:"signing_public_key"`
			EncryptionPublicKey string            `json:"encryption_public_key"`
			Management          bool              `json:"management"`
			Scopes              map[string]string `json:"scopes"`
		} `json:"members"`
		Scopes map[string]struct {
			EnvFiles []string `json:"env_files"`
		} `json:"scopes"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return
	}
	if snapshot.Team.Name != "" {
		team.name = snapshot.Team.Name
	}
	for _, member := range snapshot.Members {
		if member.PublicKeySHA == "" {
			continue
		}
		team.members[member.PublicKeySHA] = domain.Member{
			PublicIdentity: domain.PublicIdentity{
				Handle:              member.Handle,
				PublicKeySHA:        member.PublicKeySHA,
				SigningPublicKey:    member.SigningPublicKey,
				EncryptionPublicKey: member.EncryptionPublicKey,
			},
			Management: member.Management,
			Scopes:     copyStringMap(member.Scopes),
			Status:     "active",
		}
	}
	for _, scope := range team.scopes {
		scope.memberAccess = map[string]string{}
	}
	for name, scopeSnapshot := range snapshot.Scopes {
		scope := team.scopes[name]
		if scope == nil {
			scope = &memoryScope{
				id:           fmt.Sprintf("scope_%d", len(team.scopes)+1),
				name:         name,
				memberAccess: map[string]string{},
				envelopes:    map[string]domain.ScopeKeyEnvelope{},
				variables:    map[string]*memoryVariable{},
			}
			team.scopes[name] = scope
		}
		scope.envFiles = append([]string(nil), scopeSnapshot.EnvFiles...)
	}
	for _, member := range team.members {
		for scopeName, permission := range member.Scopes {
			scope := team.scopes[scopeName]
			if scope == nil {
				continue
			}
			scope.memberAccess[member.PublicKeySHA] = permission
		}
	}
}

func applyApprovedAccessDecisions(team *memoryTeam, decisions []domain.ConfigDecision) {
	for _, decision := range decisions {
		if decision.Type != "scope_access_change" || decision.PublicKeySHA == "" || decision.Scope == "" || decision.Permission == "" {
			continue
		}
		scope := team.scopes[decision.Scope]
		if scope == nil {
			continue
		}
		scope.memberAccess[decision.PublicKeySHA] = decision.Permission
		member := team.members[decision.PublicKeySHA]
		if member.PublicKeySHA != "" {
			if member.Scopes == nil {
				member.Scopes = map[string]string{}
			}
			member.Scopes[decision.Scope] = decision.Permission
			team.members[decision.PublicKeySHA] = member
		}
	}
}

func (v *memoryVariable) metadata() domain.VariableMetadata {
	return domain.VariableMetadata{
		Name:             v.name,
		EnvFilePath:      v.envFilePath,
		CurrentVersionID: v.currentVersionID,
		LastUpdatedBy:    v.lastUpdatedBy,
		LastUpdatedAt:    v.lastUpdatedAt,
	}
}

func sortedVariables(values map[string]*memoryVariable) []*memoryVariable {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]*memoryVariable, 0, len(keys))
	for _, key := range keys {
		out = append(out, values[key])
	}
	return out
}

func copyStringMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func setupMemberScopes(scopes []domain.SetupScope) map[string]string {
	out := map[string]string{}
	for _, scope := range scopes {
		out[scope.Name] = "write"
	}
	return out
}

func memberScopes(team *memoryTeam, publicKeySHA string) map[string]string {
	out := map[string]string{}
	for name, scope := range team.scopes {
		if permission := scope.memberAccess[publicKeySHA]; permission != "" {
			out[name] = permission
		}
	}
	return out
}

func memberGroup(member domain.Member) string {
	if domain.MemberCanManage(member) {
		return "management"
	}
	return "members"
}

func scopeNames(scopes []domain.SetupScope) []string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, scope.Name)
	}
	return out
}

func variableKey(path, name string) string {
	return path + "\x00" + name
}

func randomInviteID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "inv_" + hex.EncodeToString(raw), nil
}

func (s *MemoryStore) CreateTeamInvite(_ context.Context, teamID string, actor domain.Member, request domain.CreateTeamInviteRequest) (domain.CreateTeamInviteResult, error) {
	scopes := copyStringMap(request.RequestedScopes)

	s.mu.Lock()
	defer s.mu.Unlock()

	team, err := s.teamForActor(teamID, actor)
	if err != nil {
		return domain.CreateTeamInviteResult{}, err
	}
	if !domain.MemberCanManage(actor) {
		return domain.CreateTeamInviteResult{}, ErrPermissionDenied
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
	inviteID, err := randomInviteID()
	if err != nil {
		return domain.CreateTeamInviteResult{}, err
	}
	team.invites[inviteID] = &memoryInvite{
		id:                  inviteID,
		label:               strings.TrimSpace(request.Label),
		pinVerifier:         hash,
		status:              "active",
		requestedManagement: request.RequestedManagement,
		requestedScopes:     scopes,
		scopeKeyBundle:      request.ScopeKeyBundle,
		createdBy:           actor.PublicKeySHA,
		createdAt:           time.Now().UTC(),
	}
	return domain.CreateTeamInviteResult{
		InviteID: inviteID,
		PIN:      pin,
		Label:    strings.TrimSpace(request.Label),
	}, nil
}

func (s *MemoryStore) ListJoinerInvites(_ context.Context, teamID string) (domain.JoinerInvitesData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	team := s.teams[teamID]
	if team == nil {
		return domain.JoinerInvitesData{Invites: []domain.JoinerInviteRow{}}, nil
	}
	var rows []domain.JoinerInviteRow
	for _, inv := range team.invites {
		if inv.status != "active" {
			continue
		}
		rows = append(rows, domain.JoinerInviteRow{
			InviteID:  inv.id,
			Label:     inv.label,
			CreatedAt: inv.createdAt.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].CreatedAt < rows[j].CreatedAt
	})
	return domain.JoinerInvitesData{Invites: rows}, nil
}

func (s *MemoryStore) GetInviteScopeKeyBundle(_ context.Context, teamID string, inviteID string) ([]domain.RelayScopeKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	team := s.teams[teamID]
	if team == nil {
		return nil, ErrNotFound
	}
	inv := team.invites[inviteID]
	if inv == nil {
		return nil, ErrNotFound
	}
	if inv.status != "active" {
		return nil, ErrInviteNotActive
	}
	return inv.scopeKeyBundle, nil
}

func (s *MemoryStore) SubmitInvitePIN(_ context.Context, teamID string, inviteID string, request domain.InvitePINRequest, serverTime string, envelopes []domain.ScopeKeyEnvelope) (domain.InvitePINResult, error) {
	normPIN, err := domain.NormalizeInvitePIN(request.PIN)
	if err != nil {
		return domain.InvitePINResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	team := s.teams[teamID]
	if team == nil {
		return domain.InvitePINResult{}, ErrNotFound
	}
	inv := team.invites[inviteID]
	if inv == nil {
		return domain.InvitePINResult{}, ErrNotFound
	}
	if inv.status != "active" {
		return domain.InvitePINResult{}, ErrInviteNotActive
	}

	if err := bcrypt.CompareHashAndPassword(inv.pinVerifier, []byte(normPIN)); err == nil {
		inv.status = "redeemed"
		inv.redeemedAt = time.Now().UTC()
		inv.redeemedByKeySHA = request.Joiner.PublicKeySHA
		raw := make([]byte, 10)
		if _, rerr := rand.Read(raw); rerr != nil {
			return domain.InvitePINResult{}, rerr
		}
		redemptionID := "red_" + hex.EncodeToString(raw)

		preApproved := len(envelopes) > 0
		var member *domain.Member
		if preApproved {
			newMember := domain.Member{
				PublicIdentity: request.Joiner,
				Management:     inv.requestedManagement,
				Scopes:         copyStringMap(inv.requestedScopes),
				Status:         "active",
			}
			team.members[request.Joiner.PublicKeySHA] = newMember
			for scopeName, permission := range inv.requestedScopes {
				scope := team.scopes[scopeName]
				if scope == nil {
					continue
				}
				scope.memberAccess[request.Joiner.PublicKeySHA] = permission
			}
			for _, envelope := range envelopes {
				scope := team.scopes[envelope.Scope]
				if scope == nil {
					continue
				}
				scope.envelopes[envelope.RecipientKeySHA] = envelope
			}
			team.revision++
			member = &newMember
			team.audit = append(team.audit, memoryAudit{
				id:        fmt.Sprintf("audit_%05d", len(team.audit)+1),
				eventType: "invite_redeemed_pre_approved",
				actor:     newMember,
				metadata:  map[string]any{"invite_id": inviteID, "redemption_id": redemptionID},
				created:   time.Now().UTC(),
			})
		} else {
			team.audit = append(team.audit, memoryAudit{
				id:        fmt.Sprintf("audit_%05d", len(team.audit)+1),
				eventType: "invite_redeemed",
				actor:     domain.Member{PublicIdentity: request.Joiner},
				metadata:  map[string]any{"invite_id": inviteID, "redemption_id": redemptionID},
				created:   time.Now().UTC(),
			})
		}

		return domain.InvitePINResult{
			RedemptionID:      redemptionID,
			InviteID:          inviteID,
			ServerTime:        serverTime,
			PreApproved:       preApproved,
			ScopeKeyEnvelopes: envelopes,
			Member:            member,
		}, nil
	}

	inv.failedAttempts++
	if inv.failedAttempts >= 3 {
		inv.status = "invalidated_pin"
		return domain.InvitePINResult{}, ErrInviteLocked
	}
	return domain.InvitePINResult{}, ErrInvitePINInvalid
}

func (s *MemoryStore) ListAdminInvites(_ context.Context, teamID string, actor domain.Member) (domain.AdminInvitesData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	team, err := s.teamForActor(teamID, actor)
	if err != nil {
		return domain.AdminInvitesData{}, err
	}
	if !domain.MemberCanManage(actor) {
		return domain.AdminInvitesData{}, ErrPermissionDenied
	}
	var rows []domain.AdminInviteRow
	for _, inv := range team.invites {
		row := domain.AdminInviteRow{
			InviteID:          inv.id,
			Label:             inv.label,
			Status:            inv.status,
			FailedPINAttempts: inv.failedAttempts,
			CreatedAt:         inv.createdAt.UTC().Format(time.RFC3339),
		}
		if !inv.redeemedAt.IsZero() {
			row.RedeemedAt = inv.redeemedAt.UTC().Format(time.RFC3339)
			row.RedeemedByKeySHA = inv.redeemedByKeySHA
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].CreatedAt < rows[j].CreatedAt
	})
	return domain.AdminInvitesData{Invites: rows}, nil
}

func (s *MemoryStore) RevokeTeamInvite(_ context.Context, teamID string, inviteID string, actor domain.Member) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	team, err := s.teamForActor(teamID, actor)
	if err != nil {
		return err
	}
	if !domain.MemberCanManage(actor) {
		return ErrPermissionDenied
	}
	inv := team.invites[inviteID]
	if inv == nil {
		return ErrNotFound
	}
	if inv.status != "active" {
		return ErrInviteNotActive
	}
	inv.status = "revoked"
	return nil
}

func (s *MemoryStore) CreateJoinRequest(_ context.Context, teamID string, request domain.JoinRequestSubmission, serverTime string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	team := s.teams[teamID]
	if team == nil {
		return ErrNotFound
	}
	if _, exists := team.members[request.Joiner.PublicKeySHA]; exists {
		return ErrJoinRequestDuplicate
	}
	team.members[request.Joiner.PublicKeySHA] = domain.Member{
		PublicIdentity: request.Joiner,
		Management:     request.RequestedManagement,
		Status:         "pending",
	}
	return nil
}

func (s *MemoryStore) ListPendingJoinRequests(_ context.Context, teamID string, actor domain.Member) (domain.PendingJoinRequestsData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	team, err := s.teamForActor(teamID, actor)
	if err != nil {
		return domain.PendingJoinRequestsData{}, err
	}
	if !domain.MemberCanManage(actor) {
		return domain.PendingJoinRequestsData{}, ErrPermissionDenied
	}
	var requests []domain.JoinRequestRow
	for _, member := range team.members {
		if member.Status != "pending" {
			continue
		}
		requests = append(requests, domain.JoinRequestRow{
			Handle:              member.Handle,
			PublicKeySHA:        member.PublicKeySHA,
			SigningPublicKey:    member.SigningPublicKey,
			EncryptionPublicKey: member.EncryptionPublicKey,
			RequestedManagement: member.Management,
		})
	}
	if requests == nil {
		requests = []domain.JoinRequestRow{}
	}
	return domain.PendingJoinRequestsData{Requests: requests}, nil
}

func (s *MemoryStore) ApproveJoinRequest(_ context.Context, teamID string, publicKeySHA string, actor domain.Member, request domain.ApproveJoinRequestBody) (domain.ApproveJoinResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	team, err := s.teamForActor(teamID, actor)
	if err != nil {
		return domain.ApproveJoinResult{}, err
	}
	if !domain.MemberCanManage(actor) {
		return domain.ApproveJoinResult{}, ErrPermissionDenied
	}
	member, ok := team.members[publicKeySHA]
	if !ok || member.Status != "pending" {
		return domain.ApproveJoinResult{}, ErrJoinRequestNotFound
	}

	member.Management = request.GrantedManagement
	member.Status = "active"
	member.Scopes = copyStringMap(request.GrantedScopes)
	team.members[publicKeySHA] = member

	for scopeName, permission := range request.GrantedScopes {
		scope := team.scopes[scopeName]
		if scope == nil {
			continue
		}
		scope.memberAccess[publicKeySHA] = permission
	}
	for _, envelope := range request.ScopeKeyEnvelopes {
		scope := team.scopes[envelope.Scope]
		if scope == nil {
			continue
		}
		scope.envelopes[envelope.RecipientKeySHA] = envelope
	}
	team.revision++

	team.audit = append(team.audit, memoryAudit{
		id:        fmt.Sprintf("audit_%05d", len(team.audit)+1),
		eventType: "join_request_approved",
		actor:     actor,
		metadata:  map[string]any{"approved_member": publicKeySHA},
		created:   time.Now().UTC(),
	})

	return domain.ApproveJoinResult{
		MemberPublicKeySHA: publicKeySHA,
		ConfigRevision:     domain.RevisionString(team.revision),
	}, nil
}

func (s *MemoryStore) DeclineJoinRequest(_ context.Context, teamID string, publicKeySHA string, actor domain.Member, request domain.DeclineJoinRequestBody) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	team, err := s.teamForActor(teamID, actor)
	if err != nil {
		return err
	}
	if !domain.MemberCanManage(actor) {
		return ErrPermissionDenied
	}
	member, ok := team.members[publicKeySHA]
	if !ok || member.Status != "pending" {
		return ErrJoinRequestNotFound
	}
	member.Status = "declined"
	team.members[publicKeySHA] = member
	team.audit = append(team.audit, memoryAudit{
		id:        fmt.Sprintf("audit_%05d", len(team.audit)+1),
		eventType: "join_request_declined",
		actor:     actor,
		metadata:  map[string]any{"declined_member": publicKeySHA},
		created:   time.Now().UTC(),
	})
	return nil
}

func randomTeamID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "team_" + hex.EncodeToString(raw), nil
}

func setupTeamID(snapshot json.RawMessage) (string, error) {
	teamID, err := domain.ConfigSnapshotTeamID(snapshot)
	if err != nil {
		return "", err
	}
	if teamID != "" {
		return teamID, nil
	}
	return randomTeamID()
}

func isNoRows(err error) bool {
	return errors.Is(err, ErrNotFound)
}
