package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
	"propagate/cli/internal/secretcrypto"
)

type teamInviteOptions struct {
	globalOptions
	Label               string
	RequestedManagement bool
	RequestedScopes     scopeFlags
	DryRun              bool
}

// TeamInviteCreateResult is JSON output for `propagate team invite`.
type TeamInviteCreateResult struct {
	OK                  bool              `json:"ok"`
	Command             string            `json:"command"`
	Status              string            `json:"status"`
	DryRun              bool              `json:"dry_run"`
	TeamID              string            `json:"team_id,omitempty"`
	TeamName            string            `json:"team_name,omitempty"`
	InviteID            string            `json:"invite_id,omitempty"`
	PIN                 string            `json:"pin,omitempty"`
	Label               string            `json:"label,omitempty"`
	RequestedManagement bool              `json:"requested_management,omitempty"`
	RequestedScopes     map[string]string `json:"requested_scopes,omitempty"`
	BackendStatus       string            `json:"backend_status"`
	Warnings            []string          `json:"warnings,omitempty"`
	NextSteps           []string          `json:"next_steps,omitempty"`
}

func runTeamInviteCommand(args []string, global globalOptions, streams Streams) int {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			return runTeamInviteList(args[1:], global, streams)
		case "revoke":
			return runTeamInviteRevoke(args[1:], global, streams)
		case "help", "--help", "-h":
			printTeamInviteHelp(streams.Out)
			return ExitSuccess
		}
	}
	return runTeamInviteCreate(args, global, streams)
}

func runTeamInviteCreate(args []string, global globalOptions, streams Streams) int {
	opts := teamInviteOptions{globalOptions: global}
	fs := flag.NewFlagSet("team invite", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.StringVar(&opts.Label, "label", "", "human-readable name for this invite (prompted if omitted)")
	fs.BoolVar(&opts.RequestedManagement, "management", false, "grant management to whoever redeems this invite")
	fs.Var(&opts.RequestedScopes, "scope", "scope permission scope=read|write; may be repeated")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "validate locally without calling the API")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printTeamInviteHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid team invite flags", err)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate team invite does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}

	if err := promptTeamInviteInputs(&opts, streams); err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}

	result, err := runTeamInviteCreateExec(opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	if opts.JSON {
		enc := json.NewEncoder(streams.Out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return ExitSuccess
	}
	style := newOutputStyle(opts.NoColor)
	renderCommandTitle(streams.Out, style, "Team invite", opts.DryRun)
	if result.DryRun {
		renderNote(streams.Out, style, "Dry run: no invite was created.")
	} else {
		renderOK(streams.Out, style, "Invite created.")
		renderNote(streams.Out, style, "Share the PIN out of band. It will not be shown again.")
		fmt.Fprintf(streams.Out, "Invite id: %s\n", result.InviteID)
		fmt.Fprintf(streams.Out, "PIN: %s\n", result.PIN)
		fmt.Fprintf(streams.Out, "Label: %s\n", result.Label)
	}
	renderInviteAccess(streams.Out, style, result.RequestedManagement, result.RequestedScopes)
	renderWarnings(streams.Out, style, result.Warnings)
	renderNextSteps(streams.Out, style, result.NextSteps)
	return ExitSuccess
}

func runTeamInviteCreateExec(opts teamInviteOptions, streams Streams) (TeamInviteCreateResult, error) {
	label := strings.TrimSpace(opts.Label)
	if label == "" {
		return TeamInviteCreateResult{}, commandError(ExitUsageError, "usage_error", "--label is required", nil)
	}
	scopes, err := opts.RequestedScopes.Map()
	if err != nil {
		return TeamInviteCreateResult{}, commandError(ExitUsageError, "usage_error", "Invalid scope flag", err)
	}

	result := TeamInviteCreateResult{
		OK:                  true,
		Command:             "team invite",
		Status:              "success",
		DryRun:              opts.DryRun,
		Label:               label,
		RequestedManagement: opts.RequestedManagement,
		RequestedScopes:     scopes,
		BackendStatus:       "skipped",
		NextSteps: []string{
			"Share the PIN with the invitee through a trusted channel.",
			"They can run `propagate team join` and choose Join by invite code.",
		},
	}
	if opts.DryRun {
		result.Status = "dry_run"
		result.BackendStatus = "skipped"
		result.NextSteps = []string{"Re-run without `--dry-run` to create the invite."}
		return result, nil
	}

	ident, err := identity.Load()
	if err != nil {
		return TeamInviteCreateResult{}, commandError(ExitValidationError, "identity_missing", "Cannot load local Propagate identity", err)
	}
	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return TeamInviteCreateResult{}, commandError(ExitValidationError, "not_git_repo", "team invite requires a Git worktree", err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil || !exists {
		return TeamInviteCreateResult{}, commandError(ExitValidationError, "config_missing", "propagate.yaml is required", err)
	}
	project, err := config.ReadProject(configPath)
	if err != nil {
		return TeamInviteCreateResult{}, commandError(ExitValidationError, "config_invalid", "Cannot read propagate.yaml", err)
	}
	result.TeamID = project.TeamID
	result.TeamName = project.TeamName

	apiURL := resolveAPIURL(opts.APIURL, streams.WorkDir)
	if apiURL == "" {
		return TeamInviteCreateResult{}, commandError(ExitValidationError, "api_url_missing", "PROPAGATE_API_URL is required for team invite", nil)
	}

	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}

	var bundle []apiclient.RelayScopeKey
	relayData, relayErr := client.GetRelayPublicKey(context.Background())
	if relayErr == nil && relayData.RelayPublicKey != "" {
		scopeNames := make([]string, 0, len(scopes))
		for name := range scopes {
			scopeNames = append(scopeNames, name)
		}
		if len(scopeNames) == 0 {
			for _, s := range project.Scopes {
				scopeNames = append(scopeNames, s.Name)
			}
		}
		for _, scopeName := range scopeNames {
			envData, envErr := client.KeyEnvelope(context.Background(), ident, project.TeamID, scopeName)
			if envErr != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Could not fetch scope key for %q: %v", scopeName, envErr))
				continue
			}
			scopeKey, decErr := secretcrypto.DecryptScopeKey(
				ident.EncryptionPrivateKey,
				envData.ScopeKeyEnvelope.EncryptedScopeKey,
				envData.ScopeKeyEnvelope.Algorithm,
				scopeName,
				ident.PublicKeySHA,
				envData.ScopeKeyEnvelope.ScopeKeyVersion,
			)
			if decErr != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Could not decrypt scope key for %q: %v", scopeName, decErr))
				continue
			}
			encrypted, encErr := secretcrypto.EncryptScopeKey(
				scopeKey,
				relayData.RelayPublicKey,
				scopeName,
				"relay",
				envData.ScopeKeyEnvelope.ScopeKeyVersion,
			)
			if encErr != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("Could not encrypt scope key for relay: %v", encErr))
				continue
			}
			bundle = append(bundle, apiclient.RelayScopeKey{
				Scope:             scopeName,
				EncryptedScopeKey: encrypted,
				Algorithm:         secretcrypto.EnvelopeAlgorithm,
				ScopeKeyVersion:   envData.ScopeKeyEnvelope.ScopeKeyVersion,
				RelayKeyVersion:   relayData.RelayKeyVersion,
			})
		}
	}

	opID, err := operationID("team_invite")
	if err != nil {
		return TeamInviteCreateResult{}, err
	}
	req := apiclient.CreateTeamInviteRequest{
		OperationID:         opID,
		Label:               label,
		RequestedManagement: opts.RequestedManagement,
		RequestedScopes:     scopes,
		ScopeKeyBundle:      bundle,
		Client:              apiclient.ClientMetadata{CLIVersion: Version, ClientKind: "propagate-cli"},
	}
	created, err := client.CreateTeamInvite(context.Background(), ident, project.TeamID, req)
	if err != nil {
		return TeamInviteCreateResult{}, mapAPIError(err, "Team invite could not be created")
	}
	result.InviteID = created.InviteID
	result.PIN = created.PIN
	result.Label = created.Label
	result.BackendStatus = "created"
	return result, nil
}

