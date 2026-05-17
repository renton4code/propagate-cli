package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"propagate/backend/internal/signing"
	"propagate/backend/internal/storage"
)

func TestVersionEndpoint(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := NewServer(storage.NewMemoryStore(), Config{
		APIVersion:    "test-api",
		MinCLIVersion: "test-cli",
		Now:           func() time.Time { return now },
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/version", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"api_version": "test-api"`) {
		t.Fatalf("version response missing api version: %s", rec.Body.String())
	}
}

func TestTeamSetupRequiresValidSignatureAndStoresSetup(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := NewServer(storage.NewMemoryStore(), Config{Now: func() time.Time { return now }})
	signer := newTestSigner(t)
	body := setupBody(t, signer, "op_setup_1", false)

	req := signedSetupRequest(t, signer, body, now, "nonce-1")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var envelope Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK {
		t.Fatalf("expected ok envelope: %s", rec.Body.String())
	}
	payload, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"encrypted_variables_count":1`) {
		t.Fatalf("setup result missing encrypted variable count: %s", payload)
	}
}

func TestTeamSetupRejectsReplayedNonce(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := NewServer(storage.NewMemoryStore(), Config{Now: func() time.Time { return now }})
	signer := newTestSigner(t)
	body := setupBody(t, signer, "op_setup_replay", false)

	first := signedSetupRequest(t, signer, body, now, "nonce-reused")
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusCreated {
		t.Fatalf("first status = %d, body = %s", firstRec.Code, firstRec.Body.String())
	}

	second := signedSetupRequest(t, signer, body, now, "nonce-reused")
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, second)
	if secondRec.Code != http.StatusUnauthorized {
		t.Fatalf("second status = %d, body = %s", secondRec.Code, secondRec.Body.String())
	}
	if !strings.Contains(secondRec.Body.String(), `"code": "replay_rejected"`) {
		t.Fatalf("expected replay error, got: %s", secondRec.Body.String())
	}
}

func TestTeamSetupIdempotentRetryWithFreshNonce(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := NewServer(storage.NewMemoryStore(), Config{Now: func() time.Time { return now }})
	signer := newTestSigner(t)
	body := setupBody(t, signer, "op_setup_idempotent", false)

	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, signedSetupRequest(t, signer, body, now, "nonce-a"))
	if firstRec.Code != http.StatusCreated {
		t.Fatalf("first status = %d, body = %s", firstRec.Code, firstRec.Body.String())
	}
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, signedSetupRequest(t, signer, body, now, "nonce-b"))
	if secondRec.Code != http.StatusCreated {
		t.Fatalf("second status = %d, body = %s", secondRec.Code, secondRec.Body.String())
	}

	firstTeamID := teamIDFromBody(t, firstRec.Body.Bytes())
	secondTeamID := teamIDFromBody(t, secondRec.Body.Bytes())
	if firstTeamID != secondTeamID {
		t.Fatalf("team id changed across idempotent retry: %s != %s", firstTeamID, secondTeamID)
	}
}

func TestTeamSetupRejectsPlaintextFieldsInConfigSnapshot(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := NewServer(storage.NewMemoryStore(), Config{Now: func() time.Time { return now }})
	signer := newTestSigner(t)
	body := setupBody(t, signer, "op_setup_plaintext", true)

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, signedSetupRequest(t, signer, body, now, "nonce-plaintext"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code": "validation_failed"`) {
		t.Fatalf("expected validation error, got: %s", rec.Body.String())
	}
}

