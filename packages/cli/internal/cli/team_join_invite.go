package cli

import (
	"context"
	"fmt"
	"strings"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/identity"
)

type inviteJoinMetadata struct {
	SourceInviteID    string
	SourceInviteLabel string
	RedemptionID      string
	BackendStatus     string
	PreApproved       bool
	ScopeKeyEnvelopes []apiclient.ScopeKeyEnvelope
	Member            *apiclient.Member
	Warnings          []string
}

// resolveInviteJoinIfNeeded implements join-by-invite-code when active API invites exist.
func resolveInviteJoinIfNeeded(streams Streams, opts *teamJoinOptions, teamID string, summary identity.Summary, ident identity.Identity, requestedScopes map[string]string) (*inviteJoinMetadata, error) {
	if opts.DryRun {
		joinMode := strings.TrimSpace(opts.JoinMode)
		if joinMode == "" {
			joinMode = "auto"
		}
		if joinMode == "invite" {
			return nil, commandError(ExitUsageError, "usage_error", "`--dry-run` cannot verify an invite PIN; omit `--dry-run` or use `--join-mode request`.", nil)
		}
		// In auto/request dry-run we skip cloud invite listing and keep git-mediated preview only.
		return nil, nil
	}

	apiURL := resolveAPIURL(opts.APIURL, streams.WorkDir)
	if apiURL == "" {
		return nil, nil
	}

	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
	data, err := client.GetJoinInvitesPublic(context.Background(), teamID)
	var active []apiclient.JoinerInviteRow
	if err == nil {
		active = data.Invites
	}

	joinMode := strings.TrimSpace(opts.JoinMode)
	if joinMode == "" {
		joinMode = "auto"
	}

	if len(active) == 0 {
		if joinMode == "invite" {
			return nil, commandError(ExitValidationError, "validation_failed", "No active invite codes for this team.", err)
		}
		if err != nil {
			return &inviteJoinMetadata{
				Warnings: []string{"Could not list team invite codes from the API; continuing with a normal join request."},
			}, nil
		}
		return nil, nil
	}

	useInvite := false
	switch joinMode {
	case "invite":
		useInvite = true
	case "request":
		useInvite = false
	case "auto":
		if opts.NonInteractive {
			return nil, commandError(ExitConfirmationRequired, "join_mode_required", "Active invite codes exist; pass `--join-mode request` or `--join-mode invite` (with `--invite-id` and `--pin` when required).", nil)
		}
		selected, perr := promptChoiceTUI(streams.In, streams.Out, "How do you want to join?", []string{
			"This team has one or more active invite codes.",
		}, []tuiChoice{
			{Key: "r", Label: "Request to join", Value: "request", Description: "Add a Git-mediated pending request only"},
			{Key: "i", Label: "Join by invite code", Value: "invite", Description: "Verify a PIN, then add a pending join"},
		}, 0)
		if perr != nil {
			return nil, perr
		}
		useInvite = selected == "invite"
	default:
		return nil, commandError(ExitUsageError, "usage_error", fmt.Sprintf("Invalid --join-mode %q (use auto, request, or invite)", joinMode), nil)
	}

	if !useInvite {
		return nil, nil
	}

	meta := &inviteJoinMetadata{}

	inviteID := strings.TrimSpace(opts.InviteID)
	if inviteID == "" {
		if len(active) == 1 {
			inviteID = active[0].InviteID
			meta.SourceInviteLabel = active[0].Label
		} else if !opts.NonInteractive && promptCanUseTUI(streams.In, streams.Out) {
			choices := make([]tuiChoice, len(active))
			for i, inv := range active {
				choices[i] = tuiChoice{
					Key:         fmt.Sprintf("%d", i+1),
					Label:       fmt.Sprintf("%s — %s", inv.Label, inv.InviteID),
					Value:       inv.InviteID,
					Description: "Created " + inv.CreatedAt,
				}
			}
			selected, perr := promptChoiceTUI(streams.In, streams.Out, "Select invite", nil, choices, 0)
			if perr != nil {
				return nil, perr
			}
			inviteID = selected
			for _, inv := range active {
				if inv.InviteID == inviteID {
					meta.SourceInviteLabel = inv.Label
					break
				}
			}
		} else {
			return nil, commandError(ExitConfirmationRequired, "invite_id_required", "Multiple active invites: pass `--invite-id`.", nil)
		}
	} else {
		found := false
		for _, inv := range active {
			if inv.InviteID == inviteID {
				meta.SourceInviteLabel = inv.Label
				found = true
				break
			}
		}
		if !found {
			// Non-interactive flow already has --pin: let the API decide (redeemed, locked,
			// revoked, or wrong id) so clients see e.g. invite_not_active instead of a
			// local "not in active list" error after lockout.
			if !(opts.NonInteractive && strings.TrimSpace(opts.InvitePIN) != "") {
				return nil, commandError(ExitValidationError, "validation_failed", "`--invite-id` does not match any active invite for this team.", nil)
			}
		}
	}

	pin := strings.TrimSpace(opts.InvitePIN)
	if pin == "" {
		if opts.NonInteractive {
			return nil, commandError(ExitConfirmationRequired, "pin_required", "Pass `--pin` for non-interactive invite join.", nil)
		}
		var perr error
		pin, perr = promptHiddenText(streams.In, streams.Out, "Invite code (4 digits + letter)", false)
		if perr != nil {
			return nil, perr
		}
	}

	opID, err := operationID("invite_pin")
	if err != nil {
		return nil, err
	}
	joiner := apiclient.PublicIdentity{
		Handle:              summary.Handle,
		PublicKeySHA:        summary.PublicKeySHA,
		SigningPublicKey:    summary.SigningPublicKey,
		EncryptionPublicKey: summary.EncryptionPublicKey,
	}
	pinReq := apiclient.InvitePINRequest{
		OperationID:         opID,
		PIN:                 pin,
		Joiner:              joiner,
		Handle:              summary.Handle,
		RequestedManagement: opts.RequestedManagement,
		RequestedScopes:     requestedScopes,
		Client:              apiclient.ClientMetadata{CLIVersion: Version, ClientKind: "propagate-cli"},
	}
	pinRes, err := client.SubmitInvitePIN(context.Background(), ident, teamID, inviteID, pinReq)
	if err != nil {
		return nil, mapAPIError(err, "Invite code verification failed")
	}
	meta.SourceInviteID = inviteID
	meta.RedemptionID = pinRes.RedemptionID
	meta.BackendStatus = "invite_redeemed"
	meta.PreApproved = pinRes.PreApproved
	meta.ScopeKeyEnvelopes = pinRes.ScopeKeyEnvelopes
	meta.Member = pinRes.Member
	return meta, nil
}
