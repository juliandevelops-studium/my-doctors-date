// Package server wires the HTTP side of database_middleware: service-token authentication, the per-operation
// allow list from the OpenAPI spec (x-allowed-callers), request validation, error mapping and logging.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/go-chi/chi/v5"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/database_middleware/internal/api"
	"github.com/juliandevelops-studium/my-doctors-date/services/database_middleware/internal/store"
)

const (
	opPrefix       = "/v1/ops/"
	defaultMaxBody = 64 << 10  // 64 KiB
	importMaxBody  = 512 << 10 // 512 KiB
)

// Handler implements api.StrictServerInterface.
type Handler struct {
	s *store.Store
}

// NewHandler creates the handler set for a store.
func NewHandler(s *store.Store) *Handler { return &Handler{s: s} }

// AllowList maps an operation name to the services allowed to call it (x-allowed-callers of the spec).
type AllowList map[string]map[string]bool

// LoadAllowList reads the allow list from the embedded OpenAPI spec.
// See docs/api/database_middleware.yaml as reference.
func LoadAllowList(spec *openapi3.T) AllowList {
	al := AllowList{}
	for path, item := range spec.Paths.Map() {
		if !strings.HasPrefix(path, opPrefix) || item.Post == nil {
			continue
		}
		callers := map[string]bool{}
		if raw, ok := item.Post.Extensions["x-allowed-callers"]; ok {
			var list []string
			if b, err := json.Marshal(raw); err == nil && json.Unmarshal(b, &list) == nil {
				for _, c := range list {
					callers[c] = true
				}
			}
		}
		al[strings.TrimPrefix(path, opPrefix)] = callers
	}
	return al
}

type ctxKey struct{}

// Caller is the authenticated service behind a request.
type Caller struct {
	Service string
	JTI     string
	Act     string
}

// CallerFrom returns the caller stored by the auth middleware.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(ctxKey{}).(Caller)
	return c, ok
}

// New builds the HTTP handler: /healthz is open, everything under /v1/ops/ needs a valid service token that
// is allowed for the operation (deny by default).
func New(h *Handler, v *praxisauth.ServiceVerifier, log *slog.Logger) (http.Handler, error) {
	spec, err := api.GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("load embedded spec: %w", err)
	}
	spec.Servers = nil // match any host
	allow := LoadAllowList(spec)

	validator := nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			// Authentication is done by authenticate() before the validator runs.
			AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error { return nil },
		},
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, _ nethttpmiddleware.ErrorHandlerOpts) {
			// The validation message names fields, never values. Details stay in the log of the caller side.
			_ = err
			writeProblem(w, http.StatusUnprocessableEntity, "validation_failed", "Request validation failed")
		},
	})

	strict := api.NewStrictHandlerWithOptions(h, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeProblem(w, http.StatusBadRequest, "bad_request", "Malformed request")
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) { errorHandler(log, w, r, err) },
	})

	r := chi.NewRouter()
	r.Use(accessLog(log))
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusNotFound, "not_found", "Not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	})
	r.Group(func(r chi.Router) {
		r.Use(authenticate(v, allow, log))
		r.Use(limitBody)
		r.Use(requireJSON)
		r.Use(validator)
		api.HandlerFromMux(strict, r)
	})
	return r, nil
}

func authenticate(v *praxisauth.ServiceVerifier, allow AllowList, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" { // liveness and readiness probe of the kubelet: no data, no token
				next.ServeHTTP(w, r)
				return
			}
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || token == "" {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeProblem(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
				return
			}
			claims, err := v.Verify(token)
			if err != nil {
				log.Warn("service token rejected", "reason", err.Error(), "path", r.URL.Path)
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeProblem(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
				return
			}
			op := strings.TrimPrefix(r.URL.Path, opPrefix)
			callers, known := allow[op]
			if !strings.HasPrefix(r.URL.Path, opPrefix) || !known {
				writeProblem(w, http.StatusNotFound, "not_found", "Not found")
				return
			}
			if !callers[claims.Issuer] {
				log.Warn("operation denied", "caller", claims.Issuer, "op", op)
				writeProblem(w, http.StatusForbidden, "forbidden", "Operation not allowed for this service")
				return
			}
			ctx := context.WithValue(r.Context(), ctxKey{}, Caller{Service: claims.Issuer, JTI: claims.JTI, Act: claims.Act})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		maxBody := int64(defaultMaxBody)
		switch r.URL.Path {
		case opPrefix + "serviceImport":
			maxBody = importMaxBody
		}
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

// accessLog logs method, path, caller, status and duration. Bodies are never logged.
func accessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			if r.URL.Path == "/healthz" {
				return
			}
			log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
		})
	}
}

func errorHandler(log *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	var de *store.Error
	if errors.As(err, &de) {
		writeProblem(w, de.Status, de.Code, de.Title)
		return
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeProblem(w, http.StatusRequestEntityTooLarge, "too_large", "Request body too large")
		return
	}
	caller, _ := CallerFrom(r.Context())
	log.Error("internal error", "path", r.URL.Path, "caller", caller.Service, "jti", caller.JTI, "error", err.Error())
	writeProblem(w, http.StatusInternalServerError, "internal_error", "Internal error")
}

func writeProblem(w http.ResponseWriter, status int, code, title string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.Problem{Type: "about:blank", Title: title, Status: status, Code: code})
}
