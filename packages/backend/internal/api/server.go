package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"propagate/backend/internal/domain"
	"propagate/backend/internal/signing"
	"propagate/backend/internal/storage"
)

const (
	defaultAPIVersion = "0.1.0-dev"
	defaultMinCLI     = "0.1.0-dev"
	defaultMaxBody    = 4 << 20
)

type Config struct {
	APIVersion            string
	MinCLIVersion         string
	RecommendedCLIVersion string
	RequestSkew           time.Duration
	MaxBodyBytes          int64
	Now                   func() time.Time
}

func ConfigFromEnv() Config {
	maxBody := int64(defaultMaxBody)
	if value := strings.TrimSpace(os.Getenv("PROPAGATE_MAX_BODY_BYTES")); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed > 0 {
			maxBody = parsed
		}
	}

	skew := 5 * time.Minute
	if value := strings.TrimSpace(os.Getenv("PROPAGATE_REQUEST_SKEW_SECONDS")); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed > 0 {
			skew = time.Duration(parsed) * time.Second
		}
	}

	return Config{
		APIVersion:            envOrDefault("PROPAGATE_API_VERSION", defaultAPIVersion),
		MinCLIVersion:         envOrDefault("PROPAGATE_MIN_CLI_VERSION", defaultMinCLI),
		RecommendedCLIVersion: os.Getenv("PROPAGATE_RECOMMENDED_CLI_VERSION"),
		RequestSkew:           skew,
		MaxBodyBytes:          maxBody,
		Now:                   time.Now,
	}
}

type Server struct {
	store  storage.Store
	config Config
	mux    *http.ServeMux
}

func NewServer(store storage.Store, config Config) http.Handler {
	if config.APIVersion == "" {
		config.APIVersion = defaultAPIVersion
	}
	if config.MinCLIVersion == "" {
		config.MinCLIVersion = defaultMinCLI
	}
	if config.RequestSkew == 0 {
		config.RequestSkew = 5 * time.Minute
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = defaultMaxBody
	}
	if config.Now == nil {
		config.Now = time.Now
	}

	s := &Server{
		store:  store,
		config: config,
		mux:    http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/v1/version", s.handleVersion)
	s.mux.HandleFunc("/v1/teams/setup", s.handleTeamSetup)
	s.mux.HandleFunc("/v1/teams/", s.handleTeamRoutes)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	requestID := requestID()
	if r.Method != http.MethodGet {
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
		return
	}
	data := domain.VersionData{
		APIVersion:            s.config.APIVersion,
		MinCLIVersion:         s.config.MinCLIVersion,
		RecommendedCLIVersion: s.config.RecommendedCLIVersion,
		ServerTime:            s.config.Now().UTC().Format(time.RFC3339),
		Features: map[string]bool{
			"team_setup":        true,
			"request_signing":   true,
			"encrypted_secrets": true,
			"team_invites":      true,
		},
	}
	writeJSON(w, http.StatusOK, Envelope{
		OK:        true,
		RequestID: requestID,
		Data:      data,
	})
}

func (s *Server) handleTeamSetup(w http.ResponseWriter, r *http.Request) {
	requestID := requestID()
	if r.Method != http.MethodPost {
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.config.MaxBodyBytes))
	if err != nil {
		s.writeError(w, requestID, "", newAPIError(http.StatusRequestEntityTooLarge, "usage_error", "request body is too large"))
		return
	}
	defer r.Body.Close()

	var request domain.TeamSetupRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&request); err != nil {
		s.writeError(w, requestID, "", newAPIError(http.StatusBadRequest, "usage_error", "request body must be valid JSON"))
		return
	}
	if err := request.Validate(); err != nil {
		s.writeError(w, requestID, request.OperationID, validationError(err.Error()))
		return
	}

	if err := s.verifySetupSignature(r, body, request); err != nil {
		apiErr, ok := err.(apiError)
		if !ok {
			apiErr = newAPIError(http.StatusUnauthorized, "signature_invalid", err.Error())
		}
		s.writeError(w, requestID, request.OperationID, apiErr)
		return
	}

	configHash, err := domain.ConfigHash(request.ConfigSnapshot)
	if err != nil {
		s.writeError(w, requestID, request.OperationID, validationError("config_snapshot could not be hashed"))
		return
	}

	result, err := s.store.CreateTeamSetup(r.Context(), request, configHash)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrIdempotencyConflict):
			s.writeError(w, requestID, request.OperationID, newAPIError(http.StatusConflict, "idempotency_conflict", "operation_id was already used with a different setup request"))
		default:
			apiErr := newAPIError(http.StatusInternalServerError, "server_error", "team setup could not be completed")
			apiErr.body.Retryable = true
			s.writeError(w, requestID, request.OperationID, apiErr)
		}
		return
	}

	writeJSON(w, http.StatusCreated, Envelope{
		OK:          true,
		RequestID:   requestID,
		OperationID: request.OperationID,
		Data:        result,
	})
}

