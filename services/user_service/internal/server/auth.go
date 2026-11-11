package server

import (
	"context"
	"net/http"
	"time"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/api"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/db"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mwclient"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/opaque"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/password"
)

const (
	verifyTokenTTL      = 24 * time.Hour
	resetTokenTTL       = 30 * time.Minute
	adminResetTokenTTL  = 24 * time.Hour
	emailChangeTokenTTL = time.Hour
)

// Register creates a pending patient account and sends the confirmation link (UF-01). The answer is always 202:
// whether or not the address is already registered must not be visible.
func (s *Server) Register(ctx context.Context, req api.RegisterRequestObject) (api.RegisterResponseObject, error) {
	b := req.Body
	email := normalizeEmail(string(b.Email))
	if v := password.Validate(b.Password, email); len(v) > 0 {
		return nil, policyError("password", v)
	}
	hash, err := password.Hash(b.Password) // always, so that the time does not reveal an existing account
	if err != nil {
		return nil, internal(err)
	}
	if _, err := s.DB.UserGetByEmail(ctx, email); err == nil {
		return api.Register202Response{}, nil
	} else if !db.NotFound(err) {
		return nil, internal(err)
	}
	create := mwclient.UserCreate{
		FirstName: b.FirstName, LastName: b.LastName, Email: mwEmail(email), Phone: b.Phone, PasswordHash: hash,
		Roles: []mwclient.Role{mwclient.Patient}, Status: mwclient.Pending, MustChangePassword: false,
	}
	if b.Address != nil {
		create.Address = &mwclient.Address{Street: b.Address.Street, PostalCode: b.Address.PostalCode, City: b.Address.City}
	}
	u, err := s.DB.UserCreate(ctx, create)
	if err != nil {
		if db.Status(err) == http.StatusConflict {
			return api.Register202Response{}, nil // lost a race with another registration
		}
		return nil, internal(err)
	}
	if err := s.sendLink(ctx, u.Id, mwclient.EmailTokenCreateParamsPurposeVerify, verifyTokenTTL, nil, func(tok string) {
		s.Mail.VerifyEmail(ctx, email, u.FirstName, tok)
	}); err != nil {
		return nil, internal(err)
	}
	return api.Register202Response{}, nil
}

// sendLink creates a one-time token, stores its hash and hands the raw token to send (the mail).
func (s *Server) sendLink(ctx context.Context, user db.Uuid, purpose mwclient.EmailTokenCreateParamsPurpose, ttl time.Duration, newEmail *string, send func(token string)) error {
	raw, hash := opaque.New()
	p := mwclient.EmailTokenCreateParams{UserId: user, Purpose: purpose, TokenHash: hash, ExpiresAt: s.Now().Add(ttl)}
	if newEmail != nil {
		e := mwEmail(*newEmail)
		p.NewEmail = &e
	}
	if err := s.DB.EmailTokenCreate(ctx, p); err != nil {
		return err
	}
	send(raw)
	return nil
}

// VerifyEmail activates the account after the link was opened (UF-01).
func (s *Server) VerifyEmail(ctx context.Context, req api.VerifyEmailRequestObject) (api.VerifyEmailResponseObject, error) {
	res, err := s.DB.EmailTokenConsume(ctx, mwclient.EmailTokenConsumeParamsPurposeVerify, opaque.Hash(req.Body.Token))
	if err != nil {
		if db.NotFound(err) {
			return nil, newErr(http.StatusBadRequest, "invalid_token", "The link is invalid, expired or already used")
		}
		return nil, internal(err)
	}
	if err := s.DB.UserSetEmailVerified(ctx, res.UserId); err != nil {
		return nil, internal(err)
	}
	return api.VerifyEmail204Response{}, nil
}

// Login is the password step (UF-02). It returns an MFA challenge, never an access token. Every credential failure
// gives the same 401, and an unknown account costs the same time as a known one.
func (s *Server) Login(ctx context.Context, req api.LoginRequestObject) (api.LoginResponseObject, error) {
	email := normalizeEmail(string(req.Body.Email))
	if ok, retry := s.byEmail.Allow("login|" + email); !ok {
		return nil, tooMany(retry)
	}
	u, err := s.DB.UserGetByEmail(ctx, email)
	if err != nil {
		if db.NotFound(err) {
			password.DummyVerify(req.Body.Password)
			return nil, invalidCredentials()
		}
		return nil, internal(err)
	}
	ok, _ := password.Verify(req.Body.Password, u.PasswordHash)
	if !s.usable(u) {
		return nil, invalidCredentials() // locked, pending or disabled: the same answer, no counter change (SR.05)
	}
	if !ok {
		if err := s.recordFailure(ctx, u.Id); err != nil {
			return nil, internal(err)
		}
		return nil, invalidCredentials()
	}
	now := s.issuedAt(u)
	challenge := praxisauth.SignUser(s.JWTKey, s.kid, praxisauth.UserClaims{
		Issuer: praxisauth.UserIssuer, Audience: praxisauth.AudienceMFA, Subject: u.Id.String(),
		IssuedAt: now.Unix(), Expires: now.Add(praxisauth.MFAChallengeLifetime).Unix(), JTI: praxisauth.NewJTI(),
	})
	return api.Login200JSONResponse{MfaChallenge: challenge, ExpiresIn: int(praxisauth.MFAChallengeLifetime.Seconds())}, nil
}

