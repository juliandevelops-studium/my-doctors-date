// Package server is the HTTP side of user_service: routing, authentication, the authorisation table (SR.03),
// rate limits, request validation and the handlers.
package server

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/go-chi/chi/v5"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/api"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/db"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mailer"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/policy"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/ratelimit"
)

const (
	maxBody = 64 << 10
	// Defaults, see Deps.
	defaultTokenCacheTTL  = 10 * time.Second // docs/api/internal.md section 1, step 3
	defaultPublicLimit    = 30               // requests per minute and client for public routes
	defaultSensitiveLimit = 10               // requests per minute and user for password, MFA and deletion routes
	defaultEmailLimit     = 10               // login attempts per minute and e-mail address
)

func init() {
	// Make the schema check reject malformed e-mail addresses and UUIDs with 422 instead of failing later in the decoder.
	openapi3.DefineStringFormat("email", openapi3.FormatOfStringForEmail)
	openapi3.DefineStringFormat("uuid", `^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
}

// Deps are the collaborators of the server.
type Deps struct {
	DB            *db.DB
	Mail          *mailer.Mailer
	JWTKey        ed25519.PrivateKey
	MFAKey        []byte
	LoginWarnings bool
	Log           *slog.Logger
	Now           func() time.Time

	// Limits per minute; zero means the default. TokenCacheTTL zero means 10 s, negative disables the cache.
	PublicPerMinute, UserPerMinute, EmailPerMinute int
	TokenCacheTTL                                  time.Duration
}

// Server implements api.StrictServerInterface.
type Server struct {
	Deps
	kid     string
	keys    praxisauth.StaticKeySet
	jwks    praxisauth.JWKS
	public  *ratelimit.Limiter
	byUser  *ratelimit.Limiter
	byEmail *ratelimit.Limiter
	cache   *tokenCache
	ttl     time.Duration
}

// New creates the server state.
func New(d Deps) *Server {
	if d.Now == nil {
		d.Now = time.Now
	}
	pub := d.JWTKey.Public().(ed25519.PublicKey)
	orDefault := func(v, def int) int {
		if v == 0 {
			return def
		}
		return v
	}
	ttl := d.TokenCacheTTL
	if ttl == 0 {
		ttl = defaultTokenCacheTTL
	}
	return &Server{ttl: ttl,
		Deps: d, kid: praxisauth.KeyID(pub), keys: praxisauth.StaticKeySet{praxisauth.KeyID(pub): pub}, jwks: praxisauth.NewJWKS(pub),
		public: ratelimit.New(orDefault(d.PublicPerMinute, defaultPublicLimit), time.Minute), byUser: ratelimit.New(orDefault(d.UserPerMinute, defaultSensitiveLimit), time.Minute),
		byEmail: ratelimit.New(orDefault(d.EmailPerMinute, defaultEmailLimit), time.Minute), cache: &tokenCache{m: map[string]time.Time{}},
	}
}

// sensitive routes are rate limited per user in addition to the role check.
var sensitive = map[string]bool{
	"changePassword": true, "requestEmailChange": true, "mfaSetupStart": true, "mfaSetupConfirm": true,
	"mfaDisable": true, "mfaRegenerateRecoveryCodes": true, "deleteOwnAccount": true,
}

// Handler builds the HTTP handler with the full middleware chain:
// log -> body limit -> content type -> authenticate/authorise/rate limit -> request validation -> handler.
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
	r.Use(s.accessLog, noSniff)
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

type ctxKey int

const (
	identityKey ctxKey = iota
	opKey
)

// Identity is the authenticated caller. It comes only from the validated token (SR.03).
type Identity struct {
	UserID    db.Uuid
	Roles     []string
	JTI       string
	Scope     string
	MFA       bool
	IssuedAt  time.Time
	ExpiresAt time.Time
}

func identityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey).(Identity)
	return id, ok
}

func (i Identity) has(role string) bool {
	for _, r := range i.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// authorize looks up the operation, applies the generated policy table (deny by default), verifies the token and
// enforces rate limits. Order of checks: 401, 403, 429; request validation (422) comes after.
func (s *Server) authorize(router routers.Router) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route, _, err := router.FindRoute(r)
			if err != nil {
				writeProblem(w, notFound())
				return
			}
			op := contractName(route.Operation.OperationID)
			rule, ok := policy.Routes[op]
			if !ok {
				writeProblem(w, notFound()) // not in authz.yaml: not served
				return
			}
			ctx := context.WithValue(r.Context(), opKey, op)
			switch rule.Access {
			case policy.Internal:
			case policy.Public:
				if ok, retry := s.public.Allow(op + "|" + clientIP(r)); !ok {
					writeProblem(w, tooMany(retry))
					return
				}
			case policy.User:
				id, aerr := s.authenticate(ctx, r, rule)
				if aerr != nil {
					writeProblem(w, aerr)
					return
				}
				if sensitive[op] {
					if ok, retry := s.byUser.Allow(op + "|" + id.UserID.String()); !ok {
						writeProblem(w, tooMany(retry))
						return
					}
				}
				ctx = context.WithValue(ctx, identityKey, id)
				ctx = db.WithActor(ctx, id.UserID.String())
			default:
				writeProblem(w, forbidden()) // service routes do not exist in user_service
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func (s *Server) authenticate(ctx context.Context, r *http.Request, rule policy.Route) (Identity, *apiError) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		return Identity{}, unauthorized()
	}
	now := s.Now()
	c, err := praxisauth.VerifyUser(raw, s.keys, praxisauth.AudienceAPI, now)
	if err != nil {
		return Identity{}, unauthorized()
	}
	// A setup token is accepted only by the routes marked scope: setup. A full token needs a completed MFA login.
	switch {
	case c.Scope == praxisauth.ScopeSetup && rule.Setup:
	case c.Scope == praxisauth.ScopeFull && c.MFA:
	default:
		return Identity{}, unauthorized()
	}
	id := Identity{Roles: c.Roles, JTI: c.JTI, Scope: c.Scope, MFA: c.MFA, IssuedAt: time.Unix(c.IssuedAt, 0), ExpiresAt: time.Unix(c.Expires, 0)}
	if err := id.UserID.UnmarshalText([]byte(c.Subject)); err != nil {
		return Identity{}, unauthorized()
	}
	var jti db.Uuid
	if err := jti.UnmarshalText([]byte(c.JTI)); err != nil {
		return Identity{}, unauthorized()
	}
	valid, err := s.cache.check(now, s.ttl, c.JTI, func() (bool, error) { return s.DB.TokenValid(ctx, jti, id.UserID, id.IssuedAt) })
	if err != nil {
		s.Log.Error("token check failed", "error", err.Error())
		return Identity{}, newErr(http.StatusServiceUnavailable, "unavailable", "Temporarily unavailable")
	}
	if !valid {
		return Identity{}, unauthorized()
	}
	if !allowed(rule.Roles, c.Roles) {
		return Identity{}, forbidden()
	}
	return id, nil
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

// tokenCache remembers positive token checks for a few seconds (docs/api/internal.md: at most 10 s).
type tokenCache struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func (c *tokenCache) check(now time.Time, ttl time.Duration, jti string, load func() (bool, error)) (bool, error) {
	if ttl < 0 {
		return load()
	}
	c.mu.Lock()
	if until, ok := c.m[jti]; ok && now.Before(until) {
		c.mu.Unlock()
		return true, nil
	}
	c.mu.Unlock()
	ok, err := load()
	if err != nil || !ok {
		return false, err
	}
	c.mu.Lock()
	if len(c.m) > 10_000 {
		for k, v := range c.m {
			if !now.Before(v) {
				delete(c.m, k)
			}
		}
	}
	c.m[jti] = now.Add(ttl)
	c.mu.Unlock()
	return true, nil
}

// forget drops a cached token (logout on this replica).
func (c *tokenCache) forget(jti string) {
	c.mu.Lock()
	delete(c.m, jti)
	c.mu.Unlock()
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
			writeProblem(w, newErr(http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP is the address of the browser as passed on by frontend_provider (the only caller, NetworkPolicy).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

// accessLog logs method, path, status and duration. Bodies, tokens and e-mail addresses are never logged.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/healthz" {
			s.Log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", s.Now().Sub(start).Milliseconds())
		}
	})
}

// UserServiceHealth is the liveness probe.
func (s *Server) UserServiceHealth(context.Context, api.UserServiceHealthRequestObject) (api.UserServiceHealthResponseObject, error) {
	return api.UserServiceHealth200Response{}, nil
}

// GetJwks returns the public signing keys.
func (s *Server) GetJwks(context.Context, api.GetJwksRequestObject) (api.GetJwksResponseObject, error) {
	var out api.Jwks
	for _, k := range s.jwks.Keys {
		alg, use := api.JwksKeysAlg(k.Alg), api.JwksKeysUse(k.Use)
		out.Keys = append(out.Keys, struct {
			Alg *api.JwksKeysAlg `json:"alg,omitempty"`
			Crv api.JwksKeysCrv  `json:"crv"`
			Kid string           `json:"kid"`
			Kty api.JwksKeysKty  `json:"kty"`
			Use *api.JwksKeysUse `json:"use,omitempty"`
			X   string           `json:"x"`
		}{Alg: &alg, Crv: api.JwksKeysCrv(k.Crv), Kid: k.Kid, Kty: api.JwksKeysKty(k.Kty), Use: &use, X: k.X})
	}
	return api.GetJwks200JSONResponse(out), nil
}

// contractName maps the operationId of the embedded spec (oapi-codegen capitalises it: "Login") to the name used in
// docs/api/authorization.yaml and the policy table ("login").
func contractName(operationID string) string {
	if operationID == "" {
		return ""
	}
	return strings.ToLower(operationID[:1]) + operationID[1:]
}

// validationFailure turns a schema violation into a 422 that names the field and the broken keyword
// (min_length, max_length, pattern, additional_properties, ...). The rejected value is never echoed.
func validationFailure(err error) *apiError {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return newErr(http.StatusRequestEntityTooLarge, "payload_too_large", "Request body is too large")
	}
	e := newErr(http.StatusUnprocessableEntity, "validation_failed", "Request validation failed")
	var se *openapi3.SchemaError
	if errors.As(err, &se) && se.SchemaField == "maxLength" && len(se.JSONPointer()) == 0 {
		// a raw image body (decision 55) longer than the schema allows
		return newErr(http.StatusRequestEntityTooLarge, "payload_too_large", "Request body is too large")
	}
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

// noSniff stops browsers from guessing a type for any answer (images are served with a fixed type).
func noSniff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
