package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

type fieldError struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

// apiError is an answer the service decided to give. Handlers return it; errorHandler writes it as problem+json.
type apiError struct {
	Status     int
	Code       string
	Title      string
	Errors     []fieldError
	RetryAfter time.Duration
}

func (e *apiError) Error() string { return e.Code }

func newErr(status int, code, title string) *apiError {
	return &apiError{Status: status, Code: code, Title: title}
}

// invalidCredentials is the one neutral answer for every credential failure (UF-02, CONVENTIONS).
func invalidCredentials() *apiError {
	return newErr(http.StatusUnauthorized, "invalid_credentials", "Invalid credentials")
}

func unauthorized() *apiError {
	return newErr(http.StatusUnauthorized, "unauthorized", "Authentication required")
}
func forbidden() *apiError { return newErr(http.StatusForbidden, "forbidden", "Not allowed") }
func notFound() *apiError  { return newErr(http.StatusNotFound, "not_found", "Not found") }

func conflict(code, title string) *apiError { return newErr(http.StatusConflict, code, title) }

func unprocessable(code, title string) *apiError {
	return newErr(http.StatusUnprocessableEntity, code, title)
}

func validation(field, code string) *apiError {
	e := newErr(http.StatusUnprocessableEntity, "validation_failed", "Validation failed")
	e.Errors = []fieldError{{Field: field, Code: code}}
	return e
}

func tooMany(retry time.Duration) *apiError {
	e := newErr(http.StatusTooManyRequests, "rate_limited", "Too many requests")
	e.RetryAfter = retry
	return e
}

type problem struct {
	Type   string       `json:"type"`
	Title  string       `json:"title"`
	Status int          `json:"status"`
	Code   string       `json:"code"`
	Errors []fieldError `json:"errors,omitempty"`
}

func writeProblem(w http.ResponseWriter, e *apiError) {
	w.Header().Set("Content-Type", "application/problem+json")
	if e.Status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(e.RetryAfter.Seconds())+1))
	}
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(problem{Type: "about:blank", Title: e.Title, Status: e.Status, Code: e.Code, Errors: e.Errors})
}

// errorHandler writes handler errors. Anything that is not an apiError is an internal error: it is logged, and the
// client only sees a generic answer (no SQL, no hostnames, no stack traces).
func errorHandler(log *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		var ae *apiError
		if errors.As(err, &ae) {
			writeProblem(w, ae)
			return
		}
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeProblem(w, newErr(http.StatusRequestEntityTooLarge, "too_large", "Request body too large"))
			return
		}
		id, _ := identityFrom(r.Context())
		log.Error("internal error", "path", r.URL.Path, "user", id.UserID.String(), "error", err.Error())
		writeProblem(w, newErr(http.StatusInternalServerError, "internal_error", "Internal error"))
	}
}
