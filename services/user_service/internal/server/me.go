package server

import (
	"context"
	"net/http"

	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/api"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/db"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mfa"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mwclient"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/password"
)

func (s *Server) me(ctx context.Context) (Identity, *db.UserCredentials, error) {
	id, _ := identityFrom(ctx)
	u, err := s.DB.UserGetByID(ctx, id.UserID)
	if err != nil {
		if db.NotFound(err) {
			return id, nil, unauthorized()
		}
		return id, nil, internal(err)
	}
	return id, u, nil
}

// GetMe returns the own profile.
func (s *Server) GetMe(ctx context.Context, _ api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	_, u, err := s.me(ctx)
	if err != nil {
		return nil, err
	}
	return api.GetMe200JSONResponse(profileOf(u)), nil
}

// UpdateMe changes name, phone and (patients) address. The identity is the one of the token.
func (s *Server) UpdateMe(ctx context.Context, req api.UpdateMeRequestObject) (api.UpdateMeResponseObject, error) {
	id, _ := identityFrom(ctx)
	b := req.Body
	p := mwclient.UserProfileUpdate{UserId: id.UserID, FirstName: b.FirstName, LastName: b.LastName, Phone: b.Phone}
	if b.Address != nil {
		p.Address = &mwclient.Address{Street: b.Address.Street, PostalCode: b.Address.PostalCode, City: b.Address.City}
	}
	if _, err := s.DB.UserUpdateProfile(ctx, p); err != nil {
		if db.Status(err) == http.StatusUnprocessableEntity {
			return nil, unprocessable(db.Code(err), "Address data only exists for patients")
		}
		return nil, internal(err)
	}
	u, err := s.DB.UserGetByID(ctx, id.UserID)
	if err != nil {
		return nil, internal(err)
	}
	return api.UpdateMe200JSONResponse(profileOf(u)), nil
}