func (s *Server) verifySetupSignature(r *http.Request, body []byte, request domain.TeamSetupRequest) error {
	metadata := signing.MetadataFromHeaders(r.Header)
	if metadata.OperationID == "" {
		metadata.OperationID = request.OperationID
	}
	if err := metadata.Validate(request.OperationID); err != nil {
		return newAPIError(http.StatusUnauthorized, "signature_missing", err.Error())
	}
	if metadata.PublicKeySHA != request.FirstAdmin.PublicKeySHA {
		return newAPIError(http.StatusUnauthorized, "signature_invalid", "signature public key SHA does not match first management member")
	}
	timestamp, err := time.Parse(time.RFC3339, metadata.Timestamp)
	if err != nil {
		return newAPIError(http.StatusUnauthorized, "clock_skew", "signature timestamp must be RFC3339")
	}
	now := s.config.Now().UTC()
	if timestamp.Before(now.Add(-s.config.RequestSkew)) || timestamp.After(now.Add(s.config.RequestSkew)) {
		apiErr := newAPIError(http.StatusUnauthorized, "clock_skew", "signature timestamp is outside the accepted window")
		apiErr.body.Details = map[string]any{"server_time": now.Format(time.RFC3339)}
		return apiErr
	}

	publicKey, err := signing.ParseOpenSSHEd25519PublicKey(request.FirstAdmin.SigningPublicKey)
	if err != nil {
		return newAPIError(http.StatusUnauthorized, "signature_invalid", "first management member signing public key is invalid")
	}
	canonical := signing.Canonical(r.Method, r.URL.Path, r.URL.RawQuery, body, metadata)
	if err := signing.Verify(publicKey, canonical, metadata.Signature); err != nil {
		return newAPIError(http.StatusUnauthorized, "signature_invalid", "request signature could not be verified")
	}

	expiresAt := now.Add(s.config.RequestSkew + time.Minute)
	if err := s.store.ReserveNonce(r.Context(), metadata.PublicKeySHA, metadata.Nonce, expiresAt); err != nil {
		if errors.Is(err, storage.ErrReplayRejected) {
			return newAPIError(http.StatusUnauthorized, "replay_rejected", "request nonce was already used")
		}
		apiErr := newAPIError(http.StatusServiceUnavailable, "cloud_unavailable", "request nonce could not be reserved")
		apiErr.body.Retryable = true
		return apiErr
	}
	return nil
}

