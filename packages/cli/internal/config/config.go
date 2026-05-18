package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"propagate/cli/internal/atomicfile"
	"propagate/cli/internal/envfile"
	"propagate/cli/internal/identity"
)

const (
	FileName       = "propagate.yaml"
	AltFileName    = "propagate.yml"
	LocalRevision  = "rev_local_00000"
	LocalSyncState = "local_only"
)

type Scope struct {
	Name      string
	EnvFiles  []string
	Variables []VariableDeclaration
}

type ScopeSummary struct {
	Name      string
	EnvFiles  []string
	Variables []VariableDeclaration
	// DefaultRoleAccess is retained for older propagate.yaml files and cloud
	// snapshots. New renders use per-member scope grants instead.
	DefaultRoleAccess map[string]string
}

const (
	SensitivitySensitive    = "sensitive"
	SensitivityNonSensitive = "non_sensitive"
)

type VariableDeclaration struct {
	Name        string `json:"name"`
	EnvFilePath string `json:"env_file_path"`
	Sensitivity string `json:"sensitivity"`
	Digest      string `json:"digest,omitempty"`
	Literal     string `json:"literal,omitempty"`
	Preview     string `json:"preview,omitempty"`
}

type Member struct {
	Handle              string            `json:"handle"`
	PublicKeySHA        string            `json:"public_key_sha"`
	SigningPublicKey    string            `json:"signing_public_key"`
	EncryptionPublicKey string            `json:"encryption_public_key"`
	Role                string            `json:"role,omitempty"`
	Management          bool              `json:"management,omitempty"`
	Scopes              map[string]string `json:"scopes,omitempty"`
}

type ParsedProject struct {
	Version          int
	TeamID           string
	TeamName         string
	CloudRevision    string
	SyncStatus       string
	Scopes           []ScopeSummary
	Members          []Member
	ActiveMemberSHAs map[string]bool
	Raw              string
}


type Project struct {
	Version       int
	TeamID        string
	TeamName      string
	CloudRevision string
	SyncStatus    string
	Scopes        []Scope
	Admin         identity.Summary
	CreatedAt     string
}

var (
	ErrAlreadyMember = errors.New("identity is already an active team member")
)

func Path(root string) string {
	return filepath.Join(root, FileName)
}

func ExistingPath(root string) (string, bool, error) {
	canonical := filepath.Join(root, FileName)
	exists, err := atomicfile.Exists(canonical)
	if err != nil {
		return "", false, err
	}
	if exists {
		return canonical, true, nil
	}

	alternate := filepath.Join(root, AltFileName)
	exists, err = atomicfile.Exists(alternate)
	if err != nil {
		return "", false, err
	}
	if exists {
		return alternate, true, fmt.Errorf("%s exists; Propagate uses %s as the canonical config name", AltFileName, FileName)
	}
	return "", false, nil
}

func NewProject(teamName string, admin identity.Summary, candidates []envfile.Candidate) (Project, error) {
	teamName = strings.TrimSpace(teamName)
	if teamName == "" {
		return Project{}, fmt.Errorf("team name is required")
	}
	teamID, err := localTeamID()
	if err != nil {
		return Project{}, err
	}

	scopeFiles := map[string][]string{}
	for _, candidate := range candidates {
		if candidate.Path == "" {
			continue
		}
		scope := candidate.Scope
		if scope == "" {
			scope = "dev"
		}
		if err := validateScopeName(scope); err != nil {
			return Project{}, err
		}
		if err := validateEnvPath(candidate.Path); err != nil {
			return Project{}, err
		}
		scopeFiles[scope] = append(scopeFiles[scope], candidate.Path)
	}
	if len(scopeFiles) == 0 {
		scopeFiles["dev"] = nil
	}

	var scopes []Scope
	for _, name := range orderedScopes(scopeFiles) {
		files := append([]string(nil), scopeFiles[name]...)
		sort.Strings(files)
		scopes = append(scopes, Scope{Name: name, EnvFiles: files})
	}

	return Project{
		Version:  1,
		TeamID:   teamID,
		TeamName: teamName,
		Scopes:   scopes,
		Admin:    admin,
	}, nil
}

func Write(root string, project Project) error {
	payload, err := Render(project)
	if err != nil {
		return err
	}
	return atomicfile.Write(Path(root), []byte(payload), 0o644)
}

