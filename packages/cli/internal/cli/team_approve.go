package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
	"propagate/cli/internal/secretcrypto"
)

type TeamApproveResult struct {
	OK             bool   `json:"ok"`
	Command        string `json:"command"`
	Status         string `json:"status"`
	DryRun         bool   `json:"dry_run,omitempty"`
	PublicKeySHA   string `json:"public_key_sha"`
	ConfigRevision string `json:"config_revision,omitempty"`
	NextSteps      []string `json:"next_steps,omitempty"`
}

func runTeamApproveCommand(args []string, global globalOptions, streams Streams) int {
	opts := global
	var dryRun bool
	fs := flag.NewFlagSet("team approve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts)
	fs.BoolVar(&dryRun, "dry-run", false, "show what would happen without approving")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(streams.Out, "Usage: propagate team approve <public_key_sha> [flags]")
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid team approve flags", err)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 1 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate team approve requires exactly one argument: the public_key_sha of the pending member", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	targetSHA := fs.Arg(0)

	result, err := runTeamApprove(targetSHA, dryRun, opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	renderTeamApproveResult(streams.Out, opts.JSON, opts.NoColor, result)
	return ExitSuccess
}

func runTeamApprove(targetSHA string, dryRun bool, opts globalOptions, streams Streams) (TeamApproveResult, error) {
	ident, err := identity.Load()
	if err != nil {
		return TeamApproveResult{}, commandError(ExitValidationError, "identity_missing", "Cannot load local Propagate identity", err)
	}

	configPath, project, err := loadProjectConfig(streams.WorkDir)
	if err != nil {
		return TeamApproveResult{}, err
	}
	_ = configPath

	apiURL := resolveAPIURL(opts.APIURL, streams.WorkDir)
	if apiURL == "" {
		return TeamApproveResult{}, commandError(ExitValidationError, "api_unavailable", "Cannot determine API URL", nil, "Set PROPAGATE_API_URL or pass --api-url.")
	}

	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
	ctx := context.Background()

	pendingData, err := client.ListPendingJoinRequests(ctx, ident, project.TeamID)
	if err != nil {
		return TeamApproveResult{}, mapAPIError(err, "Cannot list pending join requests")
	}

	var target *apiclient.JoinRequestRow
	for i := range pendingData.Requests {
		if pendingData.Requests[i].PublicKeySHA == targetSHA {
			target = &pendingData.Requests[i]
			break
		}
	}
	if target == nil {
		return TeamApproveResult{}, commandError(ExitValidationError, "join_request_not_found", fmt.Sprintf("No pending join request found for %s", targetSHA), nil)
	}

	if dryRun {
		return TeamApproveResult{
			OK:           true,
			Command:      "team approve",
			Status:       "dry_run",
			DryRun:       true,
			PublicKeySHA: targetSHA,
			NextSteps:    []string{"Re-run without --dry-run to approve."},
		}, nil
	}

	envelopes, err := buildEnvelopesForJoiner(ctx, client, ident, project, *target)
	if err != nil {
		return TeamApproveResult{}, err
	}

	opID, _ := operationID("approve_join")
	grantedScopes := target.RequestedScopes
	if grantedScopes == nil {
		grantedScopes = map[string]string{}
	}
	approveReq := apiclient.ApproveJoinRequestBody{
		OperationID:       opID,
		ScopeKeyEnvelopes: envelopes,
		GrantedRole:       target.RequestedRole,
		GrantedManagement: target.RequestedManagement,
		GrantedScopes:     grantedScopes,
		Client:            apiclient.ClientMetadata{CLIVersion: Version, ClientKind: "propagate-cli"},
	}
	result, err := client.ApproveJoinRequest(ctx, ident, project.TeamID, targetSHA, approveReq)
	if err != nil {
		return TeamApproveResult{}, mapAPIError(err, "Cannot approve join request")
	}

	return TeamApproveResult{
		OK:             true,
		Command:        "team approve",
		Status:         "success",
		PublicKeySHA:   targetSHA,
		ConfigRevision: result.ConfigRevision,
		NextSteps: []string{
			fmt.Sprintf("Member %s (%s) is now active.", target.Handle, targetSHA),
			"Run `propagate config pull` to update local propagate.yaml.",
		},
	}, nil
}

func buildEnvelopesForJoiner(ctx context.Context, client apiclient.Client, ident identity.Identity, project config.ParsedProject, joiner apiclient.JoinRequestRow) ([]apiclient.ScopeKeyEnvelope, error) {
	scopeKeyCache := map[string]struct {
		key     []byte
		version int
	}{}
	var envelopes []apiclient.ScopeKeyEnvelope
	for scope, permission := range joiner.RequestedScopes {
		if permission == "" || permission == "none" {
			continue
		}
		cached, ok := scopeKeyCache[scope]
		if !ok {
			envelopeData, err := client.KeyEnvelope(ctx, ident, project.TeamID, scope)
			if err != nil {
				return nil, mapAPIError(err, fmt.Sprintf("Cannot fetch scope key envelope for scope %q", scope))
			}
			scopeKey, err := secretcrypto.DecryptScopeKey(
				ident.EncryptionPrivateKey,
				envelopeData.ScopeKeyEnvelope.EncryptedScopeKey,
				envelopeData.ScopeKeyEnvelope.Algorithm,
				scope,
				ident.PublicKeySHA,
				envelopeData.ScopeKeyEnvelope.ScopeKeyVersion,
			)
			if err != nil {
				return nil, commandError(ExitPermissionDenied, "scope_key_decrypt_failed", fmt.Sprintf("Cannot decrypt scope key for scope %q", scope), err)
			}
			cached = struct {
				key     []byte
				version int
			}{key: scopeKey, version: envelopeData.ScopeKeyEnvelope.ScopeKeyVersion}
			scopeKeyCache[scope] = cached
		}
		encryptedScopeKey, err := secretcrypto.EncryptScopeKey(cached.key, joiner.EncryptionPublicKey, scope, joiner.PublicKeySHA, cached.version)
		if err != nil {
			return nil, commandError(ExitValidationError, "envelope_error", fmt.Sprintf("Cannot encrypt scope key for %s", joiner.PublicKeySHA), err)
		}
		envelopes = append(envelopes, apiclient.ScopeKeyEnvelope{
			Scope:             scope,
			RecipientKeySHA:   joiner.PublicKeySHA,
			ScopeKeyVersion:   cached.version,
			EncryptedScopeKey: encryptedScopeKey,
			Algorithm:         secretcrypto.EnvelopeAlgorithm,
		})
	}
	return envelopes, nil
}

func loadProjectConfig(workDir string) (string, config.ParsedProject, error) {
	worktree, err := gitutil.Discover(workDir)
	if err != nil {
		return "", config.ParsedProject{}, commandError(ExitValidationError, "not_git_repo", "Cannot locate git worktree", err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return "", config.ParsedProject{}, commandError(ExitValidationError, "config_invalid", "Cannot locate propagate.yaml", err)
	}
	if !exists {
		return "", config.ParsedProject{}, commandError(ExitValidationError, "config_missing", "propagate.yaml not found", nil)
	}
	project, err := config.ReadProject(configPath)
	if err != nil {
		return "", config.ParsedProject{}, commandError(ExitValidationError, "config_invalid", "Cannot read propagate.yaml", err)
	}
	return configPath, project, nil
}

func renderTeamApproveResult(w io.Writer, jsonOutput bool, noColor bool, result TeamApproveResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	style := newOutputStyle(noColor)
	renderCommandTitle(w, style, "Propagate team approve", result.DryRun)
	if result.DryRun {
		renderNote(w, style, fmt.Sprintf("Would approve join request for %s.", result.PublicKeySHA))
	} else {
		renderOK(w, style, fmt.Sprintf("Join request approved for %s.", result.PublicKeySHA))
	}
	if result.ConfigRevision != "" {
		fmt.Fprintf(w, "Config revision: %s\n", result.ConfigRevision)
	}
	renderNextSteps(w, style, result.NextSteps)
}
