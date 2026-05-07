package api

import (
	"encoding/json"
	"net/http"
)

type Envelope struct {
	OK          bool       `json:"ok"`
	RequestID   string     `json:"request_id"`
	OperationID string     `json:"operation_id,omitempty"`
	Data        any        `json:"data,omitempty"`
	Error       *ErrorBody `json:"error,omitempty"`
	Warnings    []string   `json:"warnings,omitempty"`
}

type ErrorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
	NextSteps []string       `json:"next_steps,omitempty"`
}

type apiError struct {
	status    int
	body      ErrorBody
	operation string
}

func (e apiError) Error() string {
	return e.body.Message
}

func writeJSON(w http.ResponseWriter, status int, envelope Envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(envelope)
}

func newAPIError(status int, code string, message string) apiError {
	return apiError{
		status: status,
		body: ErrorBody{
			Code:    code,
			Message: message,
		},
	}
}

func validationError(message string) apiError {
	return newAPIError(http.StatusUnprocessableEntity, "validation_failed", message)
}