func (s *Server) handleTeamRoutes(w http.ResponseWriter, r *http.Request) {
	requestID := requestID()
	parts, err := teamPathParts(r.URL.Path)
	if err != nil {
		s.writeError(w, requestID, "", newAPIError(http.StatusNotFound, "usage_error", "unknown endpoint"))
		return
	}
	teamID := parts[0]

	switch {
	case len(parts) == 2 && parts[1] == "config":
		if r.Method != http.MethodGet {
			s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
			return
		}
		actor, ok := s.verifyProtectedOrWrite(w, r, requestID, teamID, nil, "")
		if !ok {
			return
		}
		data, err := s.store.GetConfig(r.Context(), teamID, actor, s.config.Now().UTC().Format(time.RFC3339))
		if err != nil {
			s.writeStorageError(w, requestID, "", err)
			return
		}
		if err := domain.ValidateMetadataOnlyJSON(data.ConfigSnapshot); err != nil {
			s.writeError(w, requestID, "", newAPIError(http.StatusInternalServerError, "server_error", "stored config snapshot failed metadata validation"))
			return
		}
		if err := domain.ValidateConfigSnapshot(data.ConfigSnapshot); err != nil {
			s.writeError(w, requestID, "", newAPIError(http.StatusInternalServerError, "server_error", "stored config snapshot failed config validation"))
			return
		}
		snapshotTeamID, err := domain.ConfigSnapshotTeamID(data.ConfigSnapshot)
		if err != nil || snapshotTeamID != teamID {
			s.writeError(w, requestID, "", newAPIError(http.StatusInternalServerError, "server_error", "stored config snapshot team id is invalid"))
			return
		}
		writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, Data: data})
	case len(parts) == 3 && parts[1] == "config" && parts[2] == "status":
		if r.Method != http.MethodGet {
			s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
			return
		}
		actor, ok := s.verifyProtectedOrWrite(w, r, requestID, teamID, nil, "")
		if !ok {
			return
		}
		data, err := s.store.ConfigStatus(r.Context(), teamID, actor, r.URL.Query().Get("local_revision"), r.URL.Query().Get("local_config_hash"))
		if err != nil {
			s.writeStorageError(w, requestID, "", err)
			return
		}
		writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, Data: data})
	case len(parts) == 3 && parts[1] == "config" && parts[2] == "push":
		s.handleConfigPush(w, r, requestID, teamID)
	case len(parts) == 2 && parts[1] == "status":
		if r.Method != http.MethodGet {
			s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
			return
		}
		actor, ok := s.verifyProtectedOrWrite(w, r, requestID, teamID, nil, "")
		if !ok {
			return
		}
		data, err := s.store.TeamStatus(r.Context(), teamID, actor)
		if err != nil {
			s.writeStorageError(w, requestID, "", err)
			return
		}
		writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, Data: data})
	case len(parts) == 4 && parts[1] == "scopes" && parts[3] == "key-envelope":
		if r.Method != http.MethodGet {
			s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
			return
		}
		actor, ok := s.verifyProtectedOrWrite(w, r, requestID, teamID, nil, "")
		if !ok {
			return
		}
		data, err := s.store.GetKeyEnvelope(r.Context(), teamID, parts[2], actor)
		if err != nil {
			s.writeStorageError(w, requestID, "", err)
			return
		}
		writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, Data: data})
	case len(parts) == 4 && parts[1] == "scopes" && parts[3] == "pull-bundle":
		if r.Method != http.MethodGet {
			s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
			return
		}
		actor, ok := s.verifyProtectedOrWrite(w, r, requestID, teamID, nil, "")
		if !ok {
			return
		}
		data, err := s.store.PullBundle(r.Context(), teamID, parts[2], actor)
		if err != nil {
			s.writeStorageError(w, requestID, "", err)
			return
		}
		writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, Data: data})
	case len(parts) == 5 && parts[1] == "scopes" && parts[3] == "env" && parts[4] == "status":
		if r.Method != http.MethodGet {
			s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
			return
		}
		actor, ok := s.verifyProtectedOrWrite(w, r, requestID, teamID, nil, "")
		if !ok {
			return
		}
		data, err := s.store.EnvStatus(r.Context(), teamID, parts[2], actor)
		if err != nil {
			s.writeStorageError(w, requestID, "", err)
			return
		}
		writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, Data: data})
	case len(parts) == 5 && parts[1] == "scopes" && parts[3] == "env" && parts[4] == "push":
		s.handleEnvPush(w, r, requestID, teamID, parts[2])
	case len(parts) == 3 && parts[1] == "events" && parts[2] == "pull":
		s.handlePullEvent(w, r, requestID, teamID)
	case len(parts) == 3 && parts[1] == "join" && parts[2] == "invites":
		s.handleJoinInvitesList(w, r, requestID, teamID)
	case len(parts) == 5 && parts[1] == "join" && parts[2] == "invites" && parts[4] == "pin":
		s.handleInvitePINSubmit(w, r, requestID, teamID, parts[3])
	case len(parts) == 2 && parts[1] == "invites":
		if r.Method == http.MethodGet {
			s.handleAdminInvitesList(w, r, requestID, teamID)
			return
		}
		if r.Method == http.MethodPost {
			s.handleAdminInviteCreate(w, r, requestID, teamID)
			return
		}
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
	case len(parts) == 4 && parts[1] == "invites" && parts[3] == "revoke":
		s.handleAdminInviteRevoke(w, r, requestID, teamID, parts[2])
	default:
		s.writeError(w, requestID, "", newAPIError(http.StatusNotFound, "usage_error", "unknown endpoint"))
	}
}

