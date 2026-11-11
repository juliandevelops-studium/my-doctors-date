// Package db is the typed access to database_middleware. user_service has no other database access (SR.02).
// Only the operations that user_service may call exist in the generated client (x-allowed-callers).
package db

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mwclient"
)

// Types re-exported for the callers.
type (
	User            = mwclient.UserRecord
	UserCredentials = mwclient.UserWithCredentials
	Uuid            = openapi_types.UUID
)

// Error is a failed middleware call. Status and Code come from the problem answer of the middleware.
type Error struct {
	Status int
	Code   string
}

func (e *Error) Error() string { return http.StatusText(e.Status) + ": " + e.Code }

// Status returns the HTTP status of a middleware error, or 0.
func Status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// Code returns the problem code of a middleware error, or "".
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// NotFound reports a 404 of the middleware.
func NotFound(err error) bool { return Status(err) == http.StatusNotFound }

func fail(status int, body []byte) error {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &p)
	return &Error{Status: status, Code: p.Code}
}

type actKey struct{}

// WithActor records the end user on whose behalf calls are made (claim act of the service token, audit only).
func WithActor(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, actKey{}, userID)
}

// DB talks to database_middleware.
type DB struct {
	c *mwclient.ClientWithResponses
}

// New creates the client. Every request carries a fresh service token (iss=user_service, at most 60 s).
func New(baseURL string, signer *praxisauth.ServiceSigner) (*DB, error) {
	c, err := mwclient.NewClientWithResponses(baseURL,
		mwclient.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}),
		mwclient.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			act, _ := ctx.Value(actKey{}).(string)
			req.Header.Set("Authorization", "Bearer "+signer.Token("database_middleware", act))
			return nil
		}))
	if err != nil {
		return nil, err
	}
	return &DB{c: c}, nil
}

// ---- accounts

func (d *DB) UserCreate(ctx context.Context, p mwclient.UserCreate) (*User, error) {
	r, err := d.c.UserCreateWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	if r.JSON201 == nil {
		return nil, fail(r.StatusCode(), r.Body)
	}
	return r.JSON201, nil
}

func (d *DB) UserGetByEmail(ctx context.Context, email string) (*UserCredentials, error) {
	r, err := d.c.UserGetByEmailWithResponse(ctx, mwclient.EmailParam{Email: openapi_types.Email(email)})
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200, nil
}

func (d *DB) UserGetByID(ctx context.Context, id Uuid) (*UserCredentials, error) {
	r, err := d.c.UserGetByIdWithResponse(ctx, mwclient.UserIdParam{UserId: id})
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200, nil
}

func (d *DB) UserList(ctx context.Context, p mwclient.UserListParams) (*mwclient.UserPage, error) {
	r, err := d.c.UserListWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200, nil
}

func (d *DB) UserUpdateProfile(ctx context.Context, p mwclient.UserProfileUpdate) (*User, error) {
	r, err := d.c.UserUpdateProfileWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200, nil
}

