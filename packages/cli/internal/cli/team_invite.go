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
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
)

type teamInviteOptions struct {
	globalOptions
	Label           string
	RequestedRole   string
	RequestedScopes scopeFlags
	DryRun          bool
}

// TeamInviteCreateResult is JSON output for `propagate team invite`.
type TeamInviteCreateResult struct {
	OK            bool     `json:"ok"`
	Command       string   `json:"command"`
	Status        string   `json:"status"`
	DryRun        bool     `json:"dry_run"`
	TeamID        string   `json:"team_id,omitempty"`
	TeamName      string   `json:"team_name,omitempty"`
	InviteID      string   `json:"invite_id,omitempty"`
	PIN           string   `json:"pin,omitempty"`
	Label         string   `json:"label,omitempty"`
	BackendStatus string   `json:"backend_status"`
	Warnings      []string `json:"warnings,omitempty"`
	NextSteps     []string `json:"next_steps,omitempty"`
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
	opts := teamInviteOptions{globalOptions: global, RequestedRole: "developers"}
	fs := flag.NewFlagSet("team invite", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.StringVar(&opts.Label, "label", "", "human-readable name for this invite (required)")
	fs.StringVar(&opts.RequestedRole, "role", opts.RequestedRole, "default requested role for redeeming joiners")
	fs.Var(&opts.RequestedScopes, "scope", "default scope permission scope=perm; may be repeated")
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
	renderCommandTitle(streams.Out, style, "Propagate team invite", opts.DryRun)
	if result.DryRun {
		renderNote(streams.Out, style, "Dry run: no invite was created.")
	} else {
		renderOK(streams.Out, style, "Invite created.")
		renderNote(streams.Out, style, "Share the PIN out of band. It will not be shown again.")
		fmt.Fprintf(streams.Out, "Invite id: %s\n", result.InviteID)
		fmt.Fprintf(streams.Out, "PIN: %s\n", result.PIN)
		fmt.Fprintf(streams.Out, "Label: %s\n", result.Label)
	}
	renderWarnings(streams.Out, style, result.Warnings)
	renderNextSteps(streams.Out, style, result.NextSteps)
	return ExitSuccess
}

func runTeamInviteCreateExec(opts teamInviteOptions, streams Streams) (TeamInviteCreateResult, error) {
	label := strings.TrimSpace(opts.Label)
	if label == "" {
		return TeamInviteCreateResult{}, commandError(ExitUsageError, "usage_error", "--label is required", nil)
	}
	if err := config.ValidateRole(opts.RequestedRole); err != nil {
		return TeamInviteCreateResult{}, commandError(ExitUsageError, "usage_error", "Invalid role", err)
	}
	scopes, err := opts.RequestedScopes.Map()
	if err != nil {
		return TeamInviteCreateResult{}, commandError(ExitUsageError, "usage_error", "Invalid scope flag", err)
	}

	result := TeamInviteCreateResult{
		OK:            true,
		Command:       "team invite",
		Status:        "success",
		DryRun:        opts.DryRun,
		BackendStatus: "skipped",
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

	opID, err := operationID("team_invite")
	if err != nil {
		return TeamInviteCreateResult{}, err
	}
	req := apiclient.CreateTeamInviteRequest{
		OperationID:     opID,
		Label:           label,
		RequestedRole:   opts.RequestedRole,
		RequestedScopes: scopes,
		Client:          apiclient.ClientMetadata{CLIVersion: Version, ClientKind: "propagate-cli"},
	}
	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
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
	renderCommandTitle(streams.Out, style, "Propagate team invite list", false)
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

func printTeamInviteHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate team invite --label TEXT [flags]   create a PIN invite (admin)")
	fmt.Fprintln(w, "  propagate team invite list [flags]            list invites (admin)")
	fmt.Fprintln(w, "  propagate team invite revoke INVITE_ID        revoke an active invite (admin)")
}
