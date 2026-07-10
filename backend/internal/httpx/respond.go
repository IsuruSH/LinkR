// Package httpx owns the wire format: the error envelope, the domain-error to
// HTTP-status mapping, and JSON encoding. It is the only package that decides
// what status code a failure deserves. Handlers return domain errors and call
// Error(); they never write a status code by hand, which is what keeps the API
// consistent as it grows.
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/IsuruSh/linkr/internal/domain"
)

// errorEnvelope is the documented failure shape:
//
//	{"error": {"code": "...", "message": "...", "details": {...}}}
//
// Success responses deliberately do NOT get an envelope — they return the
// resource directly, so clients do not unwrap `data` on every call.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    domain.Code       `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

// statusByCode is the one place a domain error becomes an HTTP status.
var statusByCode = map[domain.Code]int{
	domain.CodeValidation:         http.StatusBadRequest,
	domain.CodeInvalidURL:         http.StatusBadRequest,
	domain.CodeInvalidAlias:       http.StatusBadRequest,
	domain.CodeInvalidCursor:      http.StatusBadRequest,
	domain.CodeReservedAlias:      http.StatusBadRequest,
	domain.CodeUnauthorized:       http.StatusUnauthorized,
	domain.CodeInvalidCredentials: http.StatusUnauthorized,
	domain.CodeNotFound:           http.StatusNotFound,
	domain.CodeMethodNotAllowed:   http.StatusMethodNotAllowed,
	domain.CodeLinkNotFound:       http.StatusNotFound,
	domain.CodeAliasTaken:         http.StatusConflict,
	domain.CodeEmailTaken:         http.StatusConflict,
	domain.CodeInternal:           http.StatusInternalServerError,
	domain.CodeCodeGeneration:     http.StatusInternalServerError,
}

// JSON writes a success response. The resource is returned directly.
func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already on the wire, so there is nothing useful to
		// send the client. Log it and move on.
		slog.Error("encoding response body", "error", err)
	}
}

// Error maps err onto the envelope. Anything that is not a *domain.Error is
// treated as a bug: the client gets a generic 500 and the real error goes to the
// log, so we never leak a driver message or a query fragment to the internet.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	var derr *domain.Error
	if !errors.As(err, &derr) {
		slog.ErrorContext(r.Context(), "unhandled error", "error", err, "path", r.URL.Path)
		writeEnvelope(w, http.StatusInternalServerError, errorBody{
			Code:    domain.CodeInternal,
			Message: "an unexpected error occurred",
		})
		return
	}

	status, ok := statusByCode[derr.Code]
	if !ok {
		status = http.StatusInternalServerError
	}
	if status >= http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "server error", "error", derr, "code", derr.Code, "path", r.URL.Path)
	}

	writeEnvelope(w, status, errorBody{
		Code:    derr.Code,
		Message: derr.Message,
		Details: derr.Details,
	})
}

func writeEnvelope(w http.ResponseWriter, status int, body errorBody) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: body})
}

// Decode reads a JSON request body, rejecting unknown fields so a typo'd field
// name fails loudly instead of being silently ignored.
func Decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return domain.WrapError(err, domain.CodeValidation, "request body is not valid JSON")
	}
	return nil
}
