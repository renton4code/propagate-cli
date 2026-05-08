package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"propagate/cli/internal/agents"
	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/atomicfile"
	"propagate/cli/internal/config"
	"propagate/cli/internal/envfile"
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
	"propagate/cli/internal/secretcrypto"
)

type initOptions struct {
	globalOptions
	Handle            string
	TeamName          string
	Yes               bool
	DryRun            bool
	AgentGuidance     bool
	SkipAgentGuidance bool
}

type InitResult struct {
	OK                       bool              `json:"ok"`
	Command                  string            `json:"command"`
	Status                   string            `json:"status"`
	DryRun                   bool              `json:"dry_run"`
	IdentityCreated          bool              `json:"identity_created"`
	IdentityPath             string            `json:"identity_path,omitempty"`
	IdentityDir              string            `json:"identity_dir,omitempty"`
	Identity                 *identity.Summary `json:"identity,omitempty"`
	ProjectCreated           bool              `json:"project_created"`
	ProjectAlreadyConfigured bool              `json:"project_already_configured"`
	ProjectConfigPath        string            `json:"project_config_path,omitempty"`
	ScopesCreated            []string          `json:"scopes_created,omitempty"`
	EnvFilesMapped           []EnvFileSummary  `json:"env_files_mapped,omitempty"`
	VariablesDiscoveredCount int               `json:"variables_discovered_count"`
	VariablesUploadedCount   int               `json:"variables_uploaded_count"`
	BackendStatus            string            `json:"backend_status"`
	AgentGuidance            agents.Result     `json:"agent_guidance"`
	Warnings                 []string          `json:"warnings,omitempty"`
	NextSteps                []string          `json:"next_steps,omitempty"`
}

type EnvFileSummary struct {
	Path           string `json:"path"`
	Scope          string `json:"scope"`
	Tracked        bool   `json:"tracked"`
	ParentTracked  bool   `json:"parent_tracked"`
	VariablesCount int    `json:"variables_count"`
}

func runInitCommand(args []string, global globalOptions, streams Streams) int {
	opts := initOptions{globalOptions: global}
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.StringVar(&opts.Handle, "handle", "", "handle to store with a new local identity")
	fs.StringVar(&opts.TeamName, "team-name", "", "team name for new project setup")
	fs.BoolVar(&opts.Yes, "yes", false, "accept safe default setup decisions")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "show what would happen without writing files")
	fs.BoolVar(&opts.AgentGuidance, "agent-guidance", false, "create or update generic AGENTS.md Propagate guidance")
	fs.BoolVar(&opts.SkipAgentGuidance, "skip-agent-guidance", false, "skip agent guidance setup")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printInitHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid init flags", err, "Run `propagate init --help` for usage.")
		return renderError(streams.Err, opts.JSON, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate init does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, cmdErr)
	}
	if opts.AgentGuidance && opts.SkipAgentGuidance {
		cmdErr := commandError(ExitUsageError, "usage_error", "--agent-guidance and --skip-agent-guidance cannot be used together", nil)
		return renderError(streams.Err, opts.JSON, cmdErr)
	}

	result, err := runInit(opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, err)
	}
	renderInitResult(streams.Out, opts.JSON, result)
	return ExitSuccess
}

