package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
)

type teamStatusOptions struct {
	globalOptions
}

type TeamStatusResult struct {
	OK                        bool                    `json:"ok"`
	Command                   string                  `json:"command"`
	Status                    string                  `json:"status"`
	ProjectConfigPath         string                  `json:"project_config_path"`
	TeamID                    string                  `json:"team_id"`
	TeamName                  string                  `json:"team_name"`
	Identity                  *TeamStatusIdentity     `json:"identity,omitempty"`
	CurrentRole               string                  `json:"current_role,omitempty"`
	LocalRevision             string                  `json:"local_revision,omitempty"`
	CloudRevision             string                  `json:"cloud_revision,omitempty"`
	LocalConfigHash           string                  `json:"local_config_hash,omitempty"`
	CloudConfigHash           string                  `json:"cloud_config_hash,omitempty"`
	Members                   map[string][]TeamMember `json:"members,omitempty"`
	MembersCount              int                     `json:"members_count"`
	PendingJoins              []TeamPendingJoin       `json:"pending_joins,omitempty"`
	PendingJoinsCount         int                     `json:"pending_joins_count"`
	PendingAccessChanges      []string                `json:"pending_access_changes,omitempty"`
	PendingAccessChangesCount int                     `json:"pending_access_changes_count"`
	PendingOrRecentAccess     json.RawMessage         `json:"pending_or_recent_access,omitempty"`
	LastPulls                 []TeamPullActivity      `json:"last_pulls,omitempty"`
	NeverPulled               []TeamMember            `json:"never_pulled,omitempty"`
	AuditAvailable            bool                    `json:"audit_available"`
	BackendStatus             string                  `json:"backend_status"`
	Warnings                  []string                `json:"warnings,omitempty"`
	NextSteps                 []string                `json:"next_steps,omitempty"`
}

type TeamStatusIdentity struct {
	Handle       string `json:"handle"`
	PublicKeySHA string `json:"public_key_sha"`
}

type TeamMember struct {
	Handle       string `json:"handle,omitempty"`
	PublicKeySHA string `json:"public_key_sha"`
	Role         string `json:"role"`
	Status       string `json:"status,omitempty"`
}

type TeamPendingJoin struct {
	Handle          string            `json:"handle,omitempty"`
	PublicKeySHA    string            `json:"public_key_sha"`
	RequestedRole   string            `json:"requested_role,omitempty"`
	RequestedScopes map[string]string `json:"requested_scopes,omitempty"`
	CreatedAt       string            `json:"created_at,omitempty"`
}

type TeamPullActivity struct {
	Handle             string `json:"handle,omitempty"`
	MemberPublicKeySHA string `json:"member_public_key_sha"`
	Scope              string `json:"scope,omitempty"`
	LastPulledAt       string `json:"last_pulled_at"`
}

func runTeamStatusCommand(args []string, global globalOptions, streams Streams) int {
	opts := teamStatusOptions{globalOptions: global}
	fs := flag.NewFlagSet("team status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printTeamStatusHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid team status flags", err, "Run `propagate team status --help` for usage.")
		return renderError(streams.Err, opts.JSON, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate team status does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, cmdErr)
	}

	result, err := runTeamStatus(opts, streams)
	if err != nil {
		if teamStatusHasLocalFacts(result) {
			if opts.JSON {
				renderTeamStatusError(streams.Err, result, err)
				return errorExitCode(err)
			}
			renderTeamStatusResult(streams.Out, false, result)
		}
		return renderError(streams.Err, opts.JSON, err)
	}
	renderTeamStatusResult(streams.Out, opts.JSON, result)
	return ExitSuccess
}