func (d *DB) UserSetPassword(ctx context.Context, id Uuid, hash string, mustChange bool) error {
	r, err := d.c.UserSetPasswordWithResponse(ctx, mwclient.SetPasswordParams{UserId: id, PasswordHash: hash, MustChangePassword: mustChange})
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

func (d *DB) UserSetEmailVerified(ctx context.Context, id Uuid) error {
	r, err := d.c.UserSetEmailVerifiedWithResponse(ctx, mwclient.UserIdParam{UserId: id})
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

func (d *DB) UserChangeEmail(ctx context.Context, id Uuid, email string) error {
	r, err := d.c.UserChangeEmailWithResponse(ctx, mwclient.ChangeEmailParams{UserId: id, NewEmail: openapi_types.Email(email)})
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

func (d *DB) UserSetRoles(ctx context.Context, id Uuid, roles []mwclient.Role) (*User, error) {
	r, err := d.c.UserSetRolesWithResponse(ctx, mwclient.SetRolesParams{UserId: id, Roles: roles})
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200, nil
}

func (d *DB) UserRecordLoginFailure(ctx context.Context, id Uuid) (*mwclient.LockState, error) {
	r, err := d.c.UserRecordLoginFailureWithResponse(ctx, mwclient.UserIdParam{UserId: id})
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200, nil
}

func (d *DB) UserRecordLoginSuccess(ctx context.Context, id Uuid) error {
	r, err := d.c.UserRecordLoginSuccessWithResponse(ctx, mwclient.UserIdParam{UserId: id})
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

func (d *DB) UserUnlock(ctx context.Context, id Uuid) error {
	r, err := d.c.UserUnlockWithResponse(ctx, mwclient.UserIdParam{UserId: id})
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

func (d *DB) UserRevokeSessions(ctx context.Context, id Uuid) error {
	r, err := d.c.UserRevokeSessionsWithResponse(ctx, mwclient.UserIdParam{UserId: id})
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

func (d *DB) AccountDelete(ctx context.Context, id Uuid) (*mwclient.AccountDeleteResult, error) {
	r, err := d.c.AccountDeleteWithResponse(ctx, mwclient.UserIdParam{UserId: id})
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200, nil
}

// ---- tokens and MFA

func (d *DB) TokenValid(ctx context.Context, jti, sub Uuid, iat time.Time) (bool, error) {
	r, err := d.c.TokenCheckWithResponse(ctx, mwclient.TokenCheckParams{Jti: jti, Sub: sub, Iat: iat})
	if err != nil {
		return false, err
	}
	if r.JSON200 == nil {
		return false, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200.Valid, nil
}

func (d *DB) TokenRevoke(ctx context.Context, jti, user Uuid, expires time.Time) error {
	r, err := d.c.TokenRevokeWithResponse(ctx, mwclient.TokenRevokeParams{Jti: jti, UserId: user, ExpiresAt: expires})
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

func (d *DB) EmailTokenCreate(ctx context.Context, p mwclient.EmailTokenCreateParams) error {
	r, err := d.c.EmailTokenCreateWithResponse(ctx, p)
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

func (d *DB) EmailTokenConsume(ctx context.Context, purpose mwclient.EmailTokenConsumeParamsPurpose, hash string) (*mwclient.EmailTokenResult, error) {
	r, err := d.c.EmailTokenConsumeWithResponse(ctx, mwclient.EmailTokenConsumeParams{Purpose: purpose, TokenHash: hash})
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200, nil
}

func (d *DB) MfaSecretUpsert(ctx context.Context, id Uuid, sealed []byte) error {
	r, err := d.c.MfaSecretUpsertWithResponse(ctx, mwclient.MfaSecretParams{UserId: id, SecretEnc: sealed})
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

func (d *DB) MfaSecretGet(ctx context.Context, id Uuid) (*mwclient.MfaSecretRecord, error) {
	r, err := d.c.MfaSecretGetWithResponse(ctx, mwclient.UserIdParam{UserId: id})
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200, nil
}

func (d *DB) MfaSecretConfirm(ctx context.Context, id Uuid) error {
	r, err := d.c.MfaSecretConfirmWithResponse(ctx, mwclient.UserIdParam{UserId: id})
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

// MfaUseStep accepts a TOTP time step once; false means it was used before (replay).
func (d *DB) MfaUseStep(ctx context.Context, id Uuid, step int64) (bool, error) {
	r, err := d.c.MfaUseStepWithResponse(ctx, mwclient.MfaStepParams{UserId: id, Step: step})
	if err != nil {
		return false, err
	}
	if r.JSON200 == nil {
		return false, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200.Accepted, nil
}

func (d *DB) MfaDelete(ctx context.Context, id Uuid) error {
	r, err := d.c.MfaDeleteWithResponse(ctx, mwclient.UserIdParam{UserId: id})
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

func (d *DB) RecoveryCodesReplace(ctx context.Context, id Uuid, hashes []string) error {
	r, err := d.c.RecoveryCodesReplaceWithResponse(ctx, mwclient.RecoveryCodesParams{UserId: id, CodeHashes: hashes})
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

func (d *DB) RecoveryCodeConsume(ctx context.Context, id Uuid, hash string) (bool, error) {
	r, err := d.c.RecoveryCodeConsumeWithResponse(ctx, mwclient.RecoveryCodeConsumeParams{UserId: id, CodeHash: hash})
	if err != nil {
		return false, err
	}
	if r.JSON200 == nil {
		return false, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200.Consumed, nil
}

// ---- config and audit

func (d *DB) RetentionGet(ctx context.Context) (*mwclient.Retention, error) {
	r, err := d.c.RetentionGetWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200, nil
}

func (d *DB) RetentionSet(ctx context.Context, p mwclient.RetentionSetParams) (*mwclient.Retention, error) {
	r, err := d.c.RetentionSetWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200, nil
}

func (d *DB) AuditWrite(ctx context.Context, p mwclient.AuditWriteParams) error {
	r, err := d.c.AuditWriteWithResponse(ctx, p)
	if err != nil {
		return err
	}
	return done(r.StatusCode(), r.Body)
}

func (d *DB) AuditList(ctx context.Context, p mwclient.AuditListParams) (*mwclient.AuditPage, error) {
	r, err := d.c.AuditListWithResponse(ctx, p)
	if err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, fail(r.StatusCode(), r.Body)
	}
	return r.JSON200, nil
}

// done turns the answer of an operation without a body into an error unless it is 204.
func done(status int, body []byte) error {
	if status == http.StatusNoContent {
		return nil
	}
	return fail(status, body)
}