func (s *Server) handleConfigPush(w http.ResponseWriter, r *http.Request, requestID string, teamID string) {
	if r.Method != http.MethodPost {
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
		return
	}
	body, bodyErr := readRequestBody(w, r, s.config.MaxBodyBytes)
	if bodyErr != nil {
		s.writeError(w, requestID, "", *bodyErr)
		return
	}
	var request domain.ConfigPushRequest
	if err := json.Unmarshal(body, &request); err != nil {
		s.writeError(w, requestID, "", newAPIError(http.StatusBadRequest, "usage_error", "request body must be valid JSON"))
		return
	}
	if err := request.Validate(); err != nil {
		s.writeError(w, requestID, request.OperationID, validationError(err.Error()))
		return
	}
	snapshotTeamID, err := domain.ConfigSnapshotTeamID(request.TargetConfigSnapshot)
	if err != nil {
		s.writeError(w, requestID, request.OperationID, validationError("target_config_snapshot team id could not be read"))
		return
	}
	if snapshotTeamID != teamID {
		s.writeError(w, requestID, request.OperationID, validationError("target_config_snapshot team.id must match the request team"))
		return
	}
	actor, ok := s.verifyProtectedOrWrite(w, r, requestID, teamID, body, request.OperationID)
	if !ok {
		return
	}
	result, err := s.store.PushConfig(r.Context(), teamID, actor, request)
	if err != nil {
		s.writeStorageError(w, requestID, request.OperationID, err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, OperationID: request.OperationID, Data: result})
}

func (s *Server) handleEnvPush(w http.ResponseWriter, r *http.Request, requestID string, teamID string, scope string) {
	if r.Method != http.MethodPost {
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
		return
	}
	body, bodyErr := readRequestBody(w, r, s.config.MaxBodyBytes)
	if bodyErr != nil {
		s.writeError(w, requestID, "", *bodyErr)
		return
	}
	var request domain.EnvPushRequest
	if err := json.Unmarshal(body, &request); err != nil {
		s.writeError(w, requestID, "", newAPIError(http.StatusBadRequest, "usage_error", "request body must be valid JSON"))
		return
	}
	if err := request.Validate(); err != nil {
		s.writeError(w, requestID, request.OperationID, validationError(err.Error()))
		return
	}
	actor, ok := s.verifyProtectedOrWrite(w, r, requestID, teamID, body, request.OperationID)
	if !ok {
		return
	}
	result, err := s.store.EnvPush(r.Context(), teamID, scope, actor, request)
	if err != nil {
		if errors.Is(err, storage.ErrSecretConflict) {
			apiErr := newAPIError(http.StatusConflict, "secret_version_conflict", "one or more variables changed before this env push")
			apiErr.body.Details = map[string]any{"conflicts": result.Conflicts}
			s.writeError(w, requestID, request.OperationID, apiErr)
			return
		}
		s.writeStorageError(w, requestID, request.OperationID, err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, OperationID: request.OperationID, Data: result})
}