func runTeamStatus(opts teamStatusOptions, streams Streams) (TeamStatusResult, error) {
	result := TeamStatusResult{
		OK:            true,
		Command:       "team status",
		Status:        "success",
		Members:       map[string][]TeamMember{},
		BackendStatus: "not_contacted",
	}

	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return TeamStatusResult{}, commandError(ExitValidationError, "not_git_repo", "Cannot check team status outside a Git worktree", err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return TeamStatusResult{}, commandError(ExitValidationError, "config_invalid", "Existing Propagate config needs attention", err, "Rename `propagate.yml` to `propagate.yaml` before running team status again.")
	}
	if !exists {
		return TeamStatusResult{}, commandError(ExitValidationError, "config_missing", "propagate.yaml is required before team status", nil, "Run `propagate init` or pull the repository config first.")
	}
	result.ProjectConfigPath = configPath

	project, err := config.ReadProject(configPath)
	if err != nil {
		return TeamStatusResult{}, commandError(ExitValidationError, "config_invalid", "Cannot read propagate.yaml", err)
	}
	result.TeamID = project.TeamID
	result.TeamName = project.TeamName
	result.LocalRevision = project.CloudRevision
	result.Members = teamMembersFromLocal(project.Members)
	result.MembersCount = countTeamMembers(result.Members)
	result.PendingJoins = pendingJoinsFromLocal(project.PendingJoins)
	result.PendingJoinsCount = len(result.PendingJoins)
	result.PendingAccessChanges = append([]string(nil), project.AccessChangesRaw...)
	sort.Strings(result.PendingAccessChanges)
	result.PendingAccessChangesCount = len(result.PendingAccessChanges)

	localHash, err := config.ConfigHash(project)
	if err != nil {
		return TeamStatusResult{}, commandError(ExitValidationError, "config_invalid", "Cannot normalize propagate.yaml for team status", err)
	}
	result.LocalConfigHash = localHash

	ident, err := identity.Load()
	if err != nil {
		result.OK = false
		result.Status = "identity_missing"
		result.Warnings = append(result.Warnings, "Cloud team activity was not checked because the local Propagate identity could not be loaded.")
		result.NextSteps = []string{"Run `propagate init` to create or repair the local identity."}
		return result, commandError(ExitValidationError, "identity_missing", "Cannot load local Propagate identity for signed team status", err, result.NextSteps...)
	}
	summary := ident.Summary()
	statusIdentity := teamStatusIdentity(summary)
	result.Identity = &statusIdentity
	result.CurrentRole = localMemberRole(project, summary.PublicKeySHA)

	apiURL := resolveAPIURL(opts.APIURL)
	if apiURL == "" {
		result.OK = false
		result.Status = "cloud_unavailable"
		result.BackendStatus = "not_contacted"
		result.Warnings = append(result.Warnings, "Cloud team activity was not checked because no Propagate API URL is configured.")
		result.NextSteps = teamStatusLocalNextSteps(project, summary, "Pass `--api-url` or set PROPAGATE_API_URL.")
		return result, commandError(ExitCloudUnavailable, "cloud_unavailable", "Propagate API URL is required for team status", nil, result.NextSteps...)
	}

	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
	status, err := client.TeamStatus(context.Background(), ident, project.TeamID)
	if err != nil {
		cmdErr := mapTeamStatusAPIError(err, project, summary)
		result.OK = false
		result.Status = "failed"
		result.BackendStatus = "failed"
		result.Warnings = append(result.Warnings, "Cloud team activity request failed; local team facts are shown above.")
		result.NextSteps = commandNextSteps(cmdErr, "Retry `propagate team status` after checking connectivity and credentials.")
		switch commandCode(cmdErr) {
		case ExitCloudUnavailable:
			result.Status = "cloud_unavailable"
			result.BackendStatus = "unavailable"
		case ExitPermissionDenied:
			result.Status = "permission_denied"
			result.BackendStatus = "permission_denied"
		}
		return result, cmdErr
	}

	if status.Team.ID != "" {
		result.TeamID = status.Team.ID
	}
	if status.Team.Name != "" {
		result.TeamName = status.Team.Name
	}
	result.CloudRevision = status.Team.ConfigRevision
	result.CloudConfigHash = status.Team.ConfigHash
	if status.Actor.PublicKeySHA != "" {
		result.CurrentRole = status.Actor.Role
	}
	if len(status.Members) > 0 {
		result.Members = teamMembersFromCloud(status.Members)
		result.MembersCount = countTeamMembers(result.Members)
	}
	result.PendingOrRecentAccess = normalizeRawJSON(status.PendingOrRecentAccess)
	result.LastPulls = teamPullsFromCloud(status.LastPulls)
	result.NeverPulled = teamMemberListFromCloud(status.NeverPulled)
	result.AuditAvailable = true
	result.BackendStatus = "fetched"
	if len(result.LastPulls) == 0 {
		result.NextSteps = []string{"No env pull activity has been recorded yet."}
	}
	return result, nil
}

