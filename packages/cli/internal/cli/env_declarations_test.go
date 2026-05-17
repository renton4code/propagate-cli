package cli

import (
	"strings"
	"testing"

	"propagate/cli/internal/config"
)

func TestVariableDeclarationsUsePrefixedDigestsAndNonSensitiveLiterals(t *testing.T) {
	scopeKey := []byte("0123456789abcdef0123456789abcdef")
	project := config.ParsedProject{
		TeamID:        "team_test",
		CloudRevision: "rev_00001",
		Scopes: []config.ScopeSummary{{
			Name:     "dev",
			EnvFiles: []string{".env"},
			Variables: []config.VariableDeclaration{{
				Name:        "PUBLIC_URL",
				EnvFilePath: ".env",
				Sensitivity: config.SensitivityNonSensitive,
			}, {
				Name:        "PUBLIC_LONG",
				EnvFilePath: ".env",
				Sensitivity: config.SensitivityNonSensitive,
			}},
		}},
	}
	values := map[envVarKey]string{
		{Path: ".env", Name: "SECRET"}:      "super-secret",
		{Path: ".env", Name: "PUBLIC_URL"}:  "https://example.com",
		{Path: ".env", Name: "PUBLIC_LONG"}: strings.Repeat("a", 90) + "zzz",
	}

	project = updateScopeDeclarationsFromLocalValues(project, "dev", scopeKey, 1, values)
	decls := declarationMap(project.Scopes[0].Variables)

	secret := decls[envVarKey{Path: ".env", Name: "SECRET"}]
	if secret.Sensitivity != config.SensitivitySensitive || !strings.HasPrefix(secret.Digest, "hmac-sha-256:v1:") || secret.Literal != "" || secret.Preview != "" {
		t.Fatalf("unexpected sensitive declaration: %+v", secret)
	}

	publicURL := decls[envVarKey{Path: ".env", Name: "PUBLIC_URL"}]
	if publicURL.Literal != "https://example.com" || publicURL.Digest != "" || publicURL.Preview != "" {
		t.Fatalf("unexpected short non-sensitive declaration: %+v", publicURL)
	}

	publicLong := decls[envVarKey{Path: ".env", Name: "PUBLIC_LONG"}]
	if publicLong.Literal != "" || publicLong.Preview == "" || !strings.Contains(publicLong.Preview, "...") || !strings.HasPrefix(publicLong.Digest, "hmac-sha-256:v1:") {
		t.Fatalf("unexpected long non-sensitive declaration: %+v", publicLong)
	}
}

func TestEnvPushDeclarationUpdateHonorsSelectedDiff(t *testing.T) {
	scopeKey := []byte("0123456789abcdef0123456789abcdef")
	project := config.ParsedProject{
		TeamID:        "team_test",
		CloudRevision: "rev_00001",
		Scopes: []config.ScopeSummary{{
			Name:     "dev",
			EnvFiles: []string{".env"},
			Variables: []config.VariableDeclaration{{
				Name:        "CHANGED",
				EnvFilePath: ".env",
				Sensitivity: config.SensitivitySensitive,
				Digest:      "hmac-sha-256:v1:old-changed",
			}, {
				Name:        "REMOVED",
				EnvFilePath: ".env",
				Sensitivity: config.SensitivitySensitive,
				Digest:      "hmac-sha-256:v1:old-removed",
			}, {
				Name:        "SKIPPED_CHANGED",
				EnvFilePath: ".env",
				Sensitivity: config.SensitivitySensitive,
				Digest:      "hmac-sha-256:v1:old-skipped-changed",
			}, {
				Name:        "SKIPPED_REMOVED",
				EnvFilePath: ".env",
				Sensitivity: config.SensitivitySensitive,
				Digest:      "hmac-sha-256:v1:old-skipped-removed",
			}},
		}},
	}
	values := map[envVarKey]string{
		{Path: ".env", Name: "ADDED"}:           "added-value",
		{Path: ".env", Name: "CHANGED"}:         "changed-value",
		{Path: ".env", Name: "SKIPPED_CHANGED"}: "skipped-changed-value",
		{Path: ".env", Name: "UNCHANGED"}:       "unchanged-value",
	}
	diff := envPushDiff{
		Added:     []envVarKey{{Path: ".env", Name: "ADDED"}},
		Changed:   []envVarKey{{Path: ".env", Name: "CHANGED"}},
		Removed:   []envVarKey{{Path: ".env", Name: "REMOVED"}},
		Unchanged: []envVarKey{{Path: ".env", Name: "UNCHANGED"}},
	}

	project = updateScopeDeclarationsFromEnvPushDiff(project, "dev", scopeKey, 1, values, diff)
	decls := declarationMap(project.Scopes[0].Variables)

	if _, ok := decls[envVarKey{Path: ".env", Name: "ADDED"}]; !ok {
		t.Fatalf("selected added variable was not declared: %+v", decls)
	}
	changed := decls[envVarKey{Path: ".env", Name: "CHANGED"}]
	if changed.Digest == "" || changed.Digest == "hmac-sha-256:v1:old-changed" {
		t.Fatalf("selected changed variable was not refreshed: %+v", changed)
	}
	if _, ok := decls[envVarKey{Path: ".env", Name: "REMOVED"}]; ok {
		t.Fatalf("selected removed variable was still declared: %+v", decls)
	}
	skippedChanged := decls[envVarKey{Path: ".env", Name: "SKIPPED_CHANGED"}]
	if skippedChanged.Digest != "hmac-sha-256:v1:old-skipped-changed" {
		t.Fatalf("skipped changed variable was modified: %+v", skippedChanged)
	}
	skippedRemoved := decls[envVarKey{Path: ".env", Name: "SKIPPED_REMOVED"}]
	if skippedRemoved.Digest != "hmac-sha-256:v1:old-skipped-removed" {
		t.Fatalf("skipped removed variable was modified: %+v", skippedRemoved)
	}
	if _, ok := decls[envVarKey{Path: ".env", Name: "UNCHANGED"}]; !ok {
		t.Fatalf("unchanged variable metadata was not refreshed: %+v", decls)
	}
}