func runInit(opts initOptions, streams Streams) (InitResult, error) {
	reader := bufio.NewReader(streams.In)
	result := InitResult{
		OK:                     true,
		Command:                "init",
		Status:                 "success",
		DryRun:                 opts.DryRun,
		VariablesUploadedCount: 0,
		BackendStatus:          "not_configured_local_only",
		AgentGuidance:          agents.Result{Status: "skipped"},
	}

	identityDir, err := identity.Directory()
	if err != nil {
		return InitResult{}, commandError(ExitValidationError, "identity_missing", "Cannot locate local home directory for Propagate identity", err)
	}
	identityPath := filepath.Join(identityDir, identity.IdentityFile)
	identityExists, err := atomicfile.Exists(identityPath)
	if err != nil {
		return InitResult{}, commandError(ExitValidationError, "identity_missing", "Cannot inspect local Propagate identity", err)
	}

	if !identityExists && strings.TrimSpace(opts.Handle) == "" {
		handle, err := promptRequired(reader, streams.Out, opts.NonInteractive, "Handle (name or email)")
		if err != nil {
			return InitResult{}, err
		}
		opts.Handle = handle
	}

	var ident identity.Identity
	if opts.DryRun {
		result.IdentityDir = identityDir
		result.IdentityPath = identityPath
		result.IdentityCreated = !identityExists
		if identityExists {
			loaded, err := identity.Load()
			if err != nil {
				return InitResult{}, commandError(ExitValidationError, "identity_corrupt", "Existing Propagate identity could not be loaded", err)
			}
			summary := loaded.Summary()
			result.Identity = &summary
			ident = loaded
		}
	} else {
		ensured, err := identity.Ensure(opts.Handle)
		if err != nil {
			return InitResult{}, commandError(ExitValidationError, "identity_missing", "Cannot create or load local Propagate identity", err)
		}
		ident = ensured.Identity
		summary := ident.Summary()
		result.Identity = &summary
		result.IdentityCreated = ensured.Created
		result.IdentityDir = ensured.Dir
		result.IdentityPath = ensured.Path
	}

	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return InitResult{}, commandError(ExitValidationError, "not_git_repo", "Cannot initialize project metadata outside a Git worktree", err)
	}

	existingConfig, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return InitResult{}, commandError(ExitValidationError, "config_invalid", "Existing Propagate config needs attention", err, "Rename `propagate.yml` to `propagate.yaml` before running init again.")
	}
	if exists {
		result.ProjectAlreadyConfigured = true
		result.ProjectConfigPath = existingConfig
		result.NextSteps = []string{
			"Run `propagate team join` if you need to request access for this identity.",
			"Commit only reviewed metadata changes; never add env values to propagate.yaml.",
		}
		guidance, warnings := maybeApplyAgentGuidance(opts, streams, reader, worktree.Root)
		result.AgentGuidance = guidance
		result.Warnings = append(result.Warnings, warnings...)
		return result, nil
	}

	if strings.TrimSpace(opts.TeamName) == "" {
		teamName, err := promptRequired(reader, streams.Out, opts.NonInteractive, "Team name")
		if err != nil {
			return InitResult{}, err
		}
		opts.TeamName = teamName
	}

	candidates, warnings, err := envfile.Scan(worktree)
	if err != nil {
		return InitResult{}, commandError(ExitValidationError, "env_parse_error", "Cannot scan env files safely", err)
	}
	result.Warnings = append(result.Warnings, warnings...)
	result.EnvFilesMapped = summarizeEnvFiles(candidates)
	for _, c := range candidates {
		result.VariablesDiscoveredCount += c.VariableCount
	}

	if opts.DryRun {
		result.ProjectCreated = true
		result.ProjectConfigPath = config.Path(worktree.Root)
		result.ScopesCreated = scopesFromCandidates(candidates)
		result.NextSteps = []string{
			"Re-run without `--dry-run` to create local identity and propagate.yaml.",
			"Pass `--api-url` or set PROPAGATE_API_URL on the real run to create the cloud team and upload encrypted values.",
		}
		return result, nil
	}

	if opts.NonInteractive && !opts.Yes {
		return InitResult{}, commandError(
			ExitConfirmationRequired,
			"confirmation_required",
			"Non-interactive project setup requires --yes",
			nil,
			"Re-run with `--yes` after reviewing the env file mappings.",
		)
	}
	if !opts.NonInteractive && !opts.Yes {
		ok, err := promptConfirm(reader, streams.Out, fmt.Sprintf("Create %s with %d env file mapping(s)?", config.FileName, len(candidates)), true)
		if err != nil {
			return InitResult{}, err
		}
		if !ok {
			return InitResult{}, commandError(ExitUserCanceled, "user_canceled", "Project setup was canceled before writing propagate.yaml", nil)
		}
	}

	project, err := config.NewProject(opts.TeamName, ident.Summary(), candidates)
	if err != nil {
		return InitResult{}, commandError(ExitValidationError, "config_invalid", "Cannot build initial project config", err)
	}
	if apiURL := resolveAPIURL(opts.APIURL); apiURL != "" {
		setupRequest, updatedProject, err := buildTeamSetupRequest(worktree.Root, project, candidates)
		if err != nil {
			return InitResult{}, commandError(ExitValidationError, "env_parse_error", "Cannot prepare encrypted initial env upload", err)
		}
		project = updatedProject
		client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
		setupResult, err := client.SetupTeam(context.Background(), ident, setupRequest)
		if err != nil {
			return InitResult{}, mapInitAPIError(err)
		}
		project.TeamID = setupResult.TeamID
		project.CloudRevision = setupResult.ConfigRevision
		project.SyncStatus = "synced"
		result.VariablesUploadedCount = setupResult.EncryptedVariablesCount
		result.BackendStatus = "created"
		if len(setupResult.ScopesCreated) > 0 {
			result.ScopesCreated = setupResult.ScopesCreated
		}
	}
	if err := config.Write(worktree.Root, project); err != nil {
		return InitResult{}, commandError(ExitPartialLocalFailure, "partial_local_failure", "Could not write propagate.yaml", err)
	}
	result.ProjectCreated = true
	result.ProjectConfigPath = config.Path(worktree.Root)
	if len(result.ScopesCreated) == 0 {
		for _, scope := range project.Scopes {
			result.ScopesCreated = append(result.ScopesCreated, scope.Name)
		}
	}
	result.NextSteps = []string{
		"Review propagate.yaml and confirm it contains metadata only.",
		"Run `git add propagate.yaml` and commit the setup when ready.",
	}
	if result.BackendStatus == "not_configured_local_only" {
		result.NextSteps = append(result.NextSteps, "Set PROPAGATE_API_URL or pass `--api-url` to create the cloud team and upload encrypted values.")
	}

	guidance, guidanceWarnings := maybeApplyAgentGuidance(opts, streams, reader, worktree.Root)
	result.AgentGuidance = guidance
	result.Warnings = append(result.Warnings, guidanceWarnings...)
	return result, nil
}