func TestProtectedConfigAndPullEndpoints(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := NewServer(storage.NewMemoryStore(), Config{Now: func() time.Time { return now }})
	signer := newTestSigner(t)
	body := setupBody(t, signer, "op_setup_protected", false)
	setupRec := httptest.NewRecorder()
	server.ServeHTTP(setupRec, signedSetupRequest(t, signer, body, now, "nonce-setup-protected"))
	if setupRec.Code != http.StatusCreated {
		t.Fatalf("setup status = %d, body = %s", setupRec.Code, setupRec.Body.String())
	}
	teamID, configHash := setupFactsFromBody(t, setupRec.Body.Bytes())

	statusPath := "/v1/teams/" + teamID + "/config/status?local_revision=rev_00001&local_config_hash=" + configHash
	statusRec := httptest.NewRecorder()
	server.ServeHTTP(statusRec, signedRequest(t, signer, http.MethodGet, statusPath, nil, now, "nonce-status", ""))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("config status code = %d, body = %s", statusRec.Code, statusRec.Body.String())
	}
	if !strings.Contains(statusRec.Body.String(), `"state": "equal"`) {
		t.Fatalf("expected equal config status, got: %s", statusRec.Body.String())
	}

	configRec := httptest.NewRecorder()
	server.ServeHTTP(configRec, signedRequest(t, signer, http.MethodGet, "/v1/teams/"+teamID+"/config", nil, now, "nonce-config", ""))
	if configRec.Code != http.StatusOK {
		t.Fatalf("config get code = %d, body = %s", configRec.Code, configRec.Body.String())
	}
	if !strings.Contains(configRec.Body.String(), `"config_revision": "rev_00001"`) {
		t.Fatalf("config response missing revision: %s", configRec.Body.String())
	}
	if !strings.Contains(configRec.Body.String(), `"id": "`+teamID+`"`) || !strings.Contains(configRec.Body.String(), `"members": [`) {
		t.Fatalf("config response missing canonical team metadata: %s", configRec.Body.String())
	}

	bundleRec := httptest.NewRecorder()
	server.ServeHTTP(bundleRec, signedRequest(t, signer, http.MethodGet, "/v1/teams/"+teamID+"/scopes/dev/pull-bundle", nil, now, "nonce-bundle", ""))
	if bundleRec.Code != http.StatusOK {
		t.Fatalf("pull bundle code = %d, body = %s", bundleRec.Code, bundleRec.Body.String())
	}
	for _, want := range []string{`"DATABASE_URL"`, `"ciphertext"`, `"encrypted-scope-key"`} {
		if !strings.Contains(bundleRec.Body.String(), want) {
			t.Fatalf("pull bundle missing %s: %s", want, bundleRec.Body.String())
		}
	}
}

func TestConfigPushUpdatesRevision(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := NewServer(storage.NewMemoryStore(), Config{Now: func() time.Time { return now }})
	signer := newTestSigner(t)
	setup := setupBody(t, signer, "op_setup_config_push", false)
	setupRec := httptest.NewRecorder()
	server.ServeHTTP(setupRec, signedSetupRequest(t, signer, setup, now, "nonce-config-push-setup"))
	if setupRec.Code != http.StatusCreated {
		t.Fatalf("setup status = %d, body = %s", setupRec.Code, setupRec.Body.String())
	}
	teamID, _ := setupFactsFromBody(t, setupRec.Body.Bytes())

	target := map[string]any{
		"version": float64(1),
		"team": map[string]any{
			"id":   teamID,
			"name": "Acme API Renamed",
		},
		"scopes": map[string]any{
			"dev": map[string]any{
				"env_files": []any{".env", ".env.local"},
				"default_role_access": map[string]any{
					"admins":     "write",
					"developers": "read",
				},
			},
		},
		"members": []any{
			map[string]any{
				"handle":                "alice@example.com",
				"public_key_sha":        signer.sha,
				"signing_public_key":    signer.ssh,
				"encryption_public_key": "x25519:dGVzdC1lbmNyeXB0aW9uLXB1YmxpYy1rZXk=",
				"role":                  "admins",
			},
		},
		"pending": map[string]any{"joins": []any{}, "access_changes": []any{}},
	}
	payload := map[string]any{
		"operation_id":             "op_config_push_1",
		"expected_config_revision": "rev_00001",
		"target_config_snapshot":   target,
		"decisions": map[string]any{
			"approved": []any{},
			"declined": []any{},
			"skipped":  []any{},
		},
		"client": map[string]any{"cli_version": "test-cli"},
	}
	pushBody := mustJSON(t, payload)
	pushRec := httptest.NewRecorder()
	server.ServeHTTP(pushRec, signedRequest(t, signer, http.MethodPost, "/v1/teams/"+teamID+"/config/push", pushBody, now, "nonce-config-push", "op_config_push_1"))
	if pushRec.Code != http.StatusOK {
		t.Fatalf("config push code = %d, body = %s", pushRec.Code, pushRec.Body.String())
	}
	if !strings.Contains(pushRec.Body.String(), `"new_revision": "rev_00002"`) {
		t.Fatalf("config push missing new revision: %s", pushRec.Body.String())
	}
}

