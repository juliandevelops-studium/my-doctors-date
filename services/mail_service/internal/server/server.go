// Package server is the HTTP side of mail_service: service-token authentication, the template allow list per caller,
// request validation, rate limits and the hand-over to SMTP. Never routed by Traefik (decision 16).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/go-chi/chi/v5"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/api"
	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/policy"
	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/rateLimit"
	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/templates"
)

const (
	maxBody             = 16 << 10
	defaultPerRecipient = 10  // mails per minute and recipient: a compromised service cannot flood one mailbox
	defaultPerCaller    = 300 // mails per minute and calling service
)

// Mailer hands a finished message to the SMTP server. smtpout.Sender implements it.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) (messageID string, err error)
}

// Deps are the collaborators of the server.
type Deps struct {
	Verifier *praxisauth.ServiceVerifier
	Renderer *templates.Renderer
	Mailer   Mailer
	// LinkBase is the public frontend address. A link in a mail must start with it (a compromised caller cannot
	// send phishing links from the practice's address).
	LinkBase string
	Practice string
	Log      *slog.Logger

	PerRecipientPerMinute, PerCallerPerMinute int
}

// Server implements api.StrictServerInterface.
type Server struct {
	Deps
	byRecipient *ratelimit.Limiter
	byCaller    *ratelimit.Limiter
}

func init() {
	openapi3.DefineStringFormatValidator("email", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForEmail))
	openapi3.DefineStringFormatValidator("uuid", openapi3.NewRegexpFormatValidator(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`))
	openapi3.DefineStringFormatValidator("uri", openapi3.NewRegexpFormatValidator(`^https?://[^\s]+$`))
}

// New creates the server state.
func New(d Deps) *Server {
	or := func(v, def int) int {
		if v == 0 {
			return def
		}
		return v
	}
	return &Server{Deps: d,
		byRecipient: ratelimit.New(or(d.PerRecipientPerMinute, defaultPerRecipient), time.Minute),
		byCaller:    ratelimit.New(or(d.PerCallerPerMinute, defaultPerCaller), time.Minute)}
}

type ctxKey struct{}

// Handler builds the HTTP handler: log -> service-token auth -> body limit -> content type -> validation -> handler.
func (s *Server) Handler() (http.Handler, error) {
	spec, err := api.GetSwagger()
	if err != nil {
		return nil, err
	}
	spec.Servers = nil
	validator := nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error { return nil }, // done by authenticate
		},
		ErrorHandlerWithOpts: func(_ context.Context, _ error, w http.ResponseWriter, _ *http.Request, _ nethttpmiddleware.ErrorHandlerOpts) {
			writeProblem(w, http.StatusUnprocessableEntity, "validation_failed", "Request validation failed")
		},
	})
	strict := api.NewStrictHandlerWithOptions(s, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeProblem(w, http.StatusBadRequest, "bad_request", "Malformed request")
		},
		ResponseErrorHandlerFunc: s.errorHandler,
	})
	r := chi.NewRouter()
	r.Use(s.accessLog)
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusNotFound, "not_found", "Not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	})
	r.Group(func(r chi.Router) {
		r.Use(s.authenticate, limitBody, requireJSON, validator)
		api.HandlerFromMux(strict, r)
	})
	return r, nil
}

// authenticate verifies the service token and that its issuer is a caller of the route.
func (s *Server) authenticate(next http.Handler) http.Handler {
	callers := map[string]bool{}
	for _, c := range policy.Routes["sendMail"].Callers {
		callers[c] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || raw == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
			return
		}
		c, err := s.Verifier.Verify(raw)
		if err != nil {
			s.Log.Warn("service token rejected", "reason", err.Error())
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
			return
		}
		if !callers[c.Issuer] {
			s.Log.Warn("caller not allowed", "caller", c.Issuer)
			writeProblem(w, http.StatusForbidden, "forbidden", "Not allowed")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c.Issuer)))
	})
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

func requireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeProblem(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

// accessLog logs method, path, status and duration. Recipients and bodies are never logged.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/healthz" {
			s.Log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
		}
	})
}

type apiError struct {
	status      int
	code, title string
	retryAfter  time.Duration
}

func (e *apiError) Error() string { return e.code }

func (s *Server) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	if ae, ok := errors.AsType[*apiError](err); ok {
		if ae.retryAfter > 0 {
			w.Header().Set("Retry-After", itoa(int(ae.retryAfter.Seconds())+1))
		}
		writeProblem(w, ae.status, ae.code, ae.title)
		return
	}
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		writeProblem(w, http.StatusRequestEntityTooLarge, "too_large", "Request body too large")
		return
	}
	s.Log.Error("internal error", "path", r.URL.Path, "error", err.Error())
	writeProblem(w, http.StatusInternalServerError, "internal_error", "Internal error")
}

func itoa(n int) string {
	if n <= 0 {
		return "1"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func writeProblem(w http.ResponseWriter, status int, code, title string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.Problem{Type: "about:blank", Title: title, Status: status, Code: code})
}

// MailServiceHealth is the liveness probe.
func (s *Server) MailServiceHealth(context.Context, api.MailServiceHealthRequestObject) (api.MailServiceHealthResponseObject, error) {
	return api.MailServiceHealth200Response{}, nil
}