func (s *Server) handlePullEvent(w http.ResponseWriter, r *http.Request, requestID string, teamID string) {
	if r.Method != http.MethodPost {
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
		return
	}
	body, bodyErr := readRequestBody(w, r, s.config.MaxBodyBytes)
	if bodyErr != nil {
		s.writeError(w, requestID, "", *bodyErr)
		return
	}
	var request domain.PullEventRequest
	if err := json.Unmarshal(body, &request); err != nil {
		s.writeError(w, requestID, "", newAPIError(http.StatusBadRequest, "usage_error", "request body must be valid JSON"))
		return
	}
	if err := request.Validate(); err != nil {
		s.writeError(w, requestID, "", validationError(err.Error()))
		return
	}
	actor, ok := s.verifyProtectedOrWrite(w, r, requestID, teamID, body, "")
	if !ok {
		return
	}
	result, err := s.store.RecordPullEvent(r.Context(), teamID, actor, request)
	if err != nil {
		s.writeStorageError(w, requestID, "", err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, Data: result})
}

func (s *Server) verifyProtectedOrWrite(w http.ResponseWriter, r *http.Request, requestID string, teamID string, body []byte, operationID string) (domain.Member, bool) {
	if body == nil {
		body = []byte{}
	}
	actor, err := s.verifyProtectedRequest(r, body, teamID, operationID)
	if err != nil {
		apiErr, ok := err.(apiError)
		if !ok {
			apiErr = newAPIError(http.StatusUnauthorized, "signature_invalid", err.Error())
		}
		s.writeError(w, requestID, operationID, apiErr)
		return domain.Member{}, false
	}
	return actor, true
}

func (s *Server) verifyProtectedRequest(r *http.Request, body []byte, teamID string, operationID string) (domain.Member, error) {
	metadata := signing.MetadataFromHeaders(r.Header)
	if metadata.OperationID == "" {
		metadata.OperationID = operationID
	}
	if err := metadata.Validate(operationID); err != nil {
		return domain.Member{}, newAPIError(http.StatusUnauthorized, "signature_missing", err.Error())
	}
	timestamp, err := time.Parse(time.RFC3339, metadata.Timestamp)
	if err != nil {
		return domain.Member{}, newAPIError(http.StatusUnauthorized, "clock_skew", "signature timestamp must be RFC3339")
	}
	now := s.config.Now().UTC()
	if timestamp.Before(now.Add(-s.config.RequestSkew)) || timestamp.After(now.Add(s.config.RequestSkew)) {
		apiErr := newAPIError(http.StatusUnauthorized, "clock_skew", "signature timestamp is outside the accepted window")
		apiErr.body.Details = map[string]any{"server_time": now.Format(time.RFC3339)}
		return domain.Member{}, apiErr
	}
	actor, err := s.store.GetMember(r.Context(), teamID, metadata.PublicKeySHA)
	if err != nil {
		if errors.Is(err, storage.ErrPermissionDenied) || errors.Is(err, storage.ErrNotFound) {
			return domain.Member{}, newAPIError(http.StatusForbidden, "permission_denied", "active team membership is required")
		}
		apiErr := newAPIError(http.StatusServiceUnavailable, "cloud_unavailable", "team membership could not be loaded")
		apiErr.body.Retryable = true
		return domain.Member{}, apiErr
	}
	publicKey, err := signing.ParseOpenSSHEd25519PublicKey(actor.SigningPublicKey)
	if err != nil {
		return domain.Member{}, newAPIError(http.StatusUnauthorized, "signature_invalid", "member signing public key is invalid")
	}
	canonical := signing.Canonical(r.Method, r.URL.Path, r.URL.RawQuery, body, metadata)
	if err := signing.Verify(publicKey, canonical, metadata.Signature); err != nil {
		return domain.Member{}, newAPIError(http.StatusUnauthorized, "signature_invalid", "request signature could not be verified")
	}
	expiresAt := now.Add(s.config.RequestSkew + time.Minute)
	if err := s.store.ReserveNonce(r.Context(), metadata.PublicKeySHA, metadata.Nonce, expiresAt); err != nil {
		if errors.Is(err, storage.ErrReplayRejected) {
			return domain.Member{}, newAPIError(http.StatusUnauthorized, "replay_rejected", "request nonce was already used")
		}
		apiErr := newAPIError(http.StatusServiceUnavailable, "cloud_unavailable", "request nonce could not be reserved")
		apiErr.body.Retryable = true
		return domain.Member{}, apiErr
	}
	return actor, nil
}