// LoginMfa is the second step (UF-02, UF-03). With confirmed MFA a TOTP or recovery code is required. An account
// without confirmed MFA, or with a pending password change, only gets a setup-scope token.
func (s *Server) LoginMfa(ctx context.Context, req api.LoginMfaRequestObject) (api.LoginMfaResponseObject, error) {
	b := req.Body
	c, err := praxisauth.VerifyUser(b.MfaChallenge, s.keys, praxisauth.AudienceMFA, s.Now())
	if err != nil {
		return nil, invalidCredentials()
	}
	var uid, jti db.Uuid
	if uid.UnmarshalText([]byte(c.Subject)) != nil || jti.UnmarshalText([]byte(c.JTI)) != nil {
		return nil, invalidCredentials()
	}
	// The challenge is single use: tokenCheck fails once its jti is revoked.
	if valid, err := s.DB.TokenValid(ctx, jti, uid, time.Unix(c.IssuedAt, 0)); err != nil {
		return nil, internal(err)
	} else if !valid {
		return nil, invalidCredentials()
	}
	u, err := s.DB.UserGetByID(ctx, uid)
	if err != nil {
		if db.NotFound(err) {
			return nil, invalidCredentials()
		}
		return nil, internal(err)
	}
	if !s.usable(u) {
		return nil, invalidCredentials()
	}
	if u.MfaEnabled {
		ok, err := s.secondFactor(ctx, uid, b.Code, b.RecoveryCode)
		if err != nil {
			return nil, internal(err)
		}
		if !ok {
			if b.Code != nil || b.RecoveryCode != nil { // a missing code is a client error, not a guess
				if err := s.recordFailure(ctx, uid); err != nil {
					return nil, internal(err)
				}
			}
			return nil, invalidCredentials()
		}
	} else if b.Code != nil || b.RecoveryCode != nil {
		return nil, invalidCredentials()
	}
	if err := s.DB.TokenRevoke(ctx, jti, uid, time.Unix(c.Expires, 0)); err != nil {
		return nil, internal(err)
	}
	if err := s.DB.UserRecordLoginSuccess(ctx, uid); err != nil && !db.NotFound(err) {
		return nil, internal(err)
	}
	if s.LoginWarnings {
		s.Mail.LoginWarning(ctx, string(u.Email), u.FirstName, s.Now())
	}
	return api.LoginMfa200JSONResponse(s.issueToken(u, u.MfaEnabled, u.MfaEnabled)), nil
}

// Logout revokes the token of the request.
func (s *Server) Logout(ctx context.Context, _ api.LogoutRequestObject) (api.LogoutResponseObject, error) {
	id, _ := identityFrom(ctx)
	var jti db.Uuid
	if err := jti.UnmarshalText([]byte(id.JTI)); err != nil {
		return nil, unauthorized()
	}
	if err := s.DB.TokenRevoke(ctx, jti, id.UserID, id.ExpiresAt); err != nil {
		return nil, internal(err)
	}
	s.cache.forget(id.JTI)
	return api.Logout204Response{}, nil
}

// ForgotPassword sends a reset link if the account exists (UF-12). The answer is always 202.
func (s *Server) ForgotPassword(ctx context.Context, req api.ForgotPasswordRequestObject) (api.ForgotPasswordResponseObject, error) {
	email := normalizeEmail(string(req.Body.Email))
	u, err := s.DB.UserGetByEmail(ctx, email)
	if err != nil {
		if db.NotFound(err) {
			return api.ForgotPassword202Response{}, nil
		}
		return nil, internal(err)
	}
	if u.Status != mwclient.Active {
		return api.ForgotPassword202Response{}, nil
	}
	if err := s.sendLink(ctx, u.Id, mwclient.EmailTokenCreateParamsPurposeReset, resetTokenTTL, nil, func(tok string) {
		s.Mail.PasswordReset(ctx, string(u.Email), u.FirstName, tok)
	}); err != nil {
		return nil, internal(err)
	}
	return api.ForgotPassword202Response{}, nil
}

// ResetPassword sets a new password with a reset token (UF-10, UF-12). All sessions of the user become invalid.
// MFA stays active: the reset does not bypass it.
func (s *Server) ResetPassword(ctx context.Context, req api.ResetPasswordRequestObject) (api.ResetPasswordResponseObject, error) {
	b := req.Body
	if v := password.Validate(b.NewPassword, ""); len(v) > 0 {
		return nil, policyError("new_password", v)
	}
	hash, err := password.Hash(b.NewPassword)
	if err != nil {
		return nil, internal(err)
	}
	res, err := s.DB.EmailTokenConsume(ctx, mwclient.EmailTokenConsumeParamsPurposeReset, opaque.Hash(b.Token))
	if err != nil {
		if db.NotFound(err) {
			return nil, newErr(http.StatusBadRequest, "invalid_token", "The link is invalid, expired or already used")
		}
		return nil, internal(err)
	}
	if err := s.DB.UserSetPassword(ctx, res.UserId, hash, false); err != nil {
		return nil, internal(err)
	}
	return api.ResetPassword204Response{}, nil
}

// ConfirmEmailChange applies a confirmed e-mail change.
func (s *Server) ConfirmEmailChange(ctx context.Context, req api.ConfirmEmailChangeRequestObject) (api.ConfirmEmailChangeResponseObject, error) {
	res, err := s.DB.EmailTokenConsume(ctx, mwclient.EmailTokenConsumeParamsPurposeEmailChange, opaque.Hash(req.Body.Token))
	if err != nil {
		if db.NotFound(err) {
			return nil, newErr(http.StatusBadRequest, "invalid_token", "The link is invalid, expired or already used")
		}
		return nil, internal(err)
	}
	if res.NewEmail == nil {
		return nil, newErr(http.StatusBadRequest, "invalid_token", "The link is invalid, expired or already used")
	}
	if err := s.DB.UserChangeEmail(ctx, res.UserId, string(*res.NewEmail)); err != nil {
		if db.Status(err) == http.StatusConflict {
			return nil, conflict("email_taken", "This address is already in use")
		}
		return nil, internal(err)
	}
	return api.ConfirmEmailChange204Response{}, nil
}
