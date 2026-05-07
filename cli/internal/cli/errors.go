package cli

import (
	"encoding/json"
	"fmt"
	"io"
)

const (
	ExitSuccess              = 0
	ExitInternalError        = 1
	ExitUsageError           = 2
	ExitValidationError      = 3
	ExitPermissionDenied     = 4
	ExitCloudUnavailable     = 5
	ExitConflict             = 6
	ExitUserCanceled         = 7
	ExitConfirmationRequired = 8
	ExitPartialLocalFailure  = 9
)

type CommandError struct {
	Code      int      `json:"exit_code"`
	Symbol    string   `json:"code"`
	Message   string   `json:"message"`
	Retryable bool     `json:"retryable"`
	NextSteps []string `json:"next_steps,omitempty"`
	Err       error    `json:"-"`
}

func (e *CommandError) Error() string {
	if e.Err == nil {
		return e.Message
	}
	return e.Message + ": " + e.Err.Error()
}

func commandError(code int, symbol, message string, err error, nextSteps ...string) *CommandError {
	return &CommandError{
		Code:      code,
		Symbol:    symbol,
		Message:   message,
		Retryable: code == ExitCloudUnavailable,
		NextSteps: nextSteps,
		Err:       err,
	}
}

func renderError(w io.Writer, jsonOutput bool, err error) int {
	cmdErr, ok := err.(*CommandError)
	if !ok {
		cmdErr = commandError(ExitInternalError, "internal_error", "Unexpected internal error", err)
	}
	if jsonOutput {
		payload := map[string]any{
			"ok":     false,
			"status": "failed",
			"error":  cmdErr,
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(payload)
	} else {
		fmt.Fprintf(w, "Error: %s\n", cmdErr.Message)
		if cmdErr.Err != nil {
			fmt.Fprintf(w, "Detail: %s\n", cmdErr.Err)
		}
		for i, step := range cmdErr.NextSteps {
			if i == 0 {
				fmt.Fprintln(w, "\nNext steps:")
			}
			fmt.Fprintf(w, "%d. %s\n", i+1, step)
		}
	}
	return cmdErr.Code
}
