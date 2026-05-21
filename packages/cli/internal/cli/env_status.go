package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/envfile"
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
	"propagate/cli/internal/secretcrypto"
)

type envStatusOptions struct {
	globalOptions
	Scope string
}

type EnvStatusResult struct {
	OK                bool                `json:"ok"`
	Command           string              `json:"command"`
	Status            string              `json:"status"`
	Scope             string              `json:"scope"`
	ProjectConfigPath string              `json:"project_config_path"`
	TeamID            string              `json:"team_id"`
	TeamName          string              `json:"team_name"`
	ConfigRevision    string              `json:"config_revision,omitempty"`
	LocalRevision     string              `json:"local_revision,omitempty"`
	ConfigStale       bool                `json:"config_stale"`
	Identity          *identity.Summary   `json:"identity,omitempty"`
	CanRead           bool                `json:"can_read"`
	Variables         []EnvStatusVariable `json:"variables"`
	VariablesCount    int                 `json:"variables_count"`
	LastUpdated       *EnvStatusUpdate    `json:"last_updated,omitempty"`
	BackendStatus     string              `json:"backend_status"`
	Warnings          []string            `json:"warnings,omitempty"`
	NextSteps         []string            `json:"next_steps,omitempty"`
}

type EnvStatusVariable struct {
	Path              string `json:"path"`
	Name              string `json:"name"`
	CurrentVersionID  string `json:"current_version_id,omitempty"`
	LastUpdatedBy     string `json:"last_updated_by,omitempty"`
	LastUpdatedAt     string `json:"last_updated_at,omitempty"`
	MaskedValue       string `json:"-"`
	Sensitivity       string `json:"sensitivity,omitempty"`
	DeclarationDigest string `json:"declaration_digest,omitempty"`
	LocalState        string `json:"local_state,omitempty"`
}

type EnvStatusUpdate struct {
	At string `json:"at,omitempty"`
	By string `json:"by,omitempty"`
}