func TestConfigPushGrantsApprovedMemberScopeAccess(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := NewServer(storage.NewMemoryStore(), Config{Now: func() time.Time { return now }})
	admin := newTestSigner(t)
	member := newTestSigner(t)
	setup := setupBody(t, admin, "op_setup_member_access", false)
	setupRec := httptest.NewRecorder()
	server.ServeHTTP(setupRec, signedSetupRequest(t, admin, setup, now, "nonce-member-access-setup"))
	if setupRec.Code != http.StatusCreated {
		t.Fatalf("setup status = %d, body = %s", setupRec.Code, setupRec.Body.String())
	}
	teamID, _ := setupFactsFromBody(t, setupRec.Body.Bytes())

	target := map[string]any{
		"version": float64(1),
		"team": map[string]any{
			"id":   teamID,
			"name": "Acme API",
		},
		"scopes": map[string]any{
			"dev": map[string]any{
				"env_files": []any{".env"},
				"default_role_access": map[string]any{
					"admins":     "write",
					"developers": "read",
				},
			},
			"prod": map[string]any{
				"env_files": []any{".env.production"},
				"default_role_access": map[string]any{
					"admins": "write",
				},
			},
		},
		"members": []any{
			map[string]any{
				"handle":                "alice@example.com",
				"public_key_sha":        admin.sha,
				"signing_public_key":    admin.ssh,
				"encryption_public_key": "x25519:dGVzdC1lbmNyeXB0aW9uLXB1YmxpYy1rZXk=",
				"role":                  "admins",
			},
			map[string]any{
				"handle":                "bob@example.com",
				"public_key_sha":        member.sha,
				"signing_public_key":    member.ssh,
				"encryption_public_key": "x25519:Ym9iLWVuY3J5cHRpb24tcHVibGljLWtleQ==",
				"role":                  "developers",
			},
		},
		"pending": map[string]any{"joins": []any{}, "access_changes": []any{}},
	}
	payload := map[string]any{
		"operation_id":             "op_config_push_member_access",
		"expected_config_revision": "rev_00001",
		"target_config_snapshot":   target,
		"decisions": map[string]any{
			"approved": []any{
				map[string]any{
					"type":           "join",
					"handle":         "bob@example.com",
					"public_key_sha": member.sha,
					"role":           "developers",
				},
				map[string]any{
					"type":           "scope_access_change",
					"public_key_sha": member.sha,
					"scope":          "prod",
					"permission":     "read",
				},
			},
		},
		"scope_key_envelopes": []any{
			map[string]any{
				"scope":               "prod",
				"recipient_key_sha":   member.sha,
				"scope_key_version":   float64(1),
				"encrypted_scope_key": "bob-prod-envelope",
				"algorithm":           "test-envelope",
			},
		},
		"client": map[string]any{"cli_version": "test-cli"},
	}
	pushBody := mustJSON(t, payload)
	pushRec := httptest.NewRecorder()
	server.ServeHTTP(pushRec, signedRequest(t, admin, http.MethodPost, "/v1/teams/"+teamID+"/config/push", pushBody, now, "nonce-member-access-push", "op_config_push_member_access"))
	if pushRec.Code != http.StatusOK {
		t.Fatalf("config push code = %d, body = %s", pushRec.Code, pushRec.Body.String())
	}

	keyRec := httptest.NewRecorder()
	server.ServeHTTP(keyRec, signedRequest(t, member, http.MethodGet, "/v1/teams/"+teamID+"/scopes/prod/key-envelope", nil, now, "nonce-member-access-key", ""))
	if keyRec.Code != http.StatusOK {
		t.Fatalf("member key envelope code = %d, body = %s", keyRec.Code, keyRec.Body.String())
	}
	if !strings.Contains(keyRec.Body.String(), "bob-prod-envelope") {
		t.Fatalf("member key envelope missing approved envelope: %s", keyRec.Body.String())
	}
}

