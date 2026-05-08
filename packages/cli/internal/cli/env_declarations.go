package cli

import (
	"strings"
	"unicode/utf8"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/secretcrypto"
)

const shortLiteralMaxRunes = 80

func apiVariableDeclarations(values []config.VariableDeclaration) []apiclient.VariableDeclaration {
	out := make([]apiclient.VariableDeclaration, 0, len(values))
	for _, value := range values {
		out = append(out, apiclient.VariableDeclaration{
			Name:        value.Name,
			EnvFilePath: value.EnvFilePath,
			Sensitivity: value.Sensitivity,
			Digest:      value.Digest,
			Literal:     value.Literal,
			Preview:     value.Preview,
		})
	}
	return out
}

func updateScopeDeclarationsFromLocalValues(project config.ParsedProject, scopeName string, scopeKey []byte, scopeKeyVersion int, values map[envVarKey]string) config.ParsedProject {
	scopeIdx := -1
	for idx := range project.Scopes {
		if project.Scopes[idx].Name == scopeName {
			scopeIdx = idx
			break
		}
	}
	if scopeIdx < 0 {
		return project
	}

	existing := declarationMap(project.Scopes[scopeIdx].Variables)
	var next []config.VariableDeclaration
	for _, key := range sortedLocalKeys(values) {
		sensitivity := existing[key].Sensitivity
		if sensitivity == "" {
			sensitivity = config.SensitivitySensitive
		}
		next = append(next, declarationForValue(project.TeamID, scopeName, scopeKey, scopeKeyVersion, key, values[key], sensitivity))
	}
	project.Scopes[scopeIdx].Variables = next
	return project
}

func updateScopeDeclarationForValue(project config.ParsedProject, scopeName string, scopeKey []byte, scopeKeyVersion int, key envVarKey, value string) config.ParsedProject {
	scopeIdx := -1
	for idx := range project.Scopes {
		if project.Scopes[idx].Name == scopeName {
			scopeIdx = idx
			break
		}
	}
	if scopeIdx < 0 {
		return project
	}

	existing := declarationMap(project.Scopes[scopeIdx].Variables)
	sensitivity := existing[key].Sensitivity
	if sensitivity == "" {
		sensitivity = config.SensitivitySensitive
	}
	existing[key] = declarationForValue(project.TeamID, scopeName, scopeKey, scopeKeyVersion, key, value, sensitivity)

	var next []config.VariableDeclaration
	for _, item := range project.Scopes[scopeIdx].Variables {
		itemKey := envVarKey{Path: item.EnvFilePath, Name: item.Name}
		if itemKey == key {
			continue
		}
		next = append(next, item)
	}
	next = append(next, existing[key])
	project.Scopes[scopeIdx].Variables = next
	return project
}

func declarationForValue(teamID string, scopeName string, scopeKey []byte, scopeKeyVersion int, key envVarKey, value string, sensitivity string) config.VariableDeclaration {
	digest := secretcrypto.FingerprintValue(scopeKey, teamID, scopeName, key.Path, key.Name, scopeKeyVersion, value)
	declaration := config.VariableDeclaration{
		Name:        key.Name,
		EnvFilePath: key.Path,
		Sensitivity: sensitivity,
	}
	if sensitivity == config.SensitivityNonSensitive {
		if isShortOneLine(value) {
			declaration.Literal = value
		} else {
			declaration.Preview = truncatePreview(value)
			declaration.Digest = digest
		}
		return declaration
	}
	declaration.Sensitivity = config.SensitivitySensitive
	declaration.Digest = digest
	return declaration
}

func declarationMap(values []config.VariableDeclaration) map[envVarKey]config.VariableDeclaration {
	out := map[envVarKey]config.VariableDeclaration{}
	for _, value := range values {
		out[envVarKey{Path: value.EnvFilePath, Name: value.Name}] = value
	}
	return out
}

func isShortOneLine(value string) bool {
	if strings.ContainsAny(value, "\r\n") {
		return false
	}
	return utf8.RuneCountInString(value) <= shortLiteralMaxRunes
}

func truncatePreview(value string) string {
	runes := []rune(value)
	if len(runes) <= 12 {
		return value
	}
	return string(runes[:3]) + "..." + string(runes[len(runes)-3:])
}
