package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"propagate/backend/internal/domain"
	"propagate/backend/internal/signing"
	"propagate/backend/internal/storage"
)

func (s *Server) handleJoinInvitesList(w http.ResponseWriter, r *http.Request, requestID string, teamID string) {
	if r.Method != http.MethodGet {
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
		return
	}
	data, err := s.store.ListJoinerInvites(r.Context(), teamID)
	if err != nil {
		s.writeStorageError(w, requestID, "", err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, Data: data})
}

func (s *Server) handleInvitePINSubmit(w http.ResponseWriter, r *http.Request, requestID string, teamID string, inviteID string) {
	if r.Method != http.MethodPost {
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
		return
	}
	body, bodyErr := readRequestBody(w, r, s.config.MaxBodyBytes)
	if bodyErr != nil {
		s.writeError(w, requestID, "", *bodyErr)
		return
	}
	var request domain.InvitePINRequest
	if err := json.Unmarshal(body, &request); err != nil {
		s.writeError(w, requestID, "", newAPIError(http.StatusBadRequest, "usage_error", "request body must be valid JSON"))
		return
	}
	if err := request.Validate(); err != nil {
		s.writeError(w, requestID, request.OperationID, validationError(err.Error()))
		return
	}
	if err := s.verifyInvitePINSignature(r, body, request); err != nil {
		apiErr, ok := err.(apiError)
		if !ok {
			apiErr = newAPIError(http.StatusUnauthorized, "signature_invalid", err.Error())
		}
		s.writeError(w, requestID, request.OperationID, apiErr)
		return
	}
	serverTime := s.config.Now().UTC().Format(time.RFC3339)
	result, err := s.store.SubmitInvitePIN(r.Context(), teamID, inviteID, request, serverTime)
	if err != nil {
		s.writeInvitePINError(w, requestID, request.OperationID, err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, OperationID: request.OperationID, Data: result})
}

func (s *Server) verifyInvitePINSignature(r *http.Request, body []byte, request domain.InvitePINRequest) error {
	metadata := signing.MetadataFromHeaders(r.Header)
	if metadata.OperationID == "" {
		metadata.OperationID = request.OperationID
	}
	if err := metadata.Validate(request.OperationID); err != nil {
		return newAPIError(http.StatusUnauthorized, "signature_missing", err.Error())
	}
	if metadata.PublicKeySHA != request.Joiner.PublicKeySHA {
		return newAPIError(http.StatusUnauthorized, "signature_invalid", "signature identity does not match joiner.public_key_sha")
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
	publicKey, err := signing.ParseOpenSSHEd25519PublicKey(request.Joiner.SigningPublicKey)
	if err != nil {
		return newAPIError(http.StatusUnauthorized, "signature_invalid", "joiner signing public key is invalid")
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

func (s *Server) handleAdminInviteCreate(w http.ResponseWriter, r *http.Request, requestID string, teamID string) {
	if r.Method != http.MethodPost {
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
		return
	}
	body, bodyErr := readRequestBody(w, r, s.config.MaxBodyBytes)
	if bodyErr != nil {
		s.writeError(w, requestID, "", *bodyErr)
		return
	}
	var request domain.CreateTeamInviteRequest
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
	if !domain.MemberCanManage(actor) {
		s.writeError(w, requestID, request.OperationID, newAPIError(http.StatusForbidden, "permission_denied", "management access is required"))
		return
	}
	result, err := s.store.CreateTeamInvite(r.Context(), teamID, actor, request)
	if err != nil {
		s.writeStorageError(w, requestID, request.OperationID, err)
		return
	}
	writeJSON(w, http.StatusCreated, Envelope{OK: true, RequestID: requestID, OperationID: request.OperationID, Data: result})
}

func (s *Server) handleAdminInvitesList(w http.ResponseWriter, r *http.Request, requestID string, teamID string) {
	if r.Method != http.MethodGet {
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
		return
	}
	actor, ok := s.verifyProtectedOrWrite(w, r, requestID, teamID, nil, "")
	if !ok {
		return
	}
	if !domain.MemberCanManage(actor) {
		s.writeError(w, requestID, "", newAPIError(http.StatusForbidden, "permission_denied", "management access is required"))
		return
	}
	data, err := s.store.ListAdminInvites(r.Context(), teamID, actor)
	if err != nil {
		s.writeStorageError(w, requestID, "", err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, Data: data})
}

func (s *Server) handleAdminInviteRevoke(w http.ResponseWriter, r *http.Request, requestID string, teamID string, inviteID string) {
	if r.Method != http.MethodPost {
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
		return
	}
	body, bodyErr := readRequestBody(w, r, s.config.MaxBodyBytes)
	if bodyErr != nil {
		s.writeError(w, requestID, "", *bodyErr)
		return
	}
	actor, ok := s.verifyProtectedOrWrite(w, r, requestID, teamID, body, "")
	if !ok {
		return
	}
	if !domain.MemberCanManage(actor) {
		s.writeError(w, requestID, "", newAPIError(http.StatusForbidden, "permission_denied", "management access is required"))
		return
	}
	if err := s.store.RevokeTeamInvite(r.Context(), teamID, inviteID, actor); err != nil {
		s.writeStorageError(w, requestID, "", err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, Data: map[string]string{"status": "revoked", "invite_id": inviteID}})
}

func (s *Server) writeInvitePINError(w http.ResponseWriter, requestID string, operationID string, err error) {
	switch {
	case errors.Is(err, storage.ErrInvitePINInvalid):
		s.writeError(w, requestID, operationID, newAPIError(http.StatusUnauthorized, "invite_pin_invalid", "invite pin does not match"))
	case errors.Is(err, storage.ErrInviteLocked):
		s.writeError(w, requestID, operationID, newAPIError(http.StatusForbidden, "invite_locked", "invite pin attempts exhausted"))
	case errors.Is(err, storage.ErrInviteNotActive):
		s.writeError(w, requestID, operationID, newAPIError(http.StatusConflict, "invite_not_active", "invite cannot accept a pin in its current state"))
	default:
		s.writeStorageError(w, requestID, operationID, err)
	}
}