func WriteRaw(path, payload string) error {
	return atomicfile.Write(path, []byte(payload), 0o644)
}

func Render(project Project) (string, error) {
	if project.Version != 1 {
		return "", fmt.Errorf("unsupported config version %d", project.Version)
	}
	if strings.TrimSpace(project.TeamID) == "" {
		return "", fmt.Errorf("team ID is required")
	}
	if strings.TrimSpace(project.TeamName) == "" {
		return "", fmt.Errorf("team name is required")
	}
	if strings.TrimSpace(project.Admin.PublicKeySHA) == "" || strings.TrimSpace(project.Admin.SigningPublicKey) == "" || strings.TrimSpace(project.Admin.EncryptionPublicKey) == "" {
		return "", fmt.Errorf("admin public identity is incomplete")
	}

	var b strings.Builder
	b.WriteString("# Propagate project configuration.\n")
	b.WriteString("# This file is safe to commit: it stores metadata and public keys only.\n")
	b.WriteString("# Do not add env values, masked values, private keys, tokens, or credentials here.\n")
	b.WriteString("version: 1\n")
	b.WriteString("team:\n")
	b.WriteString("  id: " + quote(project.TeamID) + "\n")
	b.WriteString("  name: " + quote(project.TeamName) + "\n")
	cloudRevision := strings.TrimSpace(project.CloudRevision)
	if cloudRevision == "" {
		cloudRevision = LocalRevision
	}
	syncStatus := strings.TrimSpace(project.SyncStatus)
	if syncStatus == "" {
		syncStatus = LocalSyncState
	}
	b.WriteString("  cloud_revision: " + quote(cloudRevision) + "\n")
	b.WriteString("  sync_status: " + quote(syncStatus) + "\n\n")

	b.WriteString("scopes:\n")
	for _, scope := range project.Scopes {
		if err := validateScopeName(scope.Name); err != nil {
			return "", err
		}
		b.WriteString("  " + scope.Name + ":\n")
		if len(scope.EnvFiles) == 0 {
			b.WriteString("    env_files: []\n")
		} else {
			b.WriteString("    env_files:\n")
			for _, path := range scope.EnvFiles {
				if err := validateEnvPath(path); err != nil {
					return "", err
				}
				b.WriteString("      - " + quote(path) + "\n")
			}
		}
		if len(scope.Variables) > 0 {
			if err := renderVariableDeclarations(&b, scope.EnvFiles, scope.Variables); err != nil {
				return "", err
			}
		}
	}
	b.WriteString("\n")

	b.WriteString("members:\n")
	b.WriteString("  - handle: " + quote(project.Admin.Handle) + "\n")
	b.WriteString("    public_key_sha: " + quote(project.Admin.PublicKeySHA) + "\n")
	b.WriteString("    signing_public_key: " + quote(project.Admin.SigningPublicKey) + "\n")
	b.WriteString("    encryption_public_key: " + quote(project.Admin.EncryptionPublicKey) + "\n")
	b.WriteString("    management: true\n")
	b.WriteString("    scopes:\n")
	for _, scope := range project.Scopes {
		b.WriteString("      " + scope.Name + ": write\n")
	}
	b.WriteString("\n")
	return b.String(), nil
}

func renderVariableDeclarations(b *strings.Builder, envFiles []string, variables []VariableDeclaration) error {
	knownPaths := map[string]bool{}
	for _, path := range envFiles {
		knownPaths[path] = true
	}
	b.WriteString("    variables:\n")
	for _, variable := range sortedVariableDeclarations(variables) {
		if err := ValidateVariableDeclaration(variable, knownPaths); err != nil {
			return err
		}
		b.WriteString("      - name: " + quote(variable.Name) + "\n")
		b.WriteString("        env_file_path: " + quote(variable.EnvFilePath) + "\n")
		b.WriteString("        sensitivity: " + variable.Sensitivity + "\n")
		if variable.Digest != "" {
			b.WriteString("        digest: " + quote(variable.Digest) + "\n")
		}
		if variable.Literal != "" {
			b.WriteString("        literal: " + quote(variable.Literal) + "\n")
		}
		if variable.Preview != "" {
			b.WriteString("        preview: " + quote(variable.Preview) + "\n")
		}
	}
	return nil
}

