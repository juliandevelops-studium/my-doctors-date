// Package server is the HTTP side of calendar_service: authentication from the user token, the generated
// authorisation table (SR.03), request validation and the handlers for practice data, the calendar and bookings.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/go-chi/chi/v5"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/api"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/db"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/mailer"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/policy"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/ratelimit"
)

const (
	maxBody       = 64 << 10
	maxImportBody = 256 << 10
	// SlotStep is the grid of bookable start times inside a free block.
	SlotStep = 15 * time.Minute
	// MaxRange is the longest range of calendar queries (docs/api/CONVENTIONS.md).
	MaxRange = 62 * 24 * time.Hour
)

func init() {
	openapi3.DefineStringFormat("email", openapi3.FormatOfStringForEmail)
	openapi3.DefineStringFormat("uuid", `^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
}

// Deps are the collaborators of the server.
type Deps struct {
	DB   *db.DB
	Mail *mailer.Mailer
	Auth *praxisauth.Authenticator
	Log  *slog.Logger
	Now  func() time.Time

	// BookingsPerMinute limits createBooking per user; zero means 20.
	BookingsPerMinute int
}

// Server implements api.StrictServerInterface.
type Server struct {
	Deps
	bookings *ratelimit.Limiter
}

// New creates the server state.
func New(d Deps) *Server {
	if d.Now == nil {
		d.Now = time.Now
	}
	n := d.BookingsPerMinute
	if n == 0 {
		n = 20
	}
	return &Server{Deps: d, bookings: ratelimit.New(n, time.Minute)}
}

type ctxKey int

const identityKey ctxKey = iota

// Identity is the authenticated caller. It comes only from the validated token (SR.03).
type Identity struct {
	UserID db.Uuid
	Roles  []string
}

func identityFrom(ctx context.Context) Identity {
	id, _ := ctx.Value(identityKey).(Identity)
	return id
}

func (i Identity) has(role string) bool {
	for _, r := range i.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// staff are the roles that see and manage all appointments.
func (i Identity) staff() bool { return i.has("desk") || i.has("organisator") }

// Handler builds the HTTP handler: log -> body limit -> content type -> authenticate/authorise -> validation -> handler.
func (s *Server) Handler() (http.Handler, error) {
	spec, err := api.GetSwagger()
	if err != nil {
		return nil, err
	}
	spec.Servers = nil
	router, err := legacy.NewRouter(spec)
	if err != nil {
		return nil, err
	}
	validator := nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error { return nil }, // done by authorize
		},
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, _ nethttpmiddleware.ErrorHandlerOpts) {
			writeProblem(w, validationFailure(err))
		},
	})
	strict := api.NewStrictHandlerWithOptions(s, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeProblem(w, newErr(http.StatusBadRequest, "bad_request", "Malformed request"))
		},
		ResponseErrorHandlerFunc: errorHandler(s.Log),
	})
	r := chi.NewRouter()
	r.Use(s.accessLog)
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { writeProblem(w, notFound()) })
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, newErr(http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed"))
	})
	r.Group(func(r chi.Router) {
		r.Use(limitBody, requireJSON, s.authorize(router), validator)
		api.HandlerFromMux(strict, r)
	})
	return r, nil
}

// authorize looks up the operation, applies the generated policy table (deny by default) and verifies the token.
// Order of checks: 401, 403; request validation (422) comes after.
func (s *Server) authorize(router routers.Router) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route, _, err := router.FindRoute(r)
			if err != nil {
				writeProblem(w, notFound())
				return
			}
			rule, ok := policy.Routes[contractName(route.Operation.OperationID)]
			if !ok {
				writeProblem(w, notFound()) // not in docs/api/authorization.yaml: not served
				return
			}
			switch rule.Access {
			case policy.Internal:
				next.ServeHTTP(w, r)
				return
			case policy.User:
			default:
				writeProblem(w, forbidden())
				return
			}
			c, err := s.Auth.Authenticate(r.Context(), r.Header.Get("Authorization"), rule.Setup)
			if err != nil {
				if errors.Is(err, praxisauth.ErrUnauthenticated) {
					writeProblem(w, unauthorized())
				} else {
					s.Log.Error("token check failed", "error", err.Error())
					writeProblem(w, newErr(http.StatusServiceUnavailable, "unavailable", "Temporarily unavailable"))
				}
				return
			}
			if !allowed(rule.Roles, c.Roles) {
				writeProblem(w, forbidden())
				return
			}
			id := Identity{Roles: c.Roles}
			if id.UserID.UnmarshalText([]byte(c.Subject)) != nil {
				writeProblem(w, unauthorized())
				return
			}
			ctx := context.WithValue(r.Context(), identityKey, id)
			ctx = db.WithActor(ctx, id.UserID.String())
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func allowed(want, have []string) bool {
	for _, w := range want {
		if w == "any" {
			return true
		}
		for _, h := range have {
			if w == h {
				return true
			}
		}
	}
	return false
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		max := int64(maxBody)
		if strings.HasSuffix(r.URL.Path, "/services/import") {
			max = maxImportBody
		}
		r.Body = http.MaxBytesReader(w, r.Body, max)
		next.ServeHTTP(w, r)
	})
}

func requireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeProblem(w, newErr(http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json"))
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

// accessLog logs method, path, status and duration. Bodies and tokens are never logged.
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

// contractName maps the operationId of the embedded spec (oapi-codegen capitalises it) to the name in docs/api/authorization.yaml.
func contractName(operationID string) string {
	if operationID == "" {
		return ""
	}
	return strings.ToLower(operationID[:1]) + operationID[1:]
}

// validationFailure turns a schema violation into a 422 naming the field and the broken keyword (never the value).
func validationFailure(err error) *apiError {
	e := newErr(http.StatusUnprocessableEntity, "validation_failed", "Request validation failed")
	var se *openapi3.SchemaError
	if errors.As(err, &se) {
		e.Errors = []fieldError{{Field: strings.Join(se.JSONPointer(), "."), Code: snake(se.SchemaField)}}
	}
	return e
}

func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// clientKey is used for rate limits that are not per user.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// CalendarServiceHealth is the liveness probe.
func (s *Server) CalendarServiceHealth(context.Context, api.CalendarServiceHealthRequestObject) (api.CalendarServiceHealthResponseObject, error) {
	return api.CalendarServiceHealth200Response{}, nil
}

// mapErr turns middleware answers into API answers; anything else stays an internal error.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	switch db.Status(err) {
	case http.StatusNotFound:
		return notFound()
	case http.StatusConflict:
		code := db.Code(err)
		if code == "" {
			code = "conflict"
		}
		return conflict(code, "Conflict with the current state")
	case http.StatusUnprocessableEntity:
		code := db.Code(err)
		if code == "" {
			code = "validation_failed"
		}
		return unprocessable(code, "Validation failed")
	}
	return err
}