func buildTeamSetupRequest(root string, project config.Project, candidates []envfile.Candidate) (apiclient.TeamSetupRequest, config.Project, error) {
	operationID, err := operationID("team_setup")
	if err != nil {
		return apiclient.TeamSetupRequest{}, project, err
	}

	scopeKeys := map[string][]byte{}
	for _, scope := range project.Scopes {
		scopeKey, err := secretcrypto.GenerateScopeKey()
		if err != nil {
			return apiclient.TeamSetupRequest{}, project, err
		}
		scopeKeys[scope.Name] = scopeKey
	}

	var envelopes []apiclient.ScopeKeyEnvelope
	for _, scope := range project.Scopes {
		encryptedScopeKey, err := secretcrypto.EncryptScopeKey(scopeKeys[scope.Name], project.Admin.EncryptionPublicKey, scope.Name, project.Admin.PublicKeySHA, 1)
		if err != nil {
			return apiclient.TeamSetupRequest{}, project, err
		}
		envelopes = append(envelopes, apiclient.ScopeKeyEnvelope{
			Scope:             scope.Name,
			RecipientKeySHA:   project.Admin.PublicKeySHA,
			ScopeKeyVersion:   1,
			EncryptedScopeKey: encryptedScopeKey,
			Algorithm:         secretcrypto.EnvelopeAlgorithm,
		})
	}

	var versions []apiclient.EncryptedSecretVersion
	declarationsByScope := map[string][]config.VariableDeclaration{}
	for _, candidate := range candidates {
		scopeName := candidate.Scope
		if scopeName == "" {
			scopeName = "dev"
		}
		scopeKey := scopeKeys[scopeName]
		if len(scopeKey) == 0 {
			return apiclient.TeamSetupRequest{}, project, fmt.Errorf("candidate %s references unknown scope %s", candidate.Path, scopeName)
		}
		absPath, err := repoFilePath(root, candidate.Path)
		if err != nil {
			return apiclient.TeamSetupRequest{}, project, err
		}
		assignments, err := envfile.ParseAssignments(absPath)
		if err != nil {
			return apiclient.TeamSetupRequest{}, project, fmt.Errorf("parse %s: %w", candidate.Path, err)
		}
		for _, name := range sortedAssignmentNames(assignments.Values) {
			ciphertext, nonce, err := secretcrypto.EncryptValue(scopeKey, project.TeamID, scopeName, candidate.Path, name, 1, assignments.Values[name])
			if err != nil {
				return apiclient.TeamSetupRequest{}, project, err
			}
			digest := secretcrypto.FingerprintValue(scopeKey, project.TeamID, scopeName, candidate.Path, name, 1, assignments.Values[name])
			declarationsByScope[scopeName] = append(declarationsByScope[scopeName], config.VariableDeclaration{
				Name:        name,
				EnvFilePath: candidate.Path,
				Sensitivity: config.SensitivitySensitive,
				Digest:      digest,
			})
			versions = append(versions, apiclient.EncryptedSecretVersion{
				Scope:           scopeName,
				EnvFilePath:     candidate.Path,
				Name:            name,
				Ciphertext:      ciphertext,
				Nonce:           nonce,
				Algorithm:       secretcrypto.ValueAlgorithm,
				ScopeKeyVersion: 1,
			})
		}
	}
	for idx := range project.Scopes {
		project.Scopes[idx].Variables = declarationsByScope[project.Scopes[idx].Name]
	}
	parsed := parsedProjectFromProject(project)
	snapshot, err := config.SnapshotJSON(parsed)
	if err != nil {
		return apiclient.TeamSetupRequest{}, project, err
	}

	return apiclient.TeamSetupRequest{
		OperationID:             operationID,
		TeamName:                project.TeamName,
		FirstAdmin:              publicIdentity(project.Admin),
		ConfigSnapshot:          json.RawMessage(snapshot),
		Scopes:                  setupScopesFromProject(project),
		EncryptedSecretVersions: versions,
		ScopeKeyEnvelopes:       envelopes,
		Client:                  apiclient.ClientMetadata{CLIVersion: Version, ClientKind: "cli"},
	}, project, nil
}

