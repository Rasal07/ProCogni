// Package httpx holds the HTTP plumbing shared by every domain package:
// JSON encoding, the error envelope, request ids and panic recovery.
//
// It exists so that teacher/ and parent/ never need to import one another.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
)

const MaxBodyBytes = 1 << 20 // 1 MiB

// Error is the single error shape the API returns. Messages are deliberately
// terse: they are read by clients, not by operators. Operators read the logs.
type Error struct {
	Status  int            `json:"-"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// WithDetails attaches structured context to a copy of e, so callers can say
// which of 40 bulk rows was the problem without inventing an error shape.
func (e *Error) WithDetails(d map[string]any) *Error {
	c := *e
	c.Details = d
	return &c
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func Errorf(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

// Canonical errors. NotFound is used in places where Forbidden would be more
// literally accurate — see the parent authorization boundary, where confirming
// that a student exists is itself a disclosure.
var (
	ErrUnauthorized     = Errorf(http.StatusUnauthorized, "unauthorized", "authentication required")
	ErrForbidden        = Errorf(http.StatusForbidden, "forbidden", "not permitted")
	ErrNotFound         = Errorf(http.StatusNotFound, "not_found", "not found")
	ErrMethodNotAllowed = Errorf(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	ErrBadRequest       = Errorf(http.StatusBadRequest, "bad_request", "malformed request")
	ErrConsentRequired  = Errorf(http.StatusForbidden, "consent_required", "consent has not been granted for this data")
	ErrRateLimited      = Errorf(http.StatusTooManyRequests, "rate_limited", "too many requests")
	ErrInternal         = Errorf(http.StatusInternalServerError, "internal", "internal error")
)

func Invalid(msg string) *Error { return Errorf(http.StatusBadRequest, "invalid", msg) }

// Handler is a http.HandlerFunc that may return an error, so handlers can
// `return err` instead of remembering to write-and-return.
type Handler func(http.ResponseWriter, *http.Request) error

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h(w, r); err != nil {
		WriteError(w, r, err)
	}
}

func JSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return nil
	}
	return json.NewEncoder(w).Encode(v)
}

func NoContent(w http.ResponseWriter) error {
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	if !errors.As(err, &e) {
		slog.ErrorContext(r.Context(), "unhandled handler error",
			"err", err, "request_id", RequestID(r.Context()), "path", r.URL.Path)
		e = ErrInternal
	}
	body := struct {
		Error     *Error `json:"error"`
		RequestID string `json:"request_id"`
	}{e, RequestID(r.Context())}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(body)
}

// Enveloped serves mux, replacing its own text/plain 404 and 405 answers with
// the error envelope, so a client has exactly one error shape to parse.
func Enveloped(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		// No route: run the mux's stock answer against a sink to learn whether
		// it is a 404 or a 405, and to pick up the Allow header.
		sink := &statusSink{header: http.Header{}}
		h.ServeHTTP(sink, r)
		if sink.status == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", sink.header.Get("Allow"))
			WriteError(w, r, ErrMethodNotAllowed)
			return
		}
		WriteError(w, r, ErrNotFound)
	})
}

type statusSink struct {
	header http.Header
	status int
}

func (s *statusSink) Header() http.Header         { return s.header }
func (s *statusSink) Write(b []byte) (int, error) { return len(b), nil }
func (s *statusSink) WriteHeader(code int)        { s.status = code }

// Decode reads a JSON body, rejecting unknown fields so that a client typo is
// a 400 rather than a silently ignored value.
func Decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return Invalid("request body is not valid JSON for this endpoint")
	}
	return nil
}