func TestConfigPushRejectsMismatchedSnapshotTeamID(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := NewServer(storage.NewMemoryStore(), Config{Now: func() time.Time { return now }})
	signer := newTestSigner(t)
	setup := setupBody(t, signer, "op_setup_config_push_mismatch", false)
	setupRec := httptest.NewRecorder()
	server.ServeHTTP(setupRec, signedSetupRequest(t, signer, setup, now, "nonce-config-push-mismatch-setup"))
	if setupRec.Code != http.StatusCreated {
		t.Fatalf("setup status = %d, body = %s", setupRec.Code, setupRec.Body.String())
	}
	teamID, _ := setupFactsFromBody(t, setupRec.Body.Bytes())

	target := map[string]any{
		"version": float64(1),
		"team": map[string]any{
			"id":   "team_other",
			"name": "Acme API",
		},
		"scopes": map[string]any{
			"dev": map[string]any{
				"env_files": []any{".env"},
				"default_role_access": map[string]any{
					"admins":     "write",
					"developers": "read",
				},
			},
		},
		"members": []any{
			map[string]any{
				"handle":                "alice@example.com",
				"public_key_sha":        signer.sha,
				"signing_public_key":    signer.ssh,
				"encryption_public_key": "x25519:dGVzdC1lbmNyeXB0aW9uLXB1YmxpYy1rZXk=",
				"role":                  "admins",
			},
		},
		"pending": map[string]any{"joins": []any{}, "access_changes": []any{}},
	}
	payload := map[string]any{
		"operation_id":             "op_config_push_mismatch",
		"expected_config_revision": "rev_00001",
		"target_config_snapshot":   target,
	}
	pushBody := mustJSON(t, payload)
	pushRec := httptest.NewRecorder()
	server.ServeHTTP(pushRec, signedRequest(t, signer, http.MethodPost, "/v1/teams/"+teamID+"/config/push", pushBody, now, "nonce-config-push-mismatch", "op_config_push_mismatch"))
	if pushRec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("config push code = %d, body = %s", pushRec.Code, pushRec.Body.String())
	}
	if !strings.Contains(pushRec.Body.String(), "team.id must match") {
		t.Fatalf("expected team id validation error, got: %s", pushRec.Body.String())
	}
}