// ChangePassword changes the own password (UF-03). It needs the current password, counts a wrong one as a failed
// attempt (SR.05), invalidates all sessions and returns a new token.
func (s *Server) ChangePassword(ctx context.Context, req api.ChangePasswordRequestObject) (api.ChangePasswordResponseObject, error) {
	id, u, err := s.me(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	ok, lockErr := s.checkPassword(ctx, u, b.CurrentPassword)
	if lockErr != nil {
		return nil, lockErr
	}
	if !ok {
		if err := s.recordFailure(ctx, u.Id); err != nil {
			return nil, internal(err)
		}
		return nil, unprocessable("invalid_current_password", "The current password is wrong")
	}
	if v := password.Validate(b.NewPassword, string(u.Email)); len(v) > 0 {
		return nil, policyError("new_password", v)
	}
	if b.NewPassword == b.CurrentPassword {
		return nil, validation("new_password", "password_unchanged")
	}
	hash, err := password.Hash(b.NewPassword)
	if err != nil {
		return nil, internal(err)
	}
	if err := s.DB.UserSetPassword(ctx, u.Id, hash, false); err != nil {
		return nil, internal(err)
	}
	fresh, err := s.DB.UserGetByID(ctx, u.Id) // sessions_valid_after was just set
	if err != nil {
		return nil, internal(err)
	}
	return api.ChangePassword200JSONResponse(s.issueToken(fresh, fresh.MfaEnabled, id.MFA)), nil
}

// RequestEmailChange starts an e-mail change: a link goes to the new address, a notice to the old one.
// The answer is always 202 so that it does not show whether the new address is registered.
func (s *Server) RequestEmailChange(ctx context.Context, req api.RequestEmailChangeRequestObject) (api.RequestEmailChangeResponseObject, error) {
	_, u, err := s.me(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	ok, lockErr := s.checkPassword(ctx, u, b.CurrentPassword)
	if lockErr != nil {
		return nil, lockErr
	}
	if !ok {
		if err := s.recordFailure(ctx, u.Id); err != nil {
			return nil, internal(err)
		}
		return nil, unprocessable("invalid_current_password", "The current password is wrong")
	}
	newEmail := normalizeEmail(string(b.NewEmail))
	if equalFold(newEmail, string(u.Email)) {
		return nil, validation("new_email", "email_unchanged")
	}
	if _, err := s.DB.UserGetByEmail(ctx, newEmail); err == nil {
		return api.RequestEmailChange202Response{}, nil // taken: silently do nothing
	} else if !db.NotFound(err) {
		return nil, internal(err)
	}
	if err := s.sendLink(ctx, u.Id, mwclient.EmailTokenCreateParamsPurposeEmailChange, emailChangeTokenTTL, &newEmail, func(tok string) {
		s.Mail.EmailChangeConfirm(ctx, newEmail, u.FirstName, tok)
	}); err != nil {
		return nil, internal(err)
	}
	s.Mail.EmailChangeNotice(ctx, string(u.Email), u.FirstName)
	return api.RequestEmailChange202Response{}, nil
}

// MfaSetupStart creates a TOTP secret (UF-03, UF-04). It is refused while MFA is active: MFA has to be removed
// first (decision 22).
func (s *Server) MfaSetupStart(ctx context.Context, _ api.MfaSetupStartRequestObject) (api.MfaSetupStartResponseObject, error) {
	_, u, err := s.me(ctx)
	if err != nil {
		return nil, err
	}
	if u.MfaEnabled {
		return nil, conflict("mfa_active", "MFA is already active, remove it first")
	}
	secret := mfa.NewSecret()
	sealed, err := mfa.Encrypt(s.MFAKey, u.Id.String(), secret)
	if err != nil {
		return nil, internal(err)
	}
	if err := s.DB.MfaSecretUpsert(ctx, u.Id, sealed); err != nil {
		if db.Status(err) == http.StatusConflict {
			return nil, conflict("mfa_active", "MFA is already active, remove it first")
		}
		return nil, internal(err)
	}
	return api.MfaSetupStart200JSONResponse{Secret: mfa.EncodeSecret(secret), OtpauthUri: mfa.URI("Praxis", string(u.Email), secret)}, nil
}

// MfaSetupConfirm checks the first code, activates MFA, returns the recovery codes once and a new token.
func (s *Server) MfaSetupConfirm(ctx context.Context, req api.MfaSetupConfirmRequestObject) (api.MfaSetupConfirmResponseObject, error) {
	_, u, err := s.me(ctx)
	if err != nil {
		return nil, err
	}
	if u.MfaEnabled {
		return nil, conflict("mfa_active", "MFA is already active")
	}
	rec, err := s.DB.MfaSecretGet(ctx, u.Id)
	if err != nil {
		if db.NotFound(err) {
			return nil, unprocessable("no_setup", "Start the setup first")
		}
		return nil, internal(err)
	}
	secret, err := mfa.Decrypt(s.MFAKey, u.Id.String(), rec.SecretEnc)
	if err != nil {
		return nil, internal(err)
	}
	step, ok := mfa.Verify(secret, req.Body.Code, s.Now())
	if !ok {
		return nil, unprocessable("invalid_code", "The code is wrong")
	}
	if used, err := s.DB.MfaUseStep(ctx, u.Id, step); err != nil {
		return nil, internal(err)
	} else if !used {
		return nil, unprocessable("invalid_code", "The code is wrong")
	}
	if err := s.DB.MfaSecretConfirm(ctx, u.Id); err != nil {
		return nil, internal(err)
	}
	codes := mfa.NewRecoveryCodes()
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = mfa.HashRecoveryCode(s.MFAKey, c)
	}
	if err := s.DB.RecoveryCodesReplace(ctx, u.Id, hashes); err != nil {
		return nil, internal(err)
	}
	s.audit(ctx, "mfa_enabled", &u.Id, nil)
	return api.MfaSetupConfirm200JSONResponse{RecoveryCodes: codes, Token: s.issueToken(u, true, true)}, nil
}

// MfaDisable removes MFA (decision 22): password and a valid code or recovery code are required. All sessions end.
func (s *Server) MfaDisable(ctx context.Context, req api.MfaDisableRequestObject) (api.MfaDisableResponseObject, error) {
	_, u, err := s.me(ctx)
	if err != nil {
		return nil, err
	}
	if !u.MfaEnabled {
		return nil, conflict("mfa_not_active", "MFA is not active")
	}
	b := req.Body
	ok, lockErr := s.checkPassword(ctx, u, b.Password)
	if lockErr != nil {
		return nil, lockErr
	}
	if ok {
		ok, err = s.secondFactor(ctx, u.Id, b.Code, b.RecoveryCode)
		if err != nil {
			return nil, internal(err)
		}
	}
	if !ok {
		if err := s.recordFailure(ctx, u.Id); err != nil {
			return nil, internal(err)
		}
		return nil, unprocessable("invalid_credentials", "Password or code is wrong")
	}
	if err := s.DB.MfaDelete(ctx, u.Id); err != nil {
		return nil, internal(err)
	}
	if err := s.DB.UserRevokeSessions(ctx, u.Id); err != nil {
		return nil, internal(err)
	}
	s.audit(ctx, "mfa_disabled", &u.Id, nil)
	return api.MfaDisable204Response{}, nil
}

// MfaRegenerateRecoveryCodes replaces the recovery codes after a valid TOTP code.
func (s *Server) MfaRegenerateRecoveryCodes(ctx context.Context, req api.MfaRegenerateRecoveryCodesRequestObject) (api.MfaRegenerateRecoveryCodesResponseObject, error) {
	_, u, err := s.me(ctx)
	if err != nil {
		return nil, err
	}
	if !u.MfaEnabled {
		return nil, conflict("mfa_not_active", "MFA is not active")
	}
	if l, wait := s.locked(u); l {
		e := newErr(http.StatusTooManyRequests, "temporarily_locked", "Too many failed attempts")
		e.RetryAfter = wait
		return nil, e
	}
	ok, err := s.totp(ctx, u.Id, req.Body.Code, true)
	if err != nil {
		return nil, internal(err)
	}
	if !ok {
		if err := s.recordFailure(ctx, u.Id); err != nil {
			return nil, internal(err)
		}
		return nil, unprocessable("invalid_code", "The code is wrong")
	}
	codes := mfa.NewRecoveryCodes()
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = mfa.HashRecoveryCode(s.MFAKey, c)
	}
	if err := s.DB.RecoveryCodesReplace(ctx, u.Id, hashes); err != nil {
		return nil, internal(err)
	}
	return api.MfaRegenerateRecoveryCodes200JSONResponse{RecoveryCodes: codes}, nil
}

