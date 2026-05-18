package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"propagate/backend/internal/domain"
	"propagate/backend/internal/storage"
)

func (s *Server) handleJoinRequestCreate(w http.ResponseWriter, r *http.Request, requestID string, teamID string) {
	if r.Method != http.MethodPost {
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
		return
	}
	body, bodyErr := readRequestBody(w, r, s.config.MaxBodyBytes)
	if bodyErr != nil {
		s.writeError(w, requestID, "", *bodyErr)
		return
	}
	var request domain.JoinRequestSubmission
	if err := json.Unmarshal(body, &request); err != nil {
		s.writeError(w, requestID, "", newAPIError(http.StatusBadRequest, "usage_error", "request body must be valid JSON"))
		return
	}
	if err := request.Validate(); err != nil {
		s.writeError(w, requestID, request.OperationID, validationError(err.Error()))
		return
	}
	if err := s.verifyJoinerSignature(r, body, request.Joiner, request.OperationID); err != nil {
		apiErr, ok := err.(apiError)
		if !ok {
			apiErr = newAPIError(http.StatusUnauthorized, "signature_invalid", err.Error())
		}
		s.writeError(w, requestID, request.OperationID, apiErr)
		return
	}

	serverTime := s.config.Now().UTC().Format("2006-01-02T15:04:05Z07:00")
	err := s.store.CreateJoinRequest(r.Context(), teamID, request, serverTime)
	if err != nil {
		if errors.Is(err, storage.ErrJoinRequestDuplicate) {
			s.writeError(w, requestID, request.OperationID, newAPIError(http.StatusConflict, "duplicate_join_request", "a join request already exists for this identity"))
			return
		}
		s.writeStorageError(w, requestID, request.OperationID, err)
		return
	}
	writeJSON(w, http.StatusCreated, Envelope{OK: true, RequestID: requestID, OperationID: request.OperationID, Data: map[string]any{"status": "pending"}})
}

func (s *Server) handleJoinRequestsList(w http.ResponseWriter, r *http.Request, requestID string, teamID string) {
	if r.Method != http.MethodGet {
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
		return
	}
	actor, ok := s.verifyProtectedOrWrite(w, r, requestID, teamID, nil, "")
	if !ok {
		return
	}
	data, err := s.store.ListPendingJoinRequests(r.Context(), teamID, actor)
	if err != nil {
		s.writeStorageError(w, requestID, "", err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, Data: data})
}

func (s *Server) handleJoinRequestApprove(w http.ResponseWriter, r *http.Request, requestID string, teamID string, publicKeySHA string) {
	if r.Method != http.MethodPost {
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
		return
	}
	body, bodyErr := readRequestBody(w, r, s.config.MaxBodyBytes)
	if bodyErr != nil {
		s.writeError(w, requestID, "", *bodyErr)
		return
	}
	var request domain.ApproveJoinRequestBody
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
	result, err := s.store.ApproveJoinRequest(r.Context(), teamID, publicKeySHA, actor, request)
	if err != nil {
		if errors.Is(err, storage.ErrJoinRequestNotFound) {
			s.writeError(w, requestID, request.OperationID, newAPIError(http.StatusNotFound, "join_request_not_found", "no pending join request found for this identity"))
			return
		}
		s.writeStorageError(w, requestID, request.OperationID, err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, OperationID: request.OperationID, Data: result})
}

func (s *Server) handleJoinRequestDecline(w http.ResponseWriter, r *http.Request, requestID string, teamID string, publicKeySHA string) {
	if r.Method != http.MethodPost {
		s.writeError(w, requestID, "", newAPIError(http.StatusMethodNotAllowed, "usage_error", "method not allowed"))
		return
	}
	body, bodyErr := readRequestBody(w, r, s.config.MaxBodyBytes)
	if bodyErr != nil {
		s.writeError(w, requestID, "", *bodyErr)
		return
	}
	var request domain.DeclineJoinRequestBody
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
	err := s.store.DeclineJoinRequest(r.Context(), teamID, publicKeySHA, actor, request)
	if err != nil {
		if errors.Is(err, storage.ErrJoinRequestNotFound) {
			s.writeError(w, requestID, request.OperationID, newAPIError(http.StatusNotFound, "join_request_not_found", "no pending join request found for this identity"))
			return
		}
		s.writeStorageError(w, requestID, request.OperationID, err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{OK: true, RequestID: requestID, OperationID: request.OperationID, Data: map[string]any{"status": "declined"}})
}

func (s *Server) verifyJoinerSignature(r *http.Request, body []byte, joiner domain.PublicIdentity, operationID string) error {
	return s.verifyInvitePINSignature(r, body, domain.InvitePINRequest{
		OperationID: operationID,
		Joiner:      joiner,
	})
}