func TestEnvPushStatusAndPullEvent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := NewServer(storage.NewMemoryStore(), Config{Now: func() time.Time { return now }})
	signer := newTestSigner(t)
	setup := setupBody(t, signer, "op_setup_env_push", false)
	setupRec := httptest.NewRecorder()
	server.ServeHTTP(setupRec, signedSetupRequest(t, signer, setup, now, "nonce-env-setup"))
	if setupRec.Code != http.StatusCreated {
		t.Fatalf("setup status = %d, body = %s", setupRec.Code, setupRec.Body.String())
	}
	teamID, _ := setupFactsFromBody(t, setupRec.Body.Bytes())

	envPush := map[string]any{
		"operation_id":             "op_env_push_1",
		"expected_config_revision": "rev_00001",
		"upserts": []any{
			map[string]any{
				"env_file_path":     ".env",
				"name":              "API_TOKEN",
				"ciphertext":        "ciphertext-token",
				"nonce":             "nonce-token",
				"algorithm":         "test-aead",
				"scope_key_version": float64(1),
			},
		},
		"safe_counts": map[string]any{"added": float64(1)},
	}
	envBody := mustJSON(t, envPush)
	envRec := httptest.NewRecorder()
	server.ServeHTTP(envRec, signedRequest(t, signer, http.MethodPost, "/v1/teams/"+teamID+"/scopes/dev/env/push", envBody, now, "nonce-env-push", "op_env_push_1"))
	if envRec.Code != http.StatusOK {
		t.Fatalf("env push code = %d, body = %s", envRec.Code, envRec.Body.String())
	}
	if !strings.Contains(envRec.Body.String(), `"API_TOKEN"`) {
		t.Fatalf("env push response missing variable: %s", envRec.Body.String())
	}

	statusRec := httptest.NewRecorder()
	server.ServeHTTP(statusRec, signedRequest(t, signer, http.MethodGet, "/v1/teams/"+teamID+"/scopes/dev/env/status", nil, now, "nonce-env-status", ""))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("env status code = %d, body = %s", statusRec.Code, statusRec.Body.String())
	}
	if !strings.Contains(statusRec.Body.String(), `"API_TOKEN"`) || !strings.Contains(statusRec.Body.String(), `"DATABASE_URL"`) {
		t.Fatalf("env status missing variables: %s", statusRec.Body.String())
	}

	pullEvent := mustJSON(t, map[string]any{
		"scope":           "dev",
		"env_file_paths":  []any{".env"},
		"config_revision": "rev_00001",
		"variables_count": float64(2),
		"client":          map[string]any{"cli_version": "test-cli"},
	})
	eventRec := httptest.NewRecorder()
	server.ServeHTTP(eventRec, signedRequest(t, signer, http.MethodPost, "/v1/teams/"+teamID+"/events/pull", pullEvent, now, "nonce-pull-event", ""))
	if eventRec.Code != http.StatusOK {
		t.Fatalf("pull event code = %d, body = %s", eventRec.Code, eventRec.Body.String())
	}

	teamRec := httptest.NewRecorder()
	server.ServeHTTP(teamRec, signedRequest(t, signer, http.MethodGet, "/v1/teams/"+teamID+"/status", nil, now, "nonce-team-status", ""))
	if teamRec.Code != http.StatusOK {
		t.Fatalf("team status code = %d, body = %s", teamRec.Code, teamRec.Body.String())
	}
	if !strings.Contains(teamRec.Body.String(), `"last_pulls"`) {
		t.Fatalf("team status missing pull activity: %s", teamRec.Body.String())
	}
}

