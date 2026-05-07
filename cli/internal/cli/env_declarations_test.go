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