func parsedProjectFromProject(project config.Project) config.ParsedProject {
	parsed := config.ParsedProject{
		Version:       project.Version,
		TeamID:        project.TeamID,
		TeamName:      project.TeamName,
		CloudRevision: project.CloudRevision,
		SyncStatus:    project.SyncStatus,
		Members: []config.Member{{
			Handle:              project.Admin.Handle,
			PublicKeySHA:        project.Admin.PublicKeySHA,
			SigningPublicKey:    project.Admin.SigningPublicKey,
			EncryptionPublicKey: project.Admin.EncryptionPublicKey,
			Role:                "admins",
		}},
	}
	for _, scope := range project.Scopes {
		parsed.Scopes = append(parsed.Scopes, config.ScopeSummary{
			Name:              scope.Name,
			EnvFiles:          append([]string(nil), scope.EnvFiles...),
			Variables:         append([]config.VariableDeclaration(nil), scope.Variables...),
			DefaultRoleAccess: defaultRoleAccess(scope.Name),
		})
	}
	return parsed
}

func setupScopesFromProject(project config.Project) []apiclient.SetupScope {
	out := make([]apiclient.SetupScope, 0, len(project.Scopes))
	for _, scope := range project.Scopes {
		out = append(out, apiclient.SetupScope{
			Name:              scope.Name,
			EnvFiles:          append([]string(nil), scope.EnvFiles...),
			Variables:         apiVariableDeclarations(scope.Variables),
			DefaultRoleAccess: defaultRoleAccess(scope.Name),
		})
	}
	return out
}

func defaultRoleAccess(scopeName string) map[string]string {
	if scopeName == "prod" {
		return map[string]string{"admins": "write"}
	}
	return map[string]string{"admins": "write", "developers": "read"}
}