func TestTeamInvitesJoinerListAndPINRedeem(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := NewServer(storage.NewMemoryStore(), Config{Now: func() time.Time { return now }})
	admin := newTestSigner(t)
	body := setupBody(t, admin, "op_setup_invites", false)

	setupRec := httptest.NewRecorder()
	server.ServeHTTP(setupRec, signedSetupRequest(t, admin, body, now, "nonce-setup-inv"))
	if setupRec.Code != http.StatusCreated {
		t.Fatalf("setup status = %d, body = %s", setupRec.Code, setupRec.Body.String())
	}
	teamID := teamIDFromBody(t, setupRec.Body.Bytes())

	createPayload := mustJSON(t, map[string]any{
		"operation_id": "op_inv_create",
		"label":        "onboarding",
		"requested_scopes": map[string]any{
			"dev": "read",
		},
		"client": map[string]any{"cli_version": "test-cli", "client_kind": "test"},
	})
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, signedRequest(t, admin, http.MethodPost, "/v1/teams/"+teamID+"/invites", createPayload, now, "nonce-inv-create", "op_inv_create"))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create invite status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	pin := envelopeStringField(t, createRec.Body.Bytes(), "pin")
	inviteID := envelopeStringField(t, createRec.Body.Bytes(), "invite_id")

	listRec := httptest.NewRecorder()
	server.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/v1/teams/"+teamID+"/join/invites", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list join invites status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	if !strings.Contains(listRec.Body.String(), inviteID) {
		t.Fatalf("joiner list missing invite id %s: %s", inviteID, listRec.Body.String())
	}

	joiner := newTestSigner(t)
	joinerSSH := openSSHPublicKey(joiner.public, "bob@example.com")
	pinBody := mustJSON(t, map[string]any{
		"operation_id": "op_pin_ok",
		"pin":          pin,
		"handle":       "bob@example.com",
		"joiner": map[string]any{
			"handle":                "bob@example.com",
			"public_key_sha":        joiner.sha,
			"signing_public_key":    joinerSSH,
			"encryption_public_key": "x25519:dGVzdC1lbmNyeXB0aW9uLXB1YmxpYy1rZXk=",
		},
		"requested_role": "developers",
		"requested_scopes": map[string]any{"dev": "read"},
		"client":           map[string]any{"cli_version": "test-cli", "client_kind": "test"},
	})
	pinRec := httptest.NewRecorder()
	server.ServeHTTP(pinRec, signedRequest(t, joiner, http.MethodPost, "/v1/teams/"+teamID+"/join/invites/"+inviteID+"/pin", pinBody, now, "nonce-pin-1", "op_pin_ok"))
	if pinRec.Code != http.StatusOK {
		t.Fatalf("pin redeem status = %d, body = %s", pinRec.Code, pinRec.Body.String())
	}
	if !strings.Contains(pinRec.Body.String(), `"redemption_id": "red_`) {
		t.Fatalf("pin redeem missing redemption id: %s", pinRec.Body.String())
	}

	emptyListRec := httptest.NewRecorder()
	server.ServeHTTP(emptyListRec, httptest.NewRequest(http.MethodGet, "/v1/teams/"+teamID+"/join/invites", nil))
	if emptyListRec.Code != http.StatusOK {
		t.Fatalf("list after redeem status = %d, body = %s", emptyListRec.Code, emptyListRec.Body.String())
	}
	if strings.Contains(emptyListRec.Body.String(), inviteID) {
		t.Fatalf("expected no active invites after redeem: %s", emptyListRec.Body.String())
	}

	pinAgain := mustJSON(t, map[string]any{
		"operation_id": "op_pin_again",
		"pin":          pin,
		"handle":       "bob@example.com",
		"joiner": map[string]any{
			"handle":                "bob@example.com",
			"public_key_sha":        joiner.sha,
			"signing_public_key":    joinerSSH,
			"encryption_public_key": "x25519:dGVzdC1lbmNyeXB0aW9uLXB1YmxpYy1rZXk=",
		},
		"requested_role":   "developers",
		"requested_scopes": map[string]any{"dev": "read"},
		"client":           map[string]any{"cli_version": "test-cli", "client_kind": "test"},
	})
	replayRec := httptest.NewRecorder()
	server.ServeHTTP(replayRec, signedRequest(t, joiner, http.MethodPost, "/v1/teams/"+teamID+"/join/invites/"+inviteID+"/pin", pinAgain, now, "nonce-pin-2", "op_pin_again"))
	if replayRec.Code != http.StatusConflict {
		t.Fatalf("second redeem status = %d, body = %s", replayRec.Code, replayRec.Body.String())
	}
	if !strings.Contains(replayRec.Body.String(), `"code": "invite_not_active"`) {
		t.Fatalf("expected invite_not_active: %s", replayRec.Body.String())
	}
}