func runEnvStatusCommand(args []string, global globalOptions, streams Streams) int {
	opts := envStatusOptions{globalOptions: global, Scope: "dev"}
	fs := flag.NewFlagSet("env status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.StringVar(&opts.Scope, "scope", "dev", "scope to inspect")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printEnvStatusHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid env status flags", err, "Run `propagate env status --help` for usage.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate env status does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}

	result, err := runEnvStatus(opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	renderEnvStatusResult(streams.Out, opts.JSON, opts.NoColor, result)
	return ExitSuccess
}

func runEnvStatus(opts envStatusOptions, streams Streams) (EnvStatusResult, error) {
	scopeName := strings.TrimSpace(opts.Scope)
	if scopeName == "" {
		scopeName = "dev"
	}
	if err := config.ValidateScopeName(scopeName); err != nil {
		return EnvStatusResult{}, commandError(ExitUsageError, "usage_error", "Invalid env status scope", err)
	}
	result := EnvStatusResult{
		OK:            true,
		Command:       "env status",
		Status:        "success",
		Scope:         scopeName,
		BackendStatus: "not_contacted",
	}

	ident, err := identity.Load()
	if err != nil {
		return EnvStatusResult{}, commandError(ExitValidationError, "identity_missing", "Cannot load local Propagate identity for signed env status", err, "Run `propagate init` to create or repair the local identity.")
	}
	summary := ident.Summary()
	result.Identity = &summary

	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return EnvStatusResult{}, commandError(ExitValidationError, "not_git_repo", "Cannot inspect env status outside a Git worktree", err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return EnvStatusResult{}, commandError(ExitValidationError, "config_invalid", "Existing Propagate config needs attention", err, "Rename `propagate.yml` to `propagate.yaml` before running env status again.")
	}
	if !exists {
		return EnvStatusResult{}, commandError(ExitValidationError, "config_missing", "propagate.yaml is required before env status", nil, "Run `propagate init` or pull the repository config first.")
	}
	result.ProjectConfigPath = configPath

	project, err := config.ReadProject(configPath)
	if err != nil {
		return EnvStatusResult{}, commandError(ExitValidationError, "config_invalid", "Cannot read propagate.yaml", err)
	}
	result.TeamID = project.TeamID
	result.TeamName = project.TeamName
	result.LocalRevision = project.CloudRevision
	if findScopeSummary(project.Scopes, scopeName) == nil {
		return EnvStatusResult{}, commandError(ExitValidationError, "scope_not_found", fmt.Sprintf("Scope %q is not configured in propagate.yaml", scopeName), nil, "Run `propagate config pull` if the scope was added in the cloud.")
	}

	apiURL := resolveAPIURL(opts.APIURL, streams.WorkDir)
	if apiURL == "" {
		return EnvStatusResult{}, commandError(ExitCloudUnavailable, "cloud_unavailable", "Propagate API URL is required for env status", nil, "Pass `--api-url` or set PROPAGATE_API_URL.")
	}
	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
	status, err := client.EnvStatus(context.Background(), ident, project.TeamID, scopeName)
	if err != nil {
		return EnvStatusResult{}, mapEnvStatusAPIError(err, scopeName, summary)
	}
	result.BackendStatus = "fetched"
	result.ConfigRevision = status.ConfigRevision
	result.CanRead = status.CanRead
	if status.Scope.Name != "" && status.Scope.Name != scopeName {
		return EnvStatusResult{}, commandError(ExitValidationError, "validation_failed", "Cloud returned env status for a different scope", fmt.Errorf("requested %s, received %s", scopeName, status.Scope.Name))
	}

	cloudConfig, err := client.GetConfig(context.Background(), ident, project.TeamID)
	if err != nil {
		return EnvStatusResult{}, mapEnvStatusAPIError(err, scopeName, summary)
	}
	cloudProject, err := config.ParseSnapshot(cloudConfig.ConfigSnapshot, cloudConfig.ConfigRevision)
	if err != nil {
		return EnvStatusResult{}, commandError(ExitValidationError, "config_invalid", "Cannot read latest cloud config snapshot for env status", err)
	}
	cloudScope := findScopeSummary(cloudProject.Scopes, scopeName)
	if cloudScope == nil {
		return EnvStatusResult{}, commandError(ExitValidationError, "scope_not_found", fmt.Sprintf("Scope %q is not configured in the latest cloud config", scopeName), nil, "Run `propagate config pull` if the scope was added in the cloud.")
	}
	if cloudConfig.ConfigRevision != "" {
		result.ConfigRevision = cloudConfig.ConfigRevision
	}
	result.ConfigStale = result.LocalRevision != "" && result.ConfigRevision != "" && result.LocalRevision != result.ConfigRevision

	maskedByKey, scopeKey, scopeKeyVersion, err := maskedEnvValues(project.TeamID, scopeName, ident, status)
	if err != nil {
		return EnvStatusResult{}, commandError(ExitPermissionDenied, "decrypt_failed", "Cannot decrypt cloud env values for masked status output", err, "No values were shown.")
	}
	localValues, localWarnings := localDeclarationValues(worktree.Root, project.TeamID, scopeName, scopeKey, scopeKeyVersion, cloudScope.EnvFiles)
	result.Warnings = append(result.Warnings, localWarnings...)
	result.Variables = envStatusVariables(status.Variables, status.EncryptedValues, maskedByKey, cloudScope.Variables, localValues)
	result.VariablesCount = len(result.Variables)
	result.LastUpdated = lastEnvStatusUpdate(result.Variables)
	if result.ConfigStale {
		result.NextSteps = append(result.NextSteps, "Run `propagate config pull` to update propagate.yaml to the latest cloud declarations.")
	}
	if envStatusHasLocalDrift(result.Variables) {
		result.NextSteps = append(result.NextSteps, fmt.Sprintf("Run `propagate env pull --scope %s` to update local env values that differ from the latest declarations.", scopeName))
	}
	if result.VariablesCount == 0 {
		result.Status = "no_change"
		result.NextSteps = append(result.NextSteps, "No variables are stored for this scope yet.")
	}
	return result, nil
}

func maskedEnvValues(teamID string, scopeName string, ident identity.Identity, status apiclient.EnvStatusData) (map[envVarKey]string, []byte, int, error) {
	out := map[envVarKey]string{}
	if len(status.EncryptedValues) == 0 {
		return out, nil, 0, nil
	}
	if status.ScopeKeyEnvelope == nil {
		return nil, nil, 0, errors.New("cloud did not return a scope key envelope")
	}
	if status.ScopeKeyEnvelope.RecipientKeySHA != "" && status.ScopeKeyEnvelope.RecipientKeySHA != ident.PublicKeySHA {
		return nil, nil, 0, errors.New("scope key envelope does not belong to the current identity")
	}
	scopeKey, err := secretcrypto.DecryptScopeKey(
		ident.EncryptionPrivateKey,
		status.ScopeKeyEnvelope.EncryptedScopeKey,
		status.ScopeKeyEnvelope.Algorithm,
		scopeName,
		ident.PublicKeySHA,
		status.ScopeKeyEnvelope.ScopeKeyVersion,
	)
	if err != nil {
		return nil, nil, 0, err
	}
	for _, record := range status.EncryptedValues {
		value, err := secretcrypto.DecryptValue(
			scopeKey,
			teamID,
			scopeName,
			record.EnvFilePath,
			record.Name,
			record.ScopeKeyVersion,
			record.Ciphertext,
			record.Nonce,
			record.Algorithm,
		)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("%s in %s: %w", record.Name, record.EnvFilePath, err)
		}
		out[envVarKey{Path: record.EnvFilePath, Name: record.Name}] = envfile.Mask(value)
	}
	return out, scopeKey, status.ScopeKeyEnvelope.ScopeKeyVersion, nil
}

type localDeclarationValue struct {
	Digest string
	Value  string
}

func envStatusVariables(metadata []apiclient.VariableMetadata, encrypted []apiclient.SecretVersionRecord, masked map[envVarKey]string, declarations []config.VariableDeclaration, localValues map[envVarKey]localDeclarationValue) []EnvStatusVariable {
	byKey := map[envVarKey]EnvStatusVariable{}
	for _, declaration := range declarations {
		key := envVarKey{Path: declaration.EnvFilePath, Name: declaration.Name}
		localState := "unknown"
		if localValue, ok := localValues[key]; ok {
			if declaration.Digest != "" && localValue.Digest == declaration.Digest {
				localState = "equal"
			} else if declaration.Digest != "" {
				localState = "different"
			} else if declaration.Literal != "" && localValue.Value == declaration.Literal {
				localState = "equal"
			} else if declaration.Literal != "" {
				localState = "different"
			}
		} else {
			localState = "missing"
		}
		byKey[key] = EnvStatusVariable{
			Path:              declaration.EnvFilePath,
			Name:              declaration.Name,
			MaskedValue:       masked[key],
			Sensitivity:       declaration.Sensitivity,
			DeclarationDigest: declaration.Digest,
			LocalState:        localState,
		}
	}
	for _, item := range metadata {
		key := envVarKey{Path: item.EnvFilePath, Name: item.Name}
		existing := byKey[key]
		existing.Path = item.EnvFilePath
		existing.Name = item.Name
		existing.CurrentVersionID = item.CurrentVersionID
		existing.LastUpdatedBy = item.LastUpdatedBy
		existing.LastUpdatedAt = item.LastUpdatedAt
		existing.MaskedValue = masked[key]
		if existing.LocalState == "" {
			existing.LocalState = "undeclared"
		}
		byKey[key] = existing
	}
	for _, item := range encrypted {
		key := envVarKey{Path: item.EnvFilePath, Name: item.Name}
		if _, exists := byKey[key]; exists {
			continue
		}
		byKey[key] = EnvStatusVariable{
			Path:             item.EnvFilePath,
			Name:             item.Name,
			CurrentVersionID: item.CurrentVersionID,
			MaskedValue:      masked[key],
			LocalState:       "undeclared",
		}
	}
	keys := make([]envVarKey, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sortEnvKeys(keys)
	out := make([]EnvStatusVariable, 0, len(keys))
	for _, key := range keys {
		out = append(out, byKey[key])
	}
	return out
}

func localDeclarationValues(root string, teamID string, scopeName string, scopeKey []byte, scopeKeyVersion int, envFiles []string) (map[envVarKey]localDeclarationValue, []string) {
	out := map[envVarKey]localDeclarationValue{}
	if len(scopeKey) == 0 || scopeKeyVersion <= 0 {
		return out, nil
	}
	var warnings []string
	for _, rel := range envFiles {
		absPath, err := repoFilePath(root, rel)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		assignments, err := envfile.ParseAssignments(absPath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("cannot read %s for local digest comparison: %v", rel, err))
			continue
		}
		for _, name := range sortedAssignmentNames(assignments.Values) {
			key := envVarKey{Path: rel, Name: name}
			out[key] = localDeclarationValue{
				Digest: secretcrypto.FingerprintValue(scopeKey, teamID, scopeName, rel, name, scopeKeyVersion, assignments.Values[name]),
				Value:  assignments.Values[name],
			}
		}
	}
	return out, warnings
}

func envStatusHasLocalDrift(vars []EnvStatusVariable) bool {
	for _, item := range vars {
		switch item.LocalState {
		case "different", "missing", "undeclared":
			return true
		}
	}
	return false
}

func lastEnvStatusUpdate(vars []EnvStatusVariable) *EnvStatusUpdate {
	var latest *EnvStatusUpdate
	for _, item := range vars {
		if item.LastUpdatedAt == "" {
			continue
		}
		if latest == nil || item.LastUpdatedAt > latest.At {
			latest = &EnvStatusUpdate{At: item.LastUpdatedAt, By: item.LastUpdatedBy}
		}
	}
	return latest
}

func mapEnvStatusAPIError(err error, scope string, ident identity.Summary) error {
	var apiErr *apiclient.APIError
	if !errors.As(err, &apiErr) {
		return commandError(ExitCloudUnavailable, "cloud_unavailable", "Cannot fetch env status", err)
	}
	switch apiErr.Code {
	case "permission_denied":
		return commandError(
			ExitPermissionDenied,
			apiErr.Code,
			fmt.Sprintf("Cannot inspect env status for scope %q with identity %s", scope, ident.PublicKeySHA),
			apiErr,
			"Ask a Propagate management member to grant read access for this scope.",
		)
	case "team_not_found", "scope_not_found":
		return commandError(ExitValidationError, apiErr.Code, "The requested team or scope was not found in the cloud", apiErr, "Run `propagate config pull` if the local config is stale.")
	case "validation_failed", "usage_error":
		return commandError(ExitValidationError, apiErr.Code, "Env status request was rejected by the cloud", apiErr)
	default:
		code := ExitCloudUnavailable
		if apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 && !apiErr.Retryable {
			code = ExitValidationError
		}
		return commandError(code, apiErr.Code, "Cannot fetch env status", apiErr)
	}
}

func renderEnvStatusResult(w io.Writer, jsonOutput bool, noColor bool, result EnvStatusResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	style := newOutputStyle(noColor)
	renderCommandTitle(w, style, "Env status", false)
	switch result.Status {
	case "no_change":
		renderNote(w, style, "No env values stored for this scope.")
	default:
		renderOK(w, style, "Env status complete.")
	}
	fmt.Fprintln(w)
	if result.TeamName != "" {
		fmt.Fprintf(w, "Team: %s (%s)\n", result.TeamName, result.TeamID)
	}
	fmt.Fprintf(w, "Scope: %s\n", result.Scope)
	if result.Identity != nil {
		fmt.Fprintf(w, "Identity: %s (%s)\n", result.Identity.Handle, result.Identity.PublicKeySHA)
	}
	if result.ConfigRevision != "" {
		fmt.Fprintf(w, "Config revision: %s\n", result.ConfigRevision)
	}
	if result.LocalRevision != "" && result.LocalRevision != result.ConfigRevision {
		fmt.Fprintf(w, "Local config revision: %s\n", result.LocalRevision)
	}
	fmt.Fprintf(w, "Can read: %t\n", result.CanRead)
	if len(result.Variables) > 0 {
		fmt.Fprintln(w, style.bold("Variables:"))
		nameValueWidth := 0
		nameValues := make([]string, len(result.Variables))
		for i, item := range result.Variables {
			nv := item.Name
			if item.MaskedValue != "" {
				nv += "=" + item.MaskedValue
			}
			nameValues[i] = nv
			if len(nv) > nameValueWidth {
				nameValueWidth = len(nv)
			}
		}
		for i, item := range result.Variables {
			line := "  - " + nameValues[i]
			if item.Path != "" {
				pad := nameValueWidth - len(nameValues[i])
				if pad < 0 {
					pad = 0
				}
				line += strings.Repeat(" ", pad) + "  " + item.Path
			}
			if marker := envStatusLocalStateMarker(item.LocalState); marker != "" {
				line += " " + marker
			}
			fmt.Fprintln(w, line)
		}
	}
	if result.LastUpdated != nil && result.LastUpdated.At != "" {
		fmt.Fprintf(w, "Last updated: %s", formatStatusTimestamp(result.LastUpdated.At))
		if result.LastUpdated.By != "" {
			fmt.Fprintf(w, " by %s", result.LastUpdated.By)
		}
		fmt.Fprintln(w)
	}
	if result.BackendStatus != "" && result.BackendStatus != "fetched" {
		fmt.Fprintf(w, "Backend: %s\n", result.BackendStatus)
	}
	renderWarnings(w, style, result.Warnings)
	renderNextSteps(w, style, result.NextSteps)
}

func envStatusLocalStateMarker(state string) string {
	switch state {
	case "different":
		return "[drift]"
	case "missing":
		return "[missing locally]"
	case "undeclared":
		return "[undeclared]"
	default:
		return ""
	}
}

func printEnvStatusHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate env status [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --scope VALUE       scope to inspect (default dev)")
	fmt.Fprintln(w, "  --api-url VALUE     override Propagate API URL")
	fmt.Fprintln(w, "  --json              render machine-readable JSON without masked values")
	fmt.Fprintln(w, "  --non-interactive   fail instead of prompting")
}
