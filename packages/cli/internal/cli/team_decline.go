package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/identity"
)

type TeamDeclineResult struct {
	OK           bool     `json:"ok"`
	Command      string   `json:"command"`
	Status       string   `json:"status"`
	DryRun       bool     `json:"dry_run,omitempty"`
	PublicKeySHA string   `json:"public_key_sha"`
	NextSteps    []string `json:"next_steps,omitempty"`
}

func runTeamDeclineCommand(args []string, global globalOptions, streams Streams) int {
	opts := global
	var dryRun bool
	fs := flag.NewFlagSet("team decline", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts)
	fs.BoolVar(&dryRun, "dry-run", false, "show what would happen without declining")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(streams.Out, "Usage: propagate team decline <public_key_sha> [flags]")
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid team decline flags", err)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 1 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate team decline requires exactly one argument: the public_key_sha of the pending member", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	targetSHA := fs.Arg(0)

	result, err := runTeamDecline(targetSHA, dryRun, opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	renderTeamDeclineResult(streams.Out, opts.JSON, opts.NoColor, result)
	return ExitSuccess
}

func runTeamDecline(targetSHA string, dryRun bool, opts globalOptions, streams Streams) (TeamDeclineResult, error) {
	ident, err := identity.Load()
	if err != nil {
		return TeamDeclineResult{}, commandError(ExitValidationError, "identity_missing", "Cannot load local Propagate identity", err)
	}

	_, project, err := loadProjectConfig(streams.WorkDir)
	if err != nil {
		return TeamDeclineResult{}, err
	}

	apiURL := resolveAPIURL(opts.APIURL, streams.WorkDir)
	if apiURL == "" {
		return TeamDeclineResult{}, commandError(ExitValidationError, "api_unavailable", "Cannot determine API URL", nil, "Set PROPAGATE_API_URL or pass --api-url.")
	}

	if dryRun {
		return TeamDeclineResult{
			OK:           true,
			Command:      "team decline",
			Status:       "dry_run",
			DryRun:       true,
			PublicKeySHA: targetSHA,
			NextSteps:    []string{"Re-run without --dry-run to decline."},
		}, nil
	}

	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
	opID, _ := operationID("decline_join")
	declineReq := apiclient.DeclineJoinRequestBody{
		OperationID: opID,
		Client:      apiclient.ClientMetadata{CLIVersion: Version, ClientKind: "propagate-cli"},
	}
	err = client.DeclineJoinRequest(context.Background(), ident, project.TeamID, targetSHA, declineReq)
	if err != nil {
		return TeamDeclineResult{}, mapAPIError(err, "Cannot decline join request")
	}

	return TeamDeclineResult{
		OK:           true,
		Command:      "team decline",
		Status:       "success",
		PublicKeySHA: targetSHA,
		NextSteps: []string{
			fmt.Sprintf("Join request for %s has been declined.", targetSHA),
		},
	}, nil
}

func renderTeamDeclineResult(w io.Writer, jsonOutput bool, noColor bool, result TeamDeclineResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	style := newOutputStyle(noColor)
	renderCommandTitle(w, style, "Propagate team decline", result.DryRun)
	if result.DryRun {
		renderNote(w, style, fmt.Sprintf("Would decline join request for %s.", result.PublicKeySHA))
	} else {
		renderOK(w, style, fmt.Sprintf("Join request declined for %s.", result.PublicKeySHA))
	}
	renderNextSteps(w, style, result.NextSteps)
}