func TestTeamInvitesPINLocksAfterThreeFailures(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := NewServer(storage.NewMemoryStore(), Config{Now: func() time.Time { return now }})
	admin := newTestSigner(t)
	body := setupBody(t, admin, "op_setup_invites_lock", false)

	setupRec := httptest.NewRecorder()
	server.ServeHTTP(setupRec, signedSetupRequest(t, admin, body, now, "nonce-setup-lock"))
	if setupRec.Code != http.StatusCreated {
		t.Fatalf("setup status = %d, body = %s", setupRec.Code, setupRec.Body.String())
	}
	teamID := teamIDFromBody(t, setupRec.Body.Bytes())

	createPayload := mustJSON(t, map[string]any{
		"operation_id":     "op_inv_lock",
		"label":            "lock-me",
		"requested_scopes": map[string]any{"dev": "read"},
		"client":           map[string]any{"cli_version": "test-cli", "client_kind": "test"},
	})
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, signedRequest(t, admin, http.MethodPost, "/v1/teams/"+teamID+"/invites", createPayload, now, "nonce-inv-lock", "op_inv_lock"))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create invite status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	pin := envelopeStringField(t, createRec.Body.Bytes(), "pin")
	inviteID := envelopeStringField(t, createRec.Body.Bytes(), "invite_id")

	tryWrong := func(joiner testSigner, opID, nonce string) *httptest.ResponseRecorder {
		t.Helper()
		ssh := openSSHPublicKey(joiner.public, "eve@example.com")
		bad := mustJSON(t, map[string]any{
			"operation_id": opID,
			"pin":          "0000A",
			"handle":       "eve@example.com",
			"joiner": map[string]any{
				"handle":                "eve@example.com",
				"public_key_sha":        joiner.sha,
				"signing_public_key":    ssh,
				"encryption_public_key": "x25519:dGVzdC1lbmNyeXB0aW9uLXB1YmxpYy1rZXk=",
			},
			"requested_role":   "developers",
			"requested_scopes": map[string]any{"dev": "read"},
			"client":           map[string]any{"cli_version": "test-cli", "client_kind": "test"},
		})
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, signedRequest(t, joiner, http.MethodPost, "/v1/teams/"+teamID+"/join/invites/"+inviteID+"/pin", bad, now, nonce, opID))
		return rec
	}

	e1 := newTestSigner(t)
	r1 := tryWrong(e1, "op_bad_1", "nonce-bad-1")
	if r1.Code != http.StatusUnauthorized || !strings.Contains(r1.Body.String(), `"code": "invite_pin_invalid"`) {
		t.Fatalf("first bad pin: status=%d body=%s", r1.Code, r1.Body.String())
	}

	e2 := newTestSigner(t)
	r2 := tryWrong(e2, "op_bad_2", "nonce-bad-2")
	if r2.Code != http.StatusUnauthorized || !strings.Contains(r2.Body.String(), `"code": "invite_pin_invalid"`) {
		t.Fatalf("second bad pin: status=%d body=%s", r2.Code, r2.Body.String())
	}

	e3 := newTestSigner(t)
	r3 := tryWrong(e3, "op_bad_3", "nonce-bad-3")
	if r3.Code != http.StatusForbidden || !strings.Contains(r3.Body.String(), `"code": "invite_locked"`) {
		t.Fatalf("third bad pin: status=%d body=%s", r3.Code, r3.Body.String())
	}

	e4 := newTestSigner(t)
	ssh4 := openSSHPublicKey(e4.public, "frank@example.com")
	good := mustJSON(t, map[string]any{
		"operation_id": "op_good_late",
		"pin":          pin,
		"handle":       "frank@example.com",
		"joiner": map[string]any{
			"handle":                "frank@example.com",
			"public_key_sha":        e4.sha,
			"signing_public_key":    ssh4,
			"encryption_public_key": "x25519:dGVzdC1lbmNyeXB0aW9uLXB1YmxpYy1rZXk=",
		},
		"requested_role":   "developers",
		"requested_scopes": map[string]any{"dev": "read"},
		"client":           map[string]any{"cli_version": "test-cli", "client_kind": "test"},
	})
	r4 := httptest.NewRecorder()
	server.ServeHTTP(r4, signedRequest(t, e4, http.MethodPost, "/v1/teams/"+teamID+"/join/invites/"+inviteID+"/pin", good, now, "nonce-good-late", "op_good_late"))
	if r4.Code != http.StatusConflict || !strings.Contains(r4.Body.String(), `"code": "invite_not_active"`) {
		t.Fatalf("pin after lockout: status=%d body=%s", r4.Code, r4.Body.String())
	}
}

func envelopeStringField(t *testing.T, body []byte, key string) string {
	t.Helper()
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	raw, ok := env.Data[key]
	if !ok {
		t.Fatalf("envelope data missing %q: %s", key, string(body))
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode data.%s: %v body=%s", key, err, string(body))
	}
	return s
}

type testSigner struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
	sha     string
	ssh     string
}

func newTestSigner(t *testing.T) testSigner {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testSigner{
		private: privateKey,
		public:  publicKey,
		sha:     publicKeySHA(publicKey),
		ssh:     openSSHPublicKey(publicKey, "alice@example.com"),
	}
}