func ReadProject(path string) (ParsedProject, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ParsedProject{}, err
	}
	raw := string(data)
	parsed := ParsedProject{
		ActiveMemberSHAs: map[string]bool{},
		Raw:              raw,
	}

	lines := splitLines(raw)
	section := ""
	currentScope := ""
	currentMember := -1
	inDefaultAccess := false
	inEnvFiles := false
	inVariables := false
	inMemberScopes := false
	scopeIndex := map[string]int{}
	currentVariable := -1
	for i := 0; i < len(lines); i++ {
		line := trimLineBreak(lines[i])
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := leadingSpaces(line)
		if indent == 0 {
			inDefaultAccess = false
			inEnvFiles = false
			inVariables = false
			inMemberScopes = false
			currentScope = ""
			currentMember = -1
			currentVariable = -1
			if strings.HasPrefix(trimmed, "version:") {
				versionValue, err := parseScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "version:")))
				if err != nil {
					return ParsedProject{}, fmt.Errorf("parse config version: %w", err)
				}
				version, err := strconv.Atoi(versionValue)
				if err != nil {
					return ParsedProject{}, fmt.Errorf("parse config version: %w", err)
				}
				parsed.Version = version
				continue
			}
			if strings.HasSuffix(trimmed, ":") {
				section = strings.TrimSuffix(trimmed, ":")
			} else {
				section = ""
			}
			continue
		}

		switch section {
		case "team":
			if indent == 2 {
				key, value, ok := splitKeyValue(trimmed)
				if !ok {
					continue
				}
				switch key {
				case "id":
					parsed.TeamID = value
				case "name":
					parsed.TeamName = value
				case "cloud_revision":
					parsed.CloudRevision = value
				case "sync_status":
					parsed.SyncStatus = value
				}
			}
		case "scopes":
			if indent == 2 && strings.HasSuffix(trimmed, ":") {
				name := strings.TrimSuffix(trimmed, ":")
				if err := ValidateScopeName(name); err != nil {
					return ParsedProject{}, err
				}
				currentScope = name
				inDefaultAccess = false
				inEnvFiles = false
				inVariables = false
				currentVariable = -1
				scopeIndex[name] = len(parsed.Scopes)
				parsed.Scopes = append(parsed.Scopes, ScopeSummary{
					Name:              name,
					DefaultRoleAccess: map[string]string{},
				})
				continue
			}
			if currentScope == "" {
				continue
			}
			if indent == 4 {
				inDefaultAccess = false
				inEnvFiles = false
				inVariables = false
				currentVariable = -1
				key, value, ok := splitKeyValue(trimmed)
				switch {
				case trimmed == "default_role_access:":
					inDefaultAccess = true
				case trimmed == "variables:":
					inVariables = true
				case strings.HasPrefix(trimmed, "env_files:"):
					if ok && value == "[]" {
						parsed.Scopes[scopeIndex[currentScope]].EnvFiles = nil
					}
					inEnvFiles = true
				case ok && key == "env_files" && value != "[]":
					return ParsedProject{}, fmt.Errorf("scope %s env_files must be a list", currentScope)
				}
				continue
			}
			if inVariables && indent == 6 && strings.HasPrefix(trimmed, "- ") {
				content := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
				idx := scopeIndex[currentScope]
				parsed.Scopes[idx].Variables = append(parsed.Scopes[idx].Variables, VariableDeclaration{Sensitivity: SensitivitySensitive})
				currentVariable = len(parsed.Scopes[idx].Variables) - 1
				if key, value, ok := splitKeyValue(content); ok {
					assignVariableField(&parsed.Scopes[idx].Variables[currentVariable], key, value)
				}
				continue
			}
			if inVariables && indent == 8 && currentVariable >= 0 {
				key, value, ok := splitKeyValue(trimmed)
				if ok {
					idx := scopeIndex[currentScope]
					assignVariableField(&parsed.Scopes[idx].Variables[currentVariable], key, value)
				}
				continue
			}
			if inEnvFiles && indent == 6 && strings.HasPrefix(trimmed, "- ") {
				value, err := parseScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
				if err != nil {
					return ParsedProject{}, fmt.Errorf("parse env file path: %w", err)
				}
				if err := validateEnvPath(value); err != nil {
					return ParsedProject{}, err
				}
				idx := scopeIndex[currentScope]
				parsed.Scopes[idx].EnvFiles = append(parsed.Scopes[idx].EnvFiles, value)
				continue
			}
			if inDefaultAccess && indent == 6 {
				role, permission, ok := splitKeyValue(trimmed)
				if !ok {
					continue
				}
				if err := ValidateRole(role); err != nil {
					return ParsedProject{}, err
				}
				if err := ValidatePermission(permission); err != nil {
					return ParsedProject{}, err
				}
				parsed.Scopes[scopeIndex[currentScope]].DefaultRoleAccess[role] = permission
			}
		case "members":
			if indent == 2 && strings.HasPrefix(trimmed, "- ") {
				content := trimmed
				content = strings.TrimSpace(strings.TrimPrefix(content, "- "))
				parsed.Members = append(parsed.Members, Member{})
				currentMember = len(parsed.Members) - 1
				inMemberScopes = false
				if key, value, ok := splitKeyValue(content); ok {
					assignMemberField(&parsed.Members[currentMember], key, value)
				}
				continue
			}
			if indent == 4 && currentMember >= 0 {
				key, value, ok := splitKeyValue(trimmed)
				if ok {
					if key == "scopes" {
						inMemberScopes = true
						if value == "{}" || value == "" {
							parsed.Members[currentMember].Scopes = map[string]string{}
						}
						continue
					}
					inMemberScopes = false
					assignMemberField(&parsed.Members[currentMember], key, value)
				}
				continue
			}
			if inMemberScopes && indent == 6 && currentMember >= 0 {
				scope, permission, ok := splitKeyValue(trimmed)
				if !ok {
					continue
				}
				if err := ValidateScopeName(scope); err != nil {
					return ParsedProject{}, err
				}
				if err := ValidatePermission(permission); err != nil {
					return ParsedProject{}, err
				}
				if parsed.Members[currentMember].Scopes == nil {
					parsed.Members[currentMember].Scopes = map[string]string{}
				}
				parsed.Members[currentMember].Scopes[scope] = permission
			}
		case "pending":
			// Legacy section — ignored.
		}
	}

	if parsed.Version != 1 {
		return ParsedProject{}, fmt.Errorf("unsupported config version %d", parsed.Version)
	}
	if strings.TrimSpace(parsed.TeamID) == "" {
		return ParsedProject{}, fmt.Errorf("team.id is required")
	}
	if strings.TrimSpace(parsed.TeamName) == "" {
		return ParsedProject{}, fmt.Errorf("team.name is required")
	}
	if strings.TrimSpace(parsed.CloudRevision) == "" {
		parsed.CloudRevision = LocalRevision
	}
	if strings.TrimSpace(parsed.SyncStatus) == "" {
		parsed.SyncStatus = LocalSyncState
	}
	for idx, scope := range parsed.Scopes {
		knownPaths := map[string]bool{}
		for _, path := range scope.EnvFiles {
			if err := validateEnvPath(path); err != nil {
				return ParsedProject{}, fmt.Errorf("scope %s: %w", scope.Name, err)
			}
			knownPaths[path] = true
		}
		for variableIdx, variable := range scope.Variables {
			if err := ValidateVariableDeclaration(variable, knownPaths); err != nil {
				return ParsedProject{}, fmt.Errorf("scope %s variable %d: %w", scope.Name, variableIdx+1, err)
			}
		}
		parsed.Scopes[idx].Variables = sortedVariableDeclarations(scope.Variables)
	}
	for idx, member := range parsed.Members {
		member = NormalizeMemberAccess(member, parsed.Scopes)
		parsed.Members[idx] = member
		if err := ValidateMember(member); err != nil {
			return ParsedProject{}, fmt.Errorf("member %d: %w", idx+1, err)
		}
		parsed.ActiveMemberSHAs[member.PublicKeySHA] = true
	}
	return parsed, nil
}