func (s *Server) writeStorageError(w http.ResponseWriter, requestID string, operationID string, err error) {
	switch {
	case errors.Is(err, storage.ErrPermissionDenied):
		s.writeError(w, requestID, operationID, newAPIError(http.StatusForbidden, "permission_denied", "the current identity is not allowed to perform this operation"))
	case errors.Is(err, storage.ErrNotFound):
		s.writeError(w, requestID, operationID, newAPIError(http.StatusNotFound, "team_not_found", "requested team or scope was not found"))
	case errors.Is(err, storage.ErrRevisionConflict):
		s.writeError(w, requestID, operationID, newAPIError(http.StatusConflict, "revision_conflict", "cloud config revision does not match the expected revision"))
	case errors.Is(err, storage.ErrIdempotencyConflict):
		s.writeError(w, requestID, operationID, newAPIError(http.StatusConflict, "idempotency_conflict", "operation_id was reused with a different payload"))
	case errors.Is(err, storage.ErrSecretConflict):
		s.writeError(w, requestID, operationID, newAPIError(http.StatusConflict, "secret_version_conflict", "one or more variables changed before this write"))
	case errors.Is(err, storage.ErrInviteNotActive):
		s.writeError(w, requestID, operationID, newAPIError(http.StatusConflict, "invite_not_active", "invite is not in a state that allows this operation"))
	default:
		apiErr := newAPIError(http.StatusInternalServerError, "server_error", "request could not be completed")
		apiErr.body.Retryable = true
		s.writeError(w, requestID, operationID, apiErr)
	}
}

func (s *Server) writeError(w http.ResponseWriter, requestID string, operationID string, err apiError) {
	writeJSON(w, err.status, Envelope{
		OK:          false,
		RequestID:   requestID,
		OperationID: operationID,
		Error:       &err.body,
	})
}

func readRequestBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, *apiError) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		apiErr := newAPIError(http.StatusRequestEntityTooLarge, "usage_error", "request body is too large")
		return nil, &apiErr
	}
	defer r.Body.Close()
	return body, nil
}

func teamPathParts(path string) ([]string, error) {
	rest := strings.TrimPrefix(path, "/v1/teams/")
	if rest == path || rest == "" {
		return nil, fmt.Errorf("invalid team route")
	}
	rawParts := strings.Split(rest, "/")
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		if part == "" {
			return nil, fmt.Errorf("invalid empty path part")
		}
		decoded, err := url.PathUnescape(part)
		if err != nil {
			return nil, err
		}
		parts = append(parts, decoded)
	}
	if len(parts) == 0 || parts[0] == "" {
		return nil, fmt.Errorf("missing team id")
	}
	return parts, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func requestID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	return "req_" + hex.EncodeToString(raw[:])
}