// DeleteOwnAccount deletes the own account (UF-13) after password and MFA code. The last system administrator
// cannot delete the account.
func (s *Server) DeleteOwnAccount(ctx context.Context, req api.DeleteOwnAccountRequestObject) (api.DeleteOwnAccountResponseObject, error) {
	id, u, err := s.me(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	ok, lockErr := s.checkPassword(ctx, u, b.Password)
	if lockErr != nil {
		return nil, lockErr
	}
	if ok {
		ok, err = s.totp(ctx, u.Id, b.Code, true)
		if err != nil {
			return nil, internal(err)
		}
	}
	if !ok {
		if err := s.recordFailure(ctx, u.Id); err != nil {
			return nil, internal(err)
		}
		return nil, unprocessable("invalid_credentials", "Password or code is wrong")
	}
	if id.has(string(mwclient.Systemadmin)) {
		if last, err := s.lastAdmin(ctx); err != nil {
			return nil, internal(err)
		} else if last {
			return nil, unprocessable("last_admin", "The last system administrator cannot be deleted")
		}
	}
	if err := s.deleteAccount(ctx, u); err != nil {
		return nil, err
	}
	return api.DeleteOwnAccount204Response{}, nil
}

// deleteAccount runs the deletion in the middleware (cancel future bookings, detach past ones, pseudonymise,
// delete MFA data), writes the audit row and sends the confirmation to the old address.
func (s *Server) deleteAccount(ctx context.Context, u *db.UserCredentials) error {
	if _, err := s.DB.AccountDelete(ctx, u.Id); err != nil {
		return internal(err)
	}
	cat := "account"
	s.audit(ctx, "account_deleted", &u.Id, &cat)
	s.Mail.AccountDeleted(ctx, string(u.Email), u.FirstName)
	return nil
}