func assignMemberField(member *Member, key, value string) {
	switch key {
	case "handle":
		member.Handle = value
	case "public_key_sha":
		member.PublicKeySHA = value
	case "signing_public_key", "public_key":
		member.SigningPublicKey = value
	case "encryption_public_key":
		member.EncryptionPublicKey = value
	case "role":
		member.Role = value
	case "management":
		member.Management = parseBool(value)
	}
}

func assignVariableField(variable *VariableDeclaration, key, value string) {
	switch key {
	case "name":
		variable.Name = value
	case "env_file_path", "env_file":
		variable.EnvFilePath = value
	case "sensitivity":
		variable.Sensitivity = value
	case "digest":
		variable.Digest = value
	case "literal":
		variable.Literal = value
	case "preview":
		variable.Preview = value
	}
}

func (p ParsedProject) DefaultRequestedScopes(role string) map[string]string {
	return p.DefaultRequestedAccess(role == "admins")
}

func (p ParsedProject) DefaultRequestedAccess(management bool) map[string]string {
	if management {
		out := map[string]string{}
		for _, scope := range p.Scopes {
			out[scope.Name] = "write"
		}
		return out
	}
	out := map[string]string{}
	for _, scope := range p.Scopes {
		permission := scope.DefaultRoleAccess["developers"]
		if permission == "" && scope.Name == "dev" {
			permission = "read"
		}
		if permission == "" || permission == "none" {
			continue
		}
		out[scope.Name] = permission
	}
	if len(out) == 0 && len(p.Scopes) == 1 {
		out[p.Scopes[0].Name] = "read"
	}
	return out
}