func teamStatusIdentity(summary identity.Summary) TeamStatusIdentity {
	return TeamStatusIdentity{
		Handle:       summary.Handle,
		PublicKeySHA: summary.PublicKeySHA,
	}
}

func teamMembersFromLocal(members []config.Member) map[string][]TeamMember {
	out := map[string][]TeamMember{}
	for _, member := range members {
		role := strings.TrimSpace(member.Role)
		if role == "" {
			role = "members"
		}
		out[role] = append(out[role], TeamMember{
			Handle:       member.Handle,
			PublicKeySHA: member.PublicKeySHA,
			Role:         role,
			Status:       "active",
		})
	}
	sortTeamMembers(out)
	return out
}

func teamMembersFromCloud(members map[string][]apiclient.Member) map[string][]TeamMember {
	out := map[string][]TeamMember{}
	for role, items := range members {
		for _, item := range items {
			memberRole := strings.TrimSpace(item.Role)
			if memberRole == "" {
				memberRole = role
			}
			if memberRole == "" {
				memberRole = "members"
			}
			status := strings.TrimSpace(item.Status)
			if status == "" {
				status = "active"
			}
			out[memberRole] = append(out[memberRole], TeamMember{
				Handle:       item.Handle,
				PublicKeySHA: item.PublicKeySHA,
				Role:         memberRole,
				Status:       status,
			})
		}
	}
	sortTeamMembers(out)
	return out
}

