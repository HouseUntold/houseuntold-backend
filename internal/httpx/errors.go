package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type AppError struct {
	Status  int
	Message string
	Err     error
	Details map[string]any
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *AppError) Unwrap() error {
	return e.Err
}

func NewBadRequest(msg string, err error) *AppError {
	return &AppError{Status: http.StatusBadRequest, Message: msg, Err: err}
}

func NewUnauthorized(msg string, err error) *AppError {
	return &AppError{Status: http.StatusUnauthorized, Message: msg, Err: err}
}

func NewForbidden(msg string) *AppError {
	return &AppError{Status: http.StatusForbidden, Message: msg}
}

func NewNotFound(msg string) *AppError {
	return &AppError{Status: http.StatusNotFound, Message: msg}
}

func NewConflict(msg string, err error) *AppError {
	return &AppError{Status: http.StatusConflict, Message: msg, Err: err}
}

// ErrorPayload matches exact backend error contract: {"error": "message"}
type ErrorPayload struct {
	Error   string         `json:"error"`
	Details map[string]any `json:"details,omitempty"`
}

// HandleError translates domain, pgx, and context timeout errors into JSON once at the handler boundary.
func HandleError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	if logger == nil {
		logger = slog.Default()
	}

	var appErr *AppError
	if errors.As(err, &appErr) {
		if appErr.Status >= 500 {
			logger.ErrorContext(r.Context(), "internal error", "path", r.URL.Path, "error", err)
		} else {
			logger.WarnContext(r.Context(), "request rejected", "path", r.URL.Path, "status", appErr.Status, "error", err)
		}
		_ = WriteJSON(w, appErr.Status, ErrorPayload{Error: appErr.Message, Details: appErr.Details})
		return
	}

	if errors.Is(err, context.DeadlineExceeded) {
		logger.ErrorContext(r.Context(), "request timed out", "path", r.URL.Path, "error", err)
		_ = WriteJSON(w, http.StatusGatewayTimeout, ErrorPayload{Error: "database or upstream request timed out"})
		return
	}

	if errors.Is(err, pgx.ErrNoRows) {
		logger.WarnContext(r.Context(), "resource not found", "path", r.URL.Path, "error", err)
		_ = WriteJSON(w, http.StatusNotFound, ErrorPayload{Error: "resource not found"})
		return
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			logger.WarnContext(r.Context(), "unique constraint violation", "path", r.URL.Path, "error", err)
			_ = WriteJSON(w, http.StatusConflict, ErrorPayload{Error: "resource already exists"})
			return
		case "23503", "23514":
			logger.WarnContext(r.Context(), "constraint violation", "path", r.URL.Path, "error", err)
			_ = WriteJSON(w, http.StatusBadRequest, ErrorPayload{Error: "invalid domain reference or constraint"})
			return
		}
	}

	logger.ErrorContext(r.Context(), "unhandled request error", "path", r.URL.Path, "error", err)
	_ = WriteJSON(w, http.StatusInternalServerError, ErrorPayload{Error: "an unexpected server error occurred"})
}