func RenderWithApprovedMember(project ParsedProject, member Member) (string, error) {
	if project.ActiveMemberSHAs[member.PublicKeySHA] {
		return "", ErrAlreadyMember
	}
	if project.Version != 1 {
		return "", fmt.Errorf("unsupported config version %d", project.Version)
	}
	project.Members = append(project.Members, member)
	project.ActiveMemberSHAs[member.PublicKeySHA] = true
	return RenderParsed(project)
}

func RenderParsed(project ParsedProject) (string, error) {
	if project.Version != 1 {
		return "", fmt.Errorf("unsupported config version %d", project.Version)
	}
	if strings.TrimSpace(project.TeamID) == "" {
		return "", fmt.Errorf("team ID is required")
	}
	if strings.TrimSpace(project.TeamName) == "" {
		return "", fmt.Errorf("team name is required")
	}
	if strings.TrimSpace(project.CloudRevision) == "" {
		project.CloudRevision = LocalRevision
	}
	if strings.TrimSpace(project.SyncStatus) == "" {
		project.SyncStatus = LocalSyncState
	}

	var b strings.Builder
	b.WriteString("# Propagate project configuration.\n")
	b.WriteString("# This file is safe to commit: it stores metadata and public keys only.\n")
	b.WriteString("# Do not add env values, masked values, private keys, tokens, or credentials here.\n")
	b.WriteString("version: 1\n")
	b.WriteString("team:\n")
	b.WriteString("  id: " + quote(project.TeamID) + "\n")
	b.WriteString("  name: " + quote(project.TeamName) + "\n")
	b.WriteString("  cloud_revision: " + quote(project.CloudRevision) + "\n")
	b.WriteString("  sync_status: " + quote(project.SyncStatus) + "\n\n")

	b.WriteString("scopes:\n")
	for _, scope := range project.Scopes {
		if err := ValidateScopeName(scope.Name); err != nil {
			return "", err
		}
		b.WriteString("  " + scope.Name + ":\n")
		if len(scope.EnvFiles) == 0 {
			b.WriteString("    env_files: []\n")
		} else {
			b.WriteString("    env_files:\n")
			for _, path := range scope.EnvFiles {
				if err := validateEnvPath(path); err != nil {
					return "", err
				}
				b.WriteString("      - " + quote(path) + "\n")
			}
		}
		if len(scope.Variables) > 0 {
			if err := renderVariableDeclarations(&b, scope.EnvFiles, scope.Variables); err != nil {
				return "", err
			}
		}
	}
	b.WriteString("\n")

	b.WriteString("members:\n")
	for _, member := range project.Members {
		member = NormalizeMemberAccess(member, project.Scopes)
		if err := ValidateMember(member); err != nil {
			return "", err
		}
		b.WriteString("  - handle: " + quote(member.Handle) + "\n")
		b.WriteString("    public_key_sha: " + quote(member.PublicKeySHA) + "\n")
		b.WriteString("    signing_public_key: " + quote(member.SigningPublicKey) + "\n")
		b.WriteString("    encryption_public_key: " + quote(member.EncryptionPublicKey) + "\n")
		if member.Management {
			b.WriteString("    management: true\n")
		}
		if len(member.Scopes) == 0 {
			b.WriteString("    scopes: {}\n")
		} else {
			b.WriteString("    scopes:\n")
			for _, scope := range sortedMapKeys(member.Scopes) {
				b.WriteString("      " + scope + ": " + member.Scopes[scope] + "\n")
			}
		}
	}
	b.WriteString("\n")
	return b.String(), nil
}

