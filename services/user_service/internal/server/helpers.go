package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/api"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/db"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mfa"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mwclient"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/password"
)

func normalizeEmail(e string) string { return strings.TrimSpace(e) }

func roleStrings(roles []mwclient.Role) []string {
	out := make([]string, len(roles))
	for i, r := range roles {
		out[i] = string(r)
	}
	return out
}

func apiRoles(roles []mwclient.Role) []api.Role {
	out := make([]api.Role, len(roles))
	for i, r := range roles {
		out[i] = api.Role(r)
	}
	return out
}

func mwRoles(roles []api.Role) []mwclient.Role {
	out := make([]mwclient.Role, len(roles))
	for i, r := range roles {
		out[i] = mwclient.Role(r)
	}
	return out
}

// internal wraps an unexpected error. errorHandler logs it and answers 500 without details.
func internal(err error) error { return err }

// policyError turns policy violations into a 422 with one entry per broken rule (SR.04).
func policyError(field string, v []password.Violation) *apiError {
	e := newErr(http.StatusUnprocessableEntity, "validation_failed", "Validation failed")
	for _, x := range v {
		e.Errors = append(e.Errors, fieldError{Field: field, Code: x.Code})
	}
	return e
}

// locked reports whether the account may not be used for a credential check right now (SR.05).
func (s *Server) locked(u *db.UserCredentials) (bool, time.Duration) {
	if u.PermanentlyLocked {
		return true, 0
	}
	if u.LockedUntil != nil && u.LockedUntil.After(s.Now()) {
		return true, u.LockedUntil.Sub(s.Now())
	}
	return false, 0
}

// usable reports whether an account may log in at all. Pending, locked and disabled accounts may not.
func (s *Server) usable(u *db.UserCredentials) bool {
	l, _ := s.locked(u)
	return !l && u.Status == mwclient.Active
}

// recordFailure counts a failed credential check and applies the SR.05 lock.
func (s *Server) recordFailure(ctx context.Context, id db.Uuid) error {
	st, err := s.DB.UserRecordLoginFailure(ctx, id)
	if err != nil {
		return err
	}
	if st.PermanentlyLocked || st.LockedUntil != nil {
		s.audit(ctx, "account_locked", &id, nil)
	}
	return nil
}

// checkPassword verifies the password of the logged-in user for sensitive actions. While the account is in a lock
// period no check is made (the attempt is refused) so that a stolen token cannot be used to guess the password.
func (s *Server) checkPassword(ctx context.Context, u *db.UserCredentials, pw string) (bool, *apiError) {
	if l, wait := s.locked(u); l {
		e := newErr(http.StatusTooManyRequests, "temporarily_locked", "Too many failed attempts")
		e.RetryAfter = wait
		return false, e
	}
	ok, _ := password.Verify(pw, u.PasswordHash)
	return ok, nil
}

// secondFactor checks a TOTP code (single use per time step) or a recovery code (single use) of an account with
// confirmed MFA. Exactly one of both must be given. ok=false means the caller counts a failed attempt.
func (s *Server) secondFactor(ctx context.Context, uid db.Uuid, code, recovery *string) (bool, error) {
	if (code == nil) == (recovery == nil) {
		return false, nil
	}
	if recovery != nil {
		return s.DB.RecoveryCodeConsume(ctx, uid, mfa.HashRecoveryCode(s.MFAKey, *recovery))
	}
	return s.totp(ctx, uid, *code, true)
}

// totp verifies a TOTP code against the active secret. If useStep is set the time step is consumed (replay protection).
func (s *Server) totp(ctx context.Context, uid db.Uuid, code string, useStep bool) (bool, error) {
	rec, err := s.DB.MfaSecretGet(ctx, uid)
	if err != nil {
		if db.NotFound(err) {
			return false, nil
		}
		return false, err
	}
	if !rec.Confirmed {
		return false, nil
	}
	secret, err := mfa.Decrypt(s.MFAKey, uid.String(), rec.SecretEnc)
	if err != nil {
		return false, err
	}
	step, ok := mfa.Verify(secret, code, s.Now())
	if !ok {
		return false, nil
	}
	if !useStep {
		return true, nil
	}
	return s.DB.MfaUseStep(ctx, uid, step)
}

// issueToken signs an access token. The scope is full only if MFA was verified, is confirmed, and no password
// change is pending; otherwise it is setup (decision 11, UF-03).
func (s *Server) issueToken(u *db.UserCredentials, mfaEnabled, mfaVerified bool) api.TokenResponse {
	now := s.issuedAt(u)
	scope := praxisauth.ScopeSetup
	if mfaEnabled && mfaVerified && !u.MustChangePassword {
		scope = praxisauth.ScopeFull
	}
	tok := praxisauth.SignUser(s.JWTKey, s.kid, praxisauth.UserClaims{
		Issuer: praxisauth.UserIssuer, Audience: praxisauth.AudienceAPI, Subject: u.Id.String(), Roles: roleStrings(u.Roles),
		IssuedAt: now.Unix(), Expires: now.Add(praxisauth.AccessTokenLifetime).Unix(), JTI: praxisauth.NewJTI(),
		MFA: mfaVerified, Scope: scope,
	})
	roles := apiRoles(u.Roles)
	return api.TokenResponse{
		AccessToken: tok, TokenType: api.Bearer, ExpiresIn: int(praxisauth.AccessTokenLifetime.Seconds()), Scope: api.Scope(scope),
		MustChangePassword: u.MustChangePassword, MfaEnabled: mfaEnabled, Roles: &roles,
	}
}

// audit writes an audit row. Failures are logged; the action itself has already happened.
func (s *Server) audit(ctx context.Context, action string, target *db.Uuid, category *string) {
	p := mwclient.AuditWriteParams{Action: action, TargetUserId: target, DataCategory: category}
	if id, ok := identityFrom(ctx); ok {
		a := id.UserID
		p.ActorUserId = &a
	}
	if err := s.DB.AuditWrite(ctx, p); err != nil {
		s.Log.Error("audit write failed", "action", action, "error", err.Error())
	}
}

func profileOf(u *db.UserCredentials) api.Profile {
	p := api.Profile{
		Id: u.Id, FirstName: u.FirstName, LastName: u.LastName, Email: api.Email(u.Email), Phone: u.Phone,
		Roles: apiRoles(u.Roles), Status: api.UserStatus(u.Status), MfaEnabled: u.MfaEnabled, MustChangePassword: u.MustChangePassword,
	}
	if u.Address != nil {
		p.Address = &api.Address{Street: u.Address.Street, PostalCode: u.Address.PostalCode, City: u.Address.City}
	}
	return p
}

func adminUserOf(u *db.User) api.AdminUser {
	return api.AdminUser{
		Id: u.Id, FirstName: u.FirstName, LastName: u.LastName, Email: api.Email(u.Email), Roles: apiRoles(u.Roles),
		Status: api.UserStatus(u.Status), LockedUntil: u.LockedUntil, PermanentlyLocked: u.PermanentlyLocked,
		MfaEnabled: u.MfaEnabled, CreatedAt: u.CreatedAt,
	}
}

// issuedAt is the iat of a new token of this account: now, but never earlier than sessions_valid_after. A password
// change or reset takes effect at the next full second, so a token issued right afterwards must carry that second
// (decision 20), and it must not look older than the invalidation if this pod's clock is a little behind the database.
func (s *Server) issuedAt(u *db.UserCredentials) time.Time {
	now := s.Now()
	if u.SessionsValidAfter.After(now) {
		return u.SessionsValidAfter
	}
	return now
}