func setupBody(t *testing.T, signer testSigner, operationID string, includePlaintext bool) []byte {
	t.Helper()
	configSnapshot := map[string]any{
		"version": float64(1),
		"team": map[string]any{
			"name": "Acme API",
		},
		"scopes": map[string]any{
			"dev": map[string]any{
				"env_files": []any{".env"},
			},
		},
	}
	if includePlaintext {
		configSnapshot["bad"] = map[string]any{"value": "do-not-store-this"}
	}

	payload := map[string]any{
		"operation_id": operationID,
		"team_name":    "Acme API",
		"first_admin": map[string]any{
			"handle":                "alice@example.com",
			"public_key_sha":        signer.sha,
			"signing_public_key":    signer.ssh,
			"encryption_public_key": "x25519:dGVzdC1lbmNyeXB0aW9uLXB1YmxpYy1rZXk=",
		},
		"config_snapshot": configSnapshot,
		"scopes": []any{
			map[string]any{
				"name":      "dev",
				"env_files": []any{".env"},
				"default_role_access": map[string]any{
					"admins":     "write",
					"developers": "read",
				},
			},
		},
		"encrypted_secret_versions": []any{
			map[string]any{
				"scope":             "dev",
				"env_file_path":     ".env",
				"name":              "DATABASE_URL",
				"ciphertext":        "ciphertext",
				"nonce":             "nonce",
				"algorithm":         "test-aead",
				"scope_key_version": float64(1),
			},
		},
		"scope_key_envelopes": []any{
			map[string]any{
				"scope":               "dev",
				"recipient_key_sha":   signer.sha,
				"scope_key_version":   float64(1),
				"encrypted_scope_key": "encrypted-scope-key",
				"algorithm":           "test-envelope",
			},
		},
		"client": map[string]any{
			"cli_version": "test-cli",
			"client_kind": "test",
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func signedSetupRequest(t *testing.T, signer testSigner, body []byte, now time.Time, nonce string) *http.Request {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	operationID := decoded["operation_id"].(string)
	return signedRequest(t, signer, http.MethodPost, "/v1/teams/setup", body, now, nonce, operationID)
}

func signedRequest(t *testing.T, signer testSigner, method string, target string, body []byte, now time.Time, nonce string, operationID string) *http.Request {
	t.Helper()
	if body == nil {
		body = []byte{}
	}
	metadata := signing.Metadata{
		PublicKeySHA: signer.sha,
		Timestamp:    now.Format(time.RFC3339),
		Nonce:        nonce,
		CLIVersion:   "test-cli",
		OperationID:  operationID,
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	canonical := signing.Canonical(method, req.URL.Path, req.URL.RawQuery, body, metadata)
	metadata.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(signer.private, []byte(canonical)))

	req.Header.Set(signing.HeaderPublicKeySHA, metadata.PublicKeySHA)
	req.Header.Set(signing.HeaderTimestamp, metadata.Timestamp)
	req.Header.Set(signing.HeaderNonce, metadata.Nonce)
	req.Header.Set(signing.HeaderCLIVersion, metadata.CLIVersion)
	req.Header.Set(signing.HeaderOperationID, metadata.OperationID)
	req.Header.Set(signing.HeaderSignature, metadata.Signature)
	return req
}

func teamIDFromBody(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Data struct {
			TeamID string `json:"team_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data.TeamID
}

func setupFactsFromBody(t *testing.T, body []byte) (string, string) {
	t.Helper()
	var envelope struct {
		Data struct {
			TeamID     string `json:"team_id"`
			ConfigHash string `json:"config_hash"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data.TeamID, envelope.Data.ConfigHash
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func publicKeySHA(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func openSSHPublicKey(publicKey ed25519.PublicKey, comment string) string {
	var buf []byte
	buf = appendSSHString(buf, []byte("ssh-ed25519"))
	buf = appendSSHString(buf, publicKey)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(buf) + " " + comment
}

func appendSSHString(dst []byte, value []byte) []byte {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	dst = append(dst, size[:]...)
	dst = append(dst, value...)
	return dst
}