func SnapshotJSON(project ParsedProject) ([]byte, error) {
	type snapshotScope struct {
		EnvFiles  []string              `json:"env_files"`
		Variables []VariableDeclaration `json:"variables,omitempty"`
	}
	type snapshotTeam struct {
		ID   string `json:"id,omitempty"`
		Name string `json:"name"`
	}
	type snapshot struct {
		Version int                      `json:"version"`
		Team    snapshotTeam             `json:"team"`
		Scopes  map[string]snapshotScope `json:"scopes"`
		Members []Member                 `json:"members"`
	}
	if project.Version != 1 {
		return nil, fmt.Errorf("unsupported config version %d", project.Version)
	}
	scopes := map[string]snapshotScope{}
	for _, scope := range project.Scopes {
		if err := ValidateScopeName(scope.Name); err != nil {
			return nil, err
		}
		files := append([]string{}, scope.EnvFiles...)
		for _, file := range files {
			if err := validateEnvPath(file); err != nil {
				return nil, err
			}
		}
		knownPaths := map[string]bool{}
		for _, file := range files {
			knownPaths[file] = true
		}
		variables := sortedVariableDeclarations(scope.Variables)
		for _, variable := range variables {
			if err := ValidateVariableDeclaration(variable, knownPaths); err != nil {
				return nil, err
			}
		}
		scopes[scope.Name] = snapshotScope{EnvFiles: files, Variables: variables}
	}
	members := append([]Member(nil), project.Members...)
	for idx, member := range members {
		member = NormalizeMemberAccess(member, project.Scopes)
		member.Role = ""
		members[idx] = member
		if err := ValidateMember(member); err != nil {
			return nil, fmt.Errorf("member %d: %w", idx+1, err)
		}
	}
	out := snapshot{
		Version: project.Version,
		Team:    snapshotTeam{ID: project.TeamID, Name: project.TeamName},
		Scopes:  scopes,
		Members: members,
	}
	return json.Marshal(out)
}

func ConfigHash(project ParsedProject) (string, error) {
	payload, err := SnapshotJSON(project)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func ValidateMember(member Member) error {
	if err := validatePublicIdentity(member.Handle, member.PublicKeySHA, member.SigningPublicKey, member.EncryptionPublicKey); err != nil {
		return err
	}
	if err := ValidateRole(member.Role); err != nil {
		return err
	}
	for scope, permission := range member.Scopes {
		if err := ValidateScopeName(scope); err != nil {
			return err
		}
		if err := ValidatePermission(permission); err != nil {
			return err
		}
	}
	return nil
}

func ValidateVariableDeclaration(variable VariableDeclaration, knownEnvFiles map[string]bool) error {
	if !envfileNamePattern(variable.Name) {
		return fmt.Errorf("invalid variable name %q", variable.Name)
	}
	if err := validateEnvPath(variable.EnvFilePath); err != nil {
		return err
	}
	if len(knownEnvFiles) > 0 && !knownEnvFiles[variable.EnvFilePath] {
		return fmt.Errorf("variable %s references unlisted env file %s", variable.Name, variable.EnvFilePath)
	}
	sensitivity := variable.Sensitivity
	if sensitivity == "" {
		sensitivity = SensitivitySensitive
	}
	switch sensitivity {
	case SensitivitySensitive:
		if variable.Literal != "" || variable.Preview != "" {
			return fmt.Errorf("sensitive variable %s cannot include literal or preview", variable.Name)
		}
	case SensitivityNonSensitive:
		if variable.Literal != "" && variable.Preview != "" {
			return fmt.Errorf("non-sensitive variable %s cannot include both literal and preview", variable.Name)
		}
	default:
		return fmt.Errorf("unsupported sensitivity %q", sensitivity)
	}
	if variable.Digest != "" && !strings.Contains(variable.Digest, ":") {
		return fmt.Errorf("variable %s digest must include an algorithm prefix", variable.Name)
	}
	return nil
}

func envfileNamePattern(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	first := rune(name[0])
	return first == '_' || first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z'
}

func ValidateRole(role string) error {
	switch role {
	case "", "admins", "developers":
		return nil
	default:
		return fmt.Errorf("unsupported role %q", role)
	}
}

func ValidatePermission(permission string) error {
	switch permission {
	case "none", "read", "write", "admin":
		return nil
	default:
		return fmt.Errorf("unsupported permission %q", permission)
	}
}

func ValidateScopeName(name string) error {
	return validateScopeName(name)
}

func NormalizeMemberAccess(member Member, scopes []ScopeSummary) Member {
	if member.Role == "admins" {
		member.Management = true
	}
	if member.Scopes == nil {
		member.Scopes = map[string]string{}
	}
	if len(member.Scopes) == 0 && member.Role != "" {
		member.Scopes = legacyScopesForRole(member.Role, scopes)
	}
	return member
}

func MemberCanManage(member Member) bool {
	return member.Management || member.Role == "admins"
}

func MemberScopePermission(member Member, scope ScopeSummary) string {
	member = NormalizeMemberAccess(member, []ScopeSummary{scope})
	if permission := member.Scopes[scope.Name]; permission != "" {
		return permission
	}
	return ""
}

func legacyScopesForRole(role string, scopes []ScopeSummary) map[string]string {
	out := map[string]string{}
	for _, scope := range scopes {
		permission := scope.DefaultRoleAccess[role]
		if permission == "" && role == "admins" {
			permission = "write"
		}
		if permission == "" || permission == "none" {
			continue
		}
		out[scope.Name] = permission
	}
	return out
}

func sortedVariableDeclarations(variables []VariableDeclaration) []VariableDeclaration {
	out := append([]VariableDeclaration(nil), variables...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].EnvFilePath != out[j].EnvFilePath {
			return filepath.ToSlash(out[i].EnvFilePath) < filepath.ToSlash(out[j].EnvFilePath)
		}
		return out[i].Name < out[j].Name
	})
	for idx := range out {
		if out[idx].Sensitivity == "" {
			out[idx].Sensitivity = SensitivitySensitive
		}
	}
	return out
}

