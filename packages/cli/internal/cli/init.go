package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
	Handle              string
	TeamName            string
	Yes                 bool
	DryRun              bool
	AgentGuidance       bool
	SkipAgentGuidance   bool
	ExistingProjectOnly bool
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
	ConfigRevision           string            `json:"config_revision,omitempty"`
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
	Path           string                `json:"path"`
	Scope          string                `json:"scope"`
	Tracked        bool                  `json:"tracked"`
	ParentTracked  bool                  `json:"parent_tracked"`
	VariablesCount int                   `json:"variables_count"`
	Variables      []InitVariableSummary `json:"-"`
}

type InitVariableSummary struct {
	Name        string
	MaskedValue string
	ValueKnown  bool
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
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate init does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if opts.AgentGuidance && opts.SkipAgentGuidance {
		cmdErr := commandError(ExitUsageError, "usage_error", "--agent-guidance and --skip-agent-guidance cannot be used together", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}

	result, err := runInit(opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	renderInitResult(streams.Out, opts.JSON, opts.NoColor, result)
	return ExitSuccess
}

func runInit(opts initOptions, streams Streams) (InitResult, error) {
	reader := bufio.NewReader(streams.In)
	return runInitWithReader(opts, streams, reader)
}

func runInitWithReader(opts initOptions, streams Streams, reader *bufio.Reader) (InitResult, error) {
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
		handle, err := promptRequired(reader, streams.In, streams.Out, opts.NonInteractive, "Handle (name or email)")
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
		if existingProject, err := config.ReadProject(existingConfig); err == nil {
			result.ConfigRevision = existingProject.CloudRevision
		}
		result.NextSteps = []string{
			"Run `propagate team join` if you need to request access for this identity.",
			"Commit only reviewed metadata changes; never add env values to propagate.yaml.",
		}
		guidance, warnings := maybeApplyAgentGuidance(opts, streams, reader, worktree.Root)
		result.AgentGuidance = guidance
		result.Warnings = append(result.Warnings, warnings...)
		return result, nil
	}
	if opts.ExistingProjectOnly {
		return InitResult{}, commandError(
			ExitValidationError,
			"config_missing",
			"propagate.yaml is required before requesting team access",
			nil,
			"Ask a Propagate management member to share the repository's Propagate config before running `propagate team join --init`.",
		)
	}

	if strings.TrimSpace(opts.TeamName) == "" {
		teamName, err := promptRequired(reader, streams.In, streams.Out, opts.NonInteractive, "Team name")
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
	result.EnvFilesMapped = summarizeEnvFiles(worktree.Root, candidates)
	for _, c := range candidates {
		result.VariablesDiscoveredCount += c.VariableCount
	}

	if opts.DryRun {
		result.ProjectCreated = true
		result.ProjectConfigPath = config.Path(worktree.Root)
		result.ConfigRevision = config.LocalRevision
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
		ok, err := promptConfirm(reader, streams.In, streams.Out, fmt.Sprintf("Create %s with %d env file mapping(s)?", config.FileName, len(candidates)), true)
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
	if apiURL := resolveAPIURL(opts.APIURL, streams.WorkDir); apiURL != "" {
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
	result.ConfigRevision = project.CloudRevision
	if strings.TrimSpace(result.ConfigRevision) == "" {
		result.ConfigRevision = config.LocalRevision
	}
	if len(result.ScopesCreated) == 0 {
		for _, scope := range project.Scopes {
			result.ScopesCreated = append(result.ScopesCreated, scope.Name)
		}
	}
	result.NextSteps = []string{
		"Review propagate.yaml and run `propagate config edit` to change scope sensitivity, add/delete variables.",
		"Commit the setup when ready.",
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
			Management:          true,
			Scopes:              projectAdminScopes(project.Scopes),
		}},
	}
	for _, scope := range project.Scopes {
		parsed.Scopes = append(parsed.Scopes, config.ScopeSummary{
			Name:      scope.Name,
			EnvFiles:  append([]string(nil), scope.EnvFiles...),
			Variables: append([]config.VariableDeclaration(nil), scope.Variables...),
		})
	}
	return parsed
}

func setupScopesFromProject(project config.Project) []apiclient.SetupScope {
	out := make([]apiclient.SetupScope, 0, len(project.Scopes))
	for _, scope := range project.Scopes {
		out = append(out, apiclient.SetupScope{
			Name:      scope.Name,
			EnvFiles:  append([]string(nil), scope.EnvFiles...),
			Variables: apiVariableDeclarations(scope.Variables),
		})
	}
	return out
}

func projectAdminScopes(scopes []config.Scope) map[string]string {
	out := map[string]string{}
	for _, scope := range scopes {
		out[scope.Name] = "write"
	}
	return out
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

func summarizeEnvFiles(root string, candidates []envfile.Candidate) []EnvFileSummary {
	summaries := make([]EnvFileSummary, 0, len(candidates))
	for _, c := range candidates {
		summary := EnvFileSummary{
			Path:           c.Path,
			Scope:          c.Scope,
			Tracked:        c.Tracked,
			ParentTracked:  c.ParentTracked,
			VariablesCount: c.VariableCount,
		}
		values, knownValues := maskedInitValues(root, c.Path)
		for _, name := range c.Variables {
			variable := InitVariableSummary{Name: name}
			if knownValues {
				variable.ValueKnown = true
				variable.MaskedValue = initMaskValue(values[name])
			}
			summary.Variables = append(summary.Variables, variable)
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func maskedInitValues(root, rel string) (map[string]string, bool) {
	if strings.TrimSpace(root) == "" {
		return nil, false
	}
	absPath, err := repoFilePath(root, rel)
	if err != nil {
		return nil, false
	}
	assignments, err := envfile.ParseAssignments(absPath)
	if err != nil {
		return nil, false
	}
	return assignments.Values, true
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
		ok, err := promptConfirm(reader, streams.In, streams.Out, "Add Propagate guidance to AGENTS.md?", true)
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

func renderInitResult(w io.Writer, jsonOutput bool, noColor bool, result InitResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}

	style := newOutputStyle(noColor)
	renderCommandTitle(w, style, "Project setup", result.DryRun)

	renderInitIdentity(w, style, result)
	fmt.Fprintln(w)

	if result.ProjectAlreadyConfigured {
		renderInitProject(w, style, result)
		renderInitAgentGuidance(w, style, result)
		renderInitFooter(w, style, result)
		return
	}

	renderInitEnvScan(w, style, result)
	renderInitCloud(w, style, result)
	renderInitProject(w, style, result)
	renderInitAgentGuidance(w, style, result)
	renderInitFooter(w, style, result)
}

func renderInitIdentity(w io.Writer, style initStyle, result InitResult) {
	fmt.Fprintln(w, "Checking for existing identity...")
	if result.Identity != nil {
		if result.IdentityCreated {
			fmt.Fprintf(w, "%s Keypair generated → %s\n", style.ok(), displayInitPath(result.IdentityPath))
		} else {
			fmt.Fprintf(w, "%s Existing keypair found → %s\n", style.ok(), displayInitPath(result.IdentityPath))
		}
		fmt.Fprintf(w, "%s Identity set: %s (%s)\n", style.ok(), result.Identity.Handle, result.Identity.PublicKeySHA)
	} else if result.IdentityCreated {
		fmt.Fprintf(w, "%s Keypair would be generated → %s\n", style.note(), displayInitPath(result.IdentityPath))
	} else {
		fmt.Fprintf(w, "%s Identity unchanged\n", style.note())
	}
}

func renderInitEnvScan(w io.Writer, style initStyle, result InitResult) {
	fmt.Fprintln(w, "Scanning repository for .env files...")
	if len(result.EnvFilesMapped) == 0 {
		fmt.Fprintf(w, "%s No .env files discovered; dev scope will be created without files.\n", style.note())
		return
	}
	for _, item := range result.EnvFilesMapped {
		scope := defaultInitScope(item.Scope)
		fmt.Fprintf(w, "%s Found %s (%s) scope: %s\n", style.ok(), item.Path, countLabel(item.VariablesCount, "variable"), scope)
		for _, variable := range item.Variables {
			fmt.Fprintf(w, "  %s\n", initVariableLine(variable, scope))
		}
	}
}

func renderInitCloud(w io.Writer, style initStyle, result InitResult) {
	if result.BackendStatus == "created" {
		fmt.Fprintln(w, initEncryptionLine(result))
		fmt.Fprintf(w, "%s Uploaded to cloud (%s)\n", style.ok(), countLabel(result.VariablesUploadedCount, "variable"))
		return
	}
	fmt.Fprintln(w, "Preparing cloud upload...")
	if result.DryRun {
		fmt.Fprintf(w, "%s Dry run; encrypted upload was not attempted.\n", style.note())
		return
	}
	if result.VariablesDiscoveredCount > 0 {
		fmt.Fprintf(w, "%s Cloud upload skipped; %s discovered and no API URL configured.\n", style.note(), countLabel(result.VariablesDiscoveredCount, "variable"))
		return
	}
	fmt.Fprintf(w, "%s Cloud upload skipped; no API URL configured.\n", style.note())
}

func renderInitProject(w io.Writer, style initStyle, result InitResult) {
	switch {
	case result.ProjectAlreadyConfigured:
		fmt.Fprintln(w, "Checking project config...")
		if result.ConfigRevision != "" {
			fmt.Fprintf(w, "%s Project config already exists (%s) → %s\n", style.ok(), result.ConfigRevision, displayInitPath(result.ProjectConfigPath))
		} else {
			fmt.Fprintf(w, "%s Project config already exists → %s\n", style.ok(), displayInitPath(result.ProjectConfigPath))
		}
	case result.ProjectCreated:
		fmt.Fprintln(w, "Writing project metadata...")
		action := "propagate.yaml written"
		if result.DryRun {
			action = "propagate.yaml would be written"
		}
		if result.ConfigRevision != "" {
			fmt.Fprintf(w, "%s %s (%s) → %s\n", style.ok(), action, result.ConfigRevision, displayInitPath(result.ProjectConfigPath))
		} else {
			fmt.Fprintf(w, "%s %s → %s\n", style.ok(), action, displayInitPath(result.ProjectConfigPath))
		}
	default:
		fmt.Fprintf(w, "%s Project config unchanged\n", style.note())
	}
}

func renderInitAgentGuidance(w io.Writer, style initStyle, result InitResult) {
	fmt.Fprintln(w, "Installing agent guidance...")
	status := result.AgentGuidance.Status
	path := displayInitPath(result.AgentGuidance.Path)
	switch status {
	case "created":
		fmt.Fprintf(w, "%s AGENTS.md guidance created → %s\n", style.ok(), path)
	case "updated":
		fmt.Fprintf(w, "%s AGENTS.md guidance updated → %s\n", style.ok(), path)
	case "unchanged":
		fmt.Fprintf(w, "%s AGENTS.md guidance already up to date → %s\n", style.ok(), path)
	case "failed":
		fmt.Fprintf(w, "%s AGENTS.md guidance failed\n", style.warn())
	case "skipped", "":
		fmt.Fprintf(w, "%s AGENTS.md guidance skipped\n", style.note())
	default:
		if path != "" {
			fmt.Fprintf(w, "%s AGENTS.md guidance %s → %s\n", style.note(), status, path)
		} else {
			fmt.Fprintf(w, "%s AGENTS.md guidance %s\n", style.note(), status)
		}
	}
}

func renderInitFooter(w io.Writer, style initStyle, result InitResult) {
	renderWarnings(w, style, result.Warnings)
	renderNextSteps(w, style, result.NextSteps)
}

func initVariableLine(variable InitVariableSummary, scope string) string {
	parts := []string{variable.Name}
	if variable.ValueKnown {
		masked := variable.MaskedValue
		if masked == "" {
			masked = "<empty>"
		}
		parts = append(parts, masked)
	}
	parts = append(parts, "scope: "+scope)
	return strings.Join(parts, " ")
}

func initMaskValue(value string) string {
	switch len(value) {
	case 0:
		return ""
	case 1:
		return "**"
	case 2:
		return value[:1] + "**"
	default:
		return value[:1] + "**" + value[len(value)-1:]
	}
}

func initEncryptionLine(result InitResult) string {
	counts := initScopeCounts(result.EnvFilesMapped)
	total := result.VariablesDiscoveredCount
	if total == 0 {
		for _, count := range counts {
			total += count.Variables
		}
	}
	switch len(counts) {
	case 0:
		return "Creating cloud team..."
	case 1:
		return fmt.Sprintf("Encrypting %s for scope: %s...", countLabel(counts[0].Variables, "variable"), counts[0].Scope)
	default:
		return fmt.Sprintf("Encrypting %s across %s...", countLabel(total, "variable"), countLabel(len(counts), "scope"))
	}
}

type initScopeCount struct {
	Scope     string
	Variables int
}

func initScopeCounts(files []EnvFileSummary) []initScopeCount {
	counts := map[string]int{}
	for _, file := range files {
		counts[defaultInitScope(file.Scope)] += file.VariablesCount
	}
	ordered := []string{"dev", "staging", "prod", "other"}
	var out []initScopeCount
	for _, scope := range ordered {
		if count, ok := counts[scope]; ok {
			out = append(out, initScopeCount{Scope: scope, Variables: count})
			delete(counts, scope)
		}
	}
	var extra []initScopeCount
	for scope, count := range counts {
		extra = append(extra, initScopeCount{Scope: scope, Variables: count})
	}
	sort.Slice(extra, func(i, j int) bool {
		return extra[i].Scope < extra[j].Scope
	})
	out = append(out, extra...)
	return out
}

func defaultInitScope(scope string) string {
	if strings.TrimSpace(scope) == "" {
		return "dev"
	}
	return scope
}

func countLabel(count int, singular string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %ss", count, singular)
}

func displayInitPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return "(unknown)"
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		rel, err := filepath.Rel(home, path)
		if err == nil && rel == "." {
			return "~"
		}
		if err == nil && rel != "" && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
			return filepath.ToSlash(filepath.Join("~", rel))
		}
	}
	return filepath.ToSlash(path)
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
	fmt.Fprintln(w, "  --no-color               disable terminal color")
}