func teamMemberListFromCloud(members []apiclient.Member) []TeamMember {
	out := make([]TeamMember, 0, len(members))
	for _, item := range members {
		status := strings.TrimSpace(item.Status)
		if status == "" {
			status = "active"
		}
		out = append(out, TeamMember{
			Handle:       item.Handle,
			PublicKeySHA: item.PublicKeySHA,
			Role:         item.Role,
			Status:       status,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return teamMemberSortKey(out[i]) < teamMemberSortKey(out[j])
	})
	return out
}

func pendingJoinsFromLocal(joins []config.JoinRequest) []TeamPendingJoin {
	out := make([]TeamPendingJoin, 0, len(joins))
	for _, join := range joins {
		scopes := map[string]string{}
		for scope, permission := range join.RequestedScopes {
			scopes[scope] = permission
		}
		out = append(out, TeamPendingJoin{
			Handle:          join.Handle,
			PublicKeySHA:    join.PublicKeySHA,
			RequestedRole:   join.RequestedRole,
			RequestedScopes: scopes,
			CreatedAt:       join.CreatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return memberLabel(out[i].Handle, out[i].PublicKeySHA) < memberLabel(out[j].Handle, out[j].PublicKeySHA)
	})
	return out
}

func teamPullsFromCloud(pulls []apiclient.PullActivity) []TeamPullActivity {
	out := make([]TeamPullActivity, 0, len(pulls))
	for _, pull := range pulls {
		out = append(out, TeamPullActivity{
			Handle:             pull.Handle,
			MemberPublicKeySHA: pull.MemberPublicKeySHA,
			Scope:              pull.Scope,
			LastPulledAt:       pull.LastPulledAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastPulledAt != out[j].LastPulledAt {
			return out[i].LastPulledAt > out[j].LastPulledAt
		}
		return memberLabel(out[i].Handle, out[i].MemberPublicKeySHA) < memberLabel(out[j].Handle, out[j].MemberPublicKeySHA)
	})
	return out
}

func sortTeamMembers(members map[string][]TeamMember) {
	for role := range members {
		sort.Slice(members[role], func(i, j int) bool {
			return teamMemberSortKey(members[role][i]) < teamMemberSortKey(members[role][j])
		})
	}
}

func teamMemberSortKey(member TeamMember) string {
	label := strings.TrimSpace(member.Handle)
	if label == "" {
		label = member.PublicKeySHA
	}
	return label + "\x00" + member.PublicKeySHA
}

func countTeamMembers(members map[string][]TeamMember) int {
	var count int
	for _, items := range members {
		count += len(items)
	}
	return count
}

func localMemberRole(project config.ParsedProject, publicKeySHA string) string {
	if member := findMember(project.Members, publicKeySHA); member != nil {
		return member.Role
	}
	return ""
}

func normalizeRawJSON(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(trimmed)); err == nil {
		return append(json.RawMessage(nil), compact.Bytes()...)
	}
	return append(json.RawMessage(nil), []byte(trimmed)...)
}

func teamStatusLocalNextSteps(project config.ParsedProject, summary identity.Summary, cloudStep string) []string {
	var steps []string
	switch {
	case project.ActiveMemberSHAs[summary.PublicKeySHA]:
		if cloudStep != "" {
			steps = append(steps, cloudStep)
		}
	case project.PendingJoinSHAs[summary.PublicKeySHA]:
		steps = append(steps, "Commit the pending join request and ask a Propagate admin to run `propagate config push` after review.")
		if cloudStep != "" {
			steps = append(steps, cloudStep)
		}
	default:
		handle := strings.TrimSpace(summary.Handle)
		if handle == "" {
			handle = "YOUR_HANDLE"
		}
		steps = append(steps, fmt.Sprintf("Run `propagate team join --handle %s` to request access.", handle))
		if cloudStep != "" {
			steps = append(steps, cloudStep)
		}
	}
	if len(steps) == 0 && cloudStep != "" {
		steps = append(steps, cloudStep)
	}
	return steps
}

func mapTeamStatusAPIError(err error, project config.ParsedProject, summary identity.Summary) error {
	var apiErr *apiclient.APIError
	if !errors.As(err, &apiErr) {
		return commandError(ExitCloudUnavailable, "cloud_unavailable", "Cannot fetch cloud team status", err)
	}
	switch apiErr.Code {
	case "permission_denied":
		nextSteps := teamStatusLocalNextSteps(project, summary, "")
		if len(nextSteps) == 0 {
			nextSteps = []string{"Ask a Propagate admin to confirm this identity has active team access in the cloud."}
		}
		return commandError(
			ExitPermissionDenied,
			apiErr.Code,
			fmt.Sprintf("Cannot inspect cloud team status with identity %s", summary.PublicKeySHA),
			apiErr,
			nextSteps...,
		)
	case "team_not_found":
		return commandError(ExitValidationError, apiErr.Code, "The configured team was not found in the cloud", apiErr, "Run `propagate config pull` if the local config is stale.")
	case "validation_failed", "usage_error":
		return commandError(ExitValidationError, apiErr.Code, "Team status request was rejected by the cloud", apiErr)
	default:
		code := ExitCloudUnavailable
		if apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 && !apiErr.Retryable {
			code = ExitValidationError
		}
		return commandError(code, apiErr.Code, "Cannot fetch cloud team status", apiErr)
	}
}

func renderTeamStatusResult(w io.Writer, jsonOutput bool, result TeamStatusResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}

	switch result.Status {
	case "cloud_unavailable":
		fmt.Fprintln(w, "Team local status available; cloud activity unavailable.")
	case "identity_missing":
		fmt.Fprintln(w, "Team local status available; identity is missing.")
	case "permission_denied":
		fmt.Fprintln(w, "Team local status available; cloud activity denied.")
	default:
		fmt.Fprintln(w, "Team status complete.")
	}
	fmt.Fprintln(w)
	if result.TeamName != "" {
		fmt.Fprintf(w, "Team: %s (%s)\n", result.TeamName, result.TeamID)
	}
	if result.Identity != nil {
		fmt.Fprintf(w, "Checked by: %s (%s)\n", result.Identity.Handle, result.Identity.PublicKeySHA)
	}
	if result.CurrentRole != "" {
		fmt.Fprintf(w, "Current role: %s\n", result.CurrentRole)
	}
	fmt.Fprintf(w, "Local revision: %s\n", valueOrDash(result.LocalRevision))
	fmt.Fprintf(w, "Cloud revision: %s\n", valueOrDash(result.CloudRevision))
	fmt.Fprintf(w, "Local config hash: %s\n", valueOrDash(result.LocalConfigHash))
	fmt.Fprintf(w, "Cloud config hash: %s\n", valueOrDash(result.CloudConfigHash))
	fmt.Fprintf(w, "Audit available: %t\n", result.AuditAvailable)
	fmt.Fprintf(w, "Backend: %s\n", result.BackendStatus)

	renderTeamMembers(w, result.Members)
	renderPendingJoins(w, result.PendingJoins)
	renderTeamStatusList(w, "Pending access changes", result.PendingAccessChanges)
	if len(result.PendingOrRecentAccess) > 0 {
		fmt.Fprintln(w, "\nCloud pending/recent access:")
		fmt.Fprintf(w, "- %s\n", string(result.PendingOrRecentAccess))
	}
	renderPullActivity(w, result.LastPulls)
	renderNeverPulled(w, result.NeverPulled)

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

func renderTeamMembers(w io.Writer, members map[string][]TeamMember) {
	if countTeamMembers(members) == 0 {
		return
	}
	fmt.Fprintln(w, "\nMembers:")
	for _, role := range orderedTeamRoles(members) {
		fmt.Fprintf(w, "  %s:\n", role)
		for _, member := range members[role] {
			status := ""
			if member.Status != "" && member.Status != "active" {
				status = " [" + member.Status + "]"
			}
			fmt.Fprintf(w, "    - %s%s\n", memberLabel(member.Handle, member.PublicKeySHA), status)
		}
	}
}

func renderPendingJoins(w io.Writer, joins []TeamPendingJoin) {
	if len(joins) == 0 {
		return
	}
	fmt.Fprintln(w, "\nPending join requests:")
	for _, join := range joins {
		line := memberLabel(join.Handle, join.PublicKeySHA)
		if join.RequestedRole != "" {
			line += " -> " + join.RequestedRole
		}
		if len(join.RequestedScopes) > 0 {
			line += " scopes " + strings.Join(formatScopePermissions(join.RequestedScopes), ", ")
		}
		if join.CreatedAt != "" {
			line += " created " + join.CreatedAt
		}
		fmt.Fprintf(w, "- %s\n", line)
	}
}

func renderPullActivity(w io.Writer, pulls []TeamPullActivity) {
	if len(pulls) == 0 {
		return
	}
	fmt.Fprintln(w, "\nLast pulls:")
	for _, pull := range pulls {
		line := memberLabel(pull.Handle, pull.MemberPublicKeySHA)
		if pull.Scope != "" {
			line += " scope " + pull.Scope
		}
		if pull.LastPulledAt != "" {
			line += " at " + pull.LastPulledAt
		}
		fmt.Fprintf(w, "- %s\n", line)
	}
}

func renderNeverPulled(w io.Writer, members []TeamMember) {
	if len(members) == 0 {
		return
	}
	fmt.Fprintln(w, "\nNever pulled:")
	for _, member := range members {
		fmt.Fprintf(w, "- %s\n", memberLabel(member.Handle, member.PublicKeySHA))
	}
}

func renderTeamStatusList(w io.Writer, label string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s:\n", label)
	for _, value := range values {
		fmt.Fprintf(w, "- %s\n", value)
	}
}

func orderedTeamRoles(members map[string][]TeamMember) []string {
	seen := map[string]bool{}
	var roles []string
	for role := range members {
		if len(members[role]) == 0 {
			continue
		}
		seen[role] = true
	}
	for _, role := range []string{"admins", "developers"} {
		if seen[role] {
			roles = append(roles, role)
			delete(seen, role)
		}
	}
	var rest []string
	for role := range seen {
		rest = append(rest, role)
	}
	sort.Strings(rest)
	return append(roles, rest...)
}

func formatScopePermissions(scopes map[string]string) []string {
	names := sortedScopeNames(scopes)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+scopes[name])
	}
	return out
}

func renderTeamStatusError(w io.Writer, result TeamStatusResult, err error) {
	cmdErr, ok := err.(*CommandError)
	if !ok {
		cmdErr = commandError(ExitInternalError, "internal_error", "Unexpected internal error", err)
	}
	result.OK = false
	payload := struct {
		TeamStatusResult
		Error *CommandError `json:"error"`
	}{
		TeamStatusResult: result,
		Error:            cmdErr,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

func teamStatusHasLocalFacts(result TeamStatusResult) bool {
	return result.ProjectConfigPath != "" || result.TeamID != "" || result.MembersCount > 0
}

func printTeamStatusHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate team status [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --api-url VALUE     override Propagate API URL")
	fmt.Fprintln(w, "  --json              render machine-readable JSON")
	fmt.Fprintln(w, "  --non-interactive   fail instead of prompting")
}