func validateEnvPath(path string) error {
	if path == "" {
		return fmt.Errorf("env file path is empty")
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if filepath.IsAbs(path) || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return fmt.Errorf("env file path must be repo-relative and inside the worktree: %s", path)
	}
	return nil
}

func validateScopeName(name string) error {
	if name == "" {
		return fmt.Errorf("scope name is empty")
	}
	for i, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			if i == 0 && (r < 'a' || r > 'z') {
				return fmt.Errorf("scope %q must start with a lowercase letter", name)
			}
			continue
		}
		return fmt.Errorf("scope %q contains unsupported character %q", name, r)
	}
	return nil
}


func splitLines(raw string) []string {
	if raw == "" {
		return nil
	}
	var lines []string
	start := 0
	for i, r := range raw {
		if r == '\n' {
			lines = append(lines, raw[start:i+1])
			start = i + 1
		}
	}
	if start < len(raw) {
		lines = append(lines, raw[start:])
	}
	return lines
}

func trimLineBreak(line string) string {
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
}

func leadingSpaces(line string) int {
	count := 0
	for _, r := range line {
		if r != ' ' {
			break
		}
		count++
	}
	return count
}

func splitKeyValue(value string) (string, string, bool) {
	key, raw, found := strings.Cut(value, ":")
	if !found {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	raw = strings.TrimSpace(raw)
	if key == "" {
		return "", "", false
	}
	parsed, err := parseScalar(raw)
	if err != nil {
		return "", "", false
	}
	return key, parsed, true
}

func parseScalar(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "\"") {
		return strconv.Unquote(value)
	}
	if before, _, found := strings.Cut(value, " #"); found {
		value = before
	}
	return strings.TrimSpace(value), nil
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "1":
		return true
	default:
		return false
	}
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func copyStringMap(values map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range values {
		out[key] = value
	}
	return out
}

func orderedScopes(scopeFiles map[string][]string) []string {
	var names []string
	for name := range scopeFiles {
		names = append(names, name)
	}
	rank := map[string]int{"dev": 0, "staging": 1, "prod": 2, "other": 3}
	sort.Slice(names, func(i, j int) bool {
		left, lok := rank[names[i]]
		right, rok := rank[names[j]]
		if lok && rok {
			return left < right
		}
		if lok {
			return true
		}
		if rok {
			return false
		}
		return names[i] < names[j]
	})
	return names
}

func localTeamID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "team_" + hex.EncodeToString(raw[:]), nil
}

func quote(value string) string {
	return strconv.Quote(value)
}

func ReadRaw(path string) ([]byte, error) {
	return os.ReadFile(path)
}