func publicIdentity(summary identity.Summary) apiclient.PublicIdentity {
	return apiclient.PublicIdentity{
		Handle:              summary.Handle,
		PublicKeySHA:        summary.PublicKeySHA,
		SigningPublicKey:    summary.SigningPublicKey,
		EncryptionPublicKey: summary.EncryptionPublicKey,
	}
}

func sortedAssignmentNames(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func mapInitAPIError(err error) error {
	var apiErr *apiclient.APIError
	if !errors.As(err, &apiErr) {
		return commandError(ExitCloudUnavailable, "cloud_unavailable", "Initial cloud setup failed", err, "No propagate.yaml was written.")
	}
	switch apiErr.Code {
	case "validation_failed", "usage_error", "plaintext_rejected", "signature_invalid", "signature_missing":
		return commandError(ExitValidationError, apiErr.Code, "Initial cloud setup was rejected", apiErr, "No propagate.yaml was written.")
	case "idempotency_conflict":
		return commandError(ExitConflict, apiErr.Code, "Initial cloud setup operation conflicted with an earlier request", apiErr, "No propagate.yaml was written.")
	default:
		code := ExitCloudUnavailable
		if apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 && !apiErr.Retryable {
			code = ExitValidationError
		}
		return commandError(code, apiErr.Code, "Initial cloud setup failed", apiErr, "No propagate.yaml was written.")
	}
}

func summarizeEnvFiles(candidates []envfile.Candidate) []EnvFileSummary {
	summaries := make([]EnvFileSummary, 0, len(candidates))
	for _, c := range candidates {
		summaries = append(summaries, EnvFileSummary{
			Path:           c.Path,
			Scope:          c.Scope,
			Tracked:        c.Tracked,
			ParentTracked:  c.ParentTracked,
			VariablesCount: c.VariableCount,
		})
	}
	return summaries
}

func scopesFromCandidates(candidates []envfile.Candidate) []string {
	if len(candidates) == 0 {
		return []string{"dev"}
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		scope := c.Scope
		if scope == "" {
			scope = "dev"
		}
		seen[scope] = true
	}
	ordered := []string{"dev", "staging", "prod", "other"}
	var out []string
	for _, scope := range ordered {
		if seen[scope] {
			out = append(out, scope)
			delete(seen, scope)
		}
	}
	for scope := range seen {
		out = append(out, scope)
	}
	return out
}

func maybeApplyAgentGuidance(opts initOptions, streams Streams, reader *bufio.Reader, root string) (agents.Result, []string) {
	if opts.DryRun || opts.SkipAgentGuidance {
		return agents.Result{Status: "skipped"}, nil
	}
	if !opts.AgentGuidance {
		if opts.NonInteractive {
			return agents.Result{Status: "skipped"}, nil
		}
		ok, err := promptConfirm(reader, streams.Out, "Add Propagate guidance to AGENTS.md?", false)
		if err != nil {
			return agents.Result{Status: "skipped"}, []string{err.Error()}
		}
		if !ok {
			return agents.Result{Status: "skipped"}, nil
		}
	}
	result, err := agents.ApplyGeneric(root)
	if err != nil {
		return agents.Result{Status: "failed"}, []string{"Agent guidance was not updated: " + err.Error()}
	}
	return result, nil
}

func promptRequired(reader *bufio.Reader, out io.Writer, nonInteractive bool, label string) (string, error) {
	if nonInteractive {
		return "", commandError(ExitConfirmationRequired, "confirmation_required", label+" is required in non-interactive mode", nil)
	}
	for {
		fmt.Fprintf(out, "%s: ", label)
		value, err := reader.ReadString('\n')
		if err != nil && len(value) == 0 {
			return "", commandError(ExitUserCanceled, "user_canceled", "Prompt could not read input", err)
		}
		value = strings.TrimSpace(value)
		if value != "" {
			return value, nil
		}
		fmt.Fprintln(out, "Please enter a value.")
	}
}

func promptConfirm(reader *bufio.Reader, out io.Writer, label string, defaultYes bool) (bool, error) {
	suffix := " [y/N]: "
	if defaultYes {
		suffix = " [Y/n]: "
	}
	fmt.Fprint(out, label+suffix)
	value, err := reader.ReadString('\n')
	if err != nil && len(value) == 0 {
		return false, commandError(ExitUserCanceled, "user_canceled", "Prompt could not read input", err)
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return defaultYes, nil
	}
	switch value {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, commandError(ExitUserCanceled, "user_canceled", "Prompt was not confirmed", nil)
	}
}

func renderInitResult(w io.Writer, jsonOutput bool, result InitResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}

	fmt.Fprintln(w, "Propagate init complete.")
	if result.DryRun {
		fmt.Fprintln(w, "Mode: dry run; no files were written.")
	}
	fmt.Fprintln(w)

	if result.Identity != nil {
		action := "Using existing"
		if result.IdentityCreated {
			action = "Created"
		}
		fmt.Fprintf(w, "%s local identity: %s (%s)\n", action, result.Identity.Handle, result.Identity.PublicKeySHA)
	} else if result.IdentityCreated {
		fmt.Fprintln(w, "Local identity: would create")
	}
	if result.IdentityDir != "" {
		fmt.Fprintf(w, "Identity directory: %s\n", result.IdentityDir)
	}

	switch {
	case result.ProjectAlreadyConfigured:
		fmt.Fprintf(w, "Project config already exists: %s\n", result.ProjectConfigPath)
	case result.ProjectCreated:
		action := "Created"
		if result.DryRun {
			action = "Would create"
		}
		fmt.Fprintf(w, "%s project config: %s\n", action, result.ProjectConfigPath)
	default:
		fmt.Fprintln(w, "Project config: unchanged")
	}

	if len(result.ScopesCreated) > 0 {
		fmt.Fprintf(w, "Scopes: %s\n", strings.Join(result.ScopesCreated, ", "))
	}
	if len(result.EnvFilesMapped) > 0 {
		fmt.Fprintln(w, "Env file mappings:")
		for _, item := range result.EnvFilesMapped {
			fmt.Fprintf(w, "  - %s -> %s (%d variable name(s))\n", item.Path, item.Scope, item.VariablesCount)
		}
	} else if result.ProjectCreated {
		fmt.Fprintln(w, "Env file mappings: none discovered; dev scope created without files.")
	}

	if result.BackendStatus == "created" {
		fmt.Fprintf(w, "Variables encrypted/uploaded: %d\n", result.VariablesUploadedCount)
	} else {
		fmt.Fprintf(w, "Variables encrypted/uploaded: %d (cloud setup not run)\n", result.VariablesUploadedCount)
	}
	fmt.Fprintf(w, "Agent guidance: %s", result.AgentGuidance.Status)
	if result.AgentGuidance.Path != "" {
		fmt.Fprintf(w, " (%s)", result.AgentGuidance.Path)
	}
	fmt.Fprintln(w)

	if len(result.Warnings) > 0 {
		fmt.Fprintln(w, "\nWarnings:")
		for _, warning := range result.Warnings {
			fmt.Fprintf(w, "- %s\n", warning)
		}
	}
	if len(result.NextSteps) > 0 {
		fmt.Fprintln(w, "\nNext steps:")
		for i, step := range result.NextSteps {
			fmt.Fprintf(w, "%d. %s\n", i+1, step)
		}
	}
}

func printInitHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate init [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --handle VALUE           handle for a new local identity")
	fmt.Fprintln(w, "  --team-name VALUE        team name for new project setup")
	fmt.Fprintln(w, "  --yes                    accept safe default setup decisions")
	fmt.Fprintln(w, "  --dry-run                show what would happen without writing files")
	fmt.Fprintln(w, "  --agent-guidance         create or update AGENTS.md guidance")
	fmt.Fprintln(w, "  --skip-agent-guidance    skip AGENTS.md guidance")
	fmt.Fprintln(w, "  --json                   render machine-readable JSON")
	fmt.Fprintln(w, "  --non-interactive        fail instead of prompting")
}