func renderInviteAccess(w io.Writer, style outputStyle, management bool, scopes map[string]string) {
	if management {
		fmt.Fprintln(w, "Management: true")
	} else {
		fmt.Fprintln(w, "Management: false")
	}
	if len(scopes) == 0 {
		fmt.Fprintln(w, "Scopes: none specified")
		return
	}
	fmt.Fprintln(w, style.bold("Scopes:"))
	for _, scope := range sortedScopeNames(scopes) {
		fmt.Fprintf(w, "  - %s: %s\n", scope, scopes[scope])
	}
}

func runTeamInviteList(args []string, global globalOptions, streams Streams) int {
	fs := flag.NewFlagSet("team invite list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &global)
	if err := fs.Parse(args); err != nil {
		return renderError(streams.Err, global.JSON, global.NoColor, commandError(ExitUsageError, "usage_error", "Invalid flags", err))
	}
	if fs.NArg() != 0 {
		return renderError(streams.Err, global.JSON, global.NoColor, commandError(ExitUsageError, "usage_error", "unexpected arguments", nil))
	}
	ident, err := identity.Load()
	if err != nil {
		return renderError(streams.Err, global.JSON, global.NoColor, commandError(ExitValidationError, "identity_missing", "Cannot load identity", err))
	}
	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return renderError(streams.Err, global.JSON, global.NoColor, err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil || !exists {
		return renderError(streams.Err, global.JSON, global.NoColor, commandError(ExitValidationError, "config_missing", "propagate.yaml is required", err))
	}
	project, err := config.ReadProject(configPath)
	if err != nil {
		return renderError(streams.Err, global.JSON, global.NoColor, err)
	}
	apiURL := resolveAPIURL(global.APIURL, streams.WorkDir)
	if apiURL == "" {
		return renderError(streams.Err, global.JSON, global.NoColor, commandError(ExitValidationError, "api_url_missing", "PROPAGATE_API_URL is required", nil))
	}
	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
	data, err := client.ListAdminInvites(context.Background(), ident, project.TeamID)
	if err != nil {
		return renderError(streams.Err, global.JSON, global.NoColor, mapAPIError(err, "Could not list invites"))
	}
	if global.JSON {
		enc := json.NewEncoder(streams.Out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(data)
		return ExitSuccess
	}
	style := newOutputStyle(global.NoColor)
	renderCommandTitle(streams.Out, style, "Active invites", false)
	for _, inv := range data.Invites {
		fmt.Fprintf(streams.Out, "• %s  %s  (%s attempts=%d)\n", inv.InviteID, inv.Label, inv.Status, inv.FailedPINAttempts)
	}
	return ExitSuccess
}

func runTeamInviteRevoke(args []string, global globalOptions, streams Streams) int {
	fs := flag.NewFlagSet("team invite revoke", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &global)
	if err := fs.Parse(args); err != nil {
		return renderError(streams.Err, global.JSON, global.NoColor, commandError(ExitUsageError, "usage_error", "Invalid flags", err))
	}
	if fs.NArg() != 1 {
		return renderError(streams.Err, global.JSON, global.NoColor, commandError(ExitUsageError, "usage_error", "invite id is required", nil))
	}
	inviteID := strings.TrimSpace(fs.Arg(0))

	ident, err := identity.Load()
	if err != nil {
		return renderError(streams.Err, global.JSON, global.NoColor, commandError(ExitValidationError, "identity_missing", "Cannot load identity", err))
	}
	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return renderError(streams.Err, global.JSON, global.NoColor, err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil || !exists {
		return renderError(streams.Err, global.JSON, global.NoColor, commandError(ExitValidationError, "config_missing", "propagate.yaml is required", err))
	}
	project, err := config.ReadProject(configPath)
	if err != nil {
		return renderError(streams.Err, global.JSON, global.NoColor, err)
	}
	apiURL := resolveAPIURL(global.APIURL, streams.WorkDir)
	if apiURL == "" {
		return renderError(streams.Err, global.JSON, global.NoColor, commandError(ExitValidationError, "api_url_missing", "PROPAGATE_API_URL is required", nil))
	}
	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
	if err := client.RevokeTeamInvite(context.Background(), ident, project.TeamID, inviteID); err != nil {
		return renderError(streams.Err, global.JSON, global.NoColor, mapAPIError(err, "Could not revoke invite"))
	}
	if global.JSON {
		_ = json.NewEncoder(streams.Out).Encode(map[string]string{"ok": "true", "invite_id": inviteID, "status": "revoked"})
		return ExitSuccess
	}
	fmt.Fprintf(streams.Out, "Revoked invite %s\n", inviteID)
	return ExitSuccess
}

func promptTeamInviteInputs(opts *teamInviteOptions, streams Streams) error {
	if strings.TrimSpace(opts.Label) != "" && (opts.RequestedManagement || len(opts.RequestedScopes) > 0) {
		return nil
	}
	reader := bufio.NewReader(streams.In)
	if strings.TrimSpace(opts.Label) == "" {
		label, err := promptRequired(reader, streams.In, streams.Out, opts.NonInteractive, "Invite label")
		if err != nil {
			return err
		}
		opts.Label = label
	}
	if opts.RequestedManagement || len(opts.RequestedScopes) > 0 {
		return nil
	}
	if opts.NonInteractive || !promptCanUseTUI(streams.In, streams.Out) {
		return nil
	}
	scopeNames, err := projectScopeNames(streams)
	if err != nil || len(scopeNames) == 0 {
		return nil
	}
	accessScopes := make([]tuiAccessScope, 0, len(scopeNames))
	for _, name := range scopeNames {
		accessScopes = append(accessScopes, tuiAccessScope{Name: name, Permission: "none"})
	}
	access, err := promptAccessTUI(streams.In, streams.Out,
		"Access for "+opts.Label,
		[]string{"Choose what this invite can do when redeemed."},
		false, accessScopes, true)
	if err != nil {
		return err
	}
	opts.RequestedManagement = access.Management
	opts.RequestedScopes = quickstartScopeFlagsFromMap(scopeNames, access.Scopes)
	return nil
}

func projectScopeNames(streams Streams) ([]string, error) {
	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return nil, err
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil || !exists {
		return nil, err
	}
	project, err := config.ReadProject(configPath)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(project.Scopes))
	for _, scope := range project.Scopes {
		names = append(names, scope.Name)
	}
	return names, nil
}

func printTeamInviteHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate team invite [flags]                 create a PIN invite (management)")
	fmt.Fprintln(w, "  propagate team invite list [flags]            list invites (management)")
	fmt.Fprintln(w, "  propagate team invite revoke INVITE_ID        revoke an active invite (management)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Create flags:")
	fmt.Fprintln(w, "  --label VALUE       human-readable name for this invite (prompted if omitted)")
	fmt.Fprintln(w, "  --management        grant management to whoever redeems this invite")
	fmt.Fprintln(w, "  --scope VALUE       scope permission scope=read|write; may be repeated")
	fmt.Fprintln(w, "  --dry-run           validate locally without calling the API")
	fmt.Fprintln(w, "  --non-interactive   fail instead of prompting; --label becomes required")
}
