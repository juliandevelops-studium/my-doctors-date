package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"

	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/api"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/db"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mwclient"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/password"
)

// AdminListUsers lists accounts. It returns no appointment data (decision 15: the admin has no access to them).
func (s *Server) AdminListUsers(ctx context.Context, req api.AdminListUsersRequestObject) (api.AdminListUsersResponseObject, error) {
	p := mwclient.UserListParams{Q: req.Params.Q, Limit: (*int)(req.Params.Limit), Offset: (*int)(req.Params.Offset)}
	if req.Params.Role != nil {
		r := mwclient.Role(*req.Params.Role)
		p.Role = &r
	}
	if req.Params.Status != nil {
		st := mwclient.UserStatus(*req.Params.Status)
		p.Status = &st
	}
	page, err := s.DB.UserList(ctx, p)
	if err != nil {
		return nil, internal(err)
	}
	out := api.UserPage{Items: make([]api.AdminUser, len(page.Items)), Total: page.Total}
	for i := range page.Items {
		out.Items[i] = adminUserOf(&page.Items[i])
	}
	return api.AdminListUsers200JSONResponse(out), nil
}

// AdminCreateUser creates an active account with a random unusable password and sends a reset link (UF-10).
// The administrator never sees or sets a password.
func (s *Server) AdminCreateUser(ctx context.Context, req api.AdminCreateUserRequestObject) (api.AdminCreateUserResponseObject, error) {
	b := req.Body
	throwaway := make([]byte, 32)
	if _, err := rand.Read(throwaway); err != nil {
		return nil, internal(err)
	}
	hash, err := password.Hash(base64.RawStdEncoding.EncodeToString(throwaway))
	if err != nil {
		return nil, internal(err)
	}
	u, err := s.DB.UserCreate(ctx, mwclient.UserCreate{
		FirstName: b.FirstName, LastName: b.LastName, Email: mwEmail(normalizeEmail(string(b.Email))), Phone: b.Phone,
		PasswordHash: hash, Roles: mwRoles(b.Roles), Status: mwclient.Active, MustChangePassword: true,
	})
	if err != nil {
		switch db.Status(err) {
		case http.StatusConflict:
			return nil, conflict("email_taken", "This address is already in use")
		case http.StatusUnprocessableEntity:
			return nil, unprocessable(db.Code(err), "Invalid roles")
		}
		return nil, internal(err)
	}
	if err := s.sendLink(ctx, u.Id, mwclient.EmailTokenCreateParamsPurposeReset, adminResetTokenTTL, nil, func(tok string) {
		s.Mail.PasswordReset(ctx, string(u.Email), u.FirstName, tok)
	}); err != nil {
		return nil, internal(err)
	}
	s.audit(ctx, "user_created", &u.Id, nil)
	return api.AdminCreateUser201JSONResponse{Body: adminUserOf(u), Headers: api.AdminCreateUser201ResponseHeaders{Location: "/v1/admin/users/" + u.Id.String()}}, nil
}

// AdminGetUser shows one account.
func (s *Server) AdminGetUser(ctx context.Context, req api.AdminGetUserRequestObject) (api.AdminGetUserResponseObject, error) {
	u, err := s.DB.UserGetByID(ctx, req.UserId)
	if err != nil {
		if db.NotFound(err) {
			return nil, notFound()
		}
		return nil, internal(err)
	}
	rec := mwclient.UserRecord{
		Id: u.Id, FirstName: u.FirstName, LastName: u.LastName, Email: u.Email, Roles: u.Roles, Status: u.Status,
		LockedUntil: u.LockedUntil, PermanentlyLocked: u.PermanentlyLocked, MfaEnabled: u.MfaEnabled, CreatedAt: u.CreatedAt,
	}
	return api.AdminGetUser200JSONResponse(adminUserOf(&rec)), nil
}

// AdminSetRoles replaces the roles (UF-10). systemadmin must be the only role (decision 9). Sessions of the user
// end because the roles are part of the token.
func (s *Server) AdminSetRoles(ctx context.Context, req api.AdminSetRolesRequestObject) (api.AdminSetRolesResponseObject, error) {
	id, _ := identityFrom(ctx)
	if req.UserId == id.UserID {
		return nil, unprocessable("own_roles", "Administrators cannot change their own roles")
	}
	target, err := s.DB.UserGetByID(ctx, req.UserId)
	if err != nil {
		if db.NotFound(err) {
			return nil, notFound()
		}
		return nil, internal(err)
	}
	if hasRole(target.Roles, mwclient.Systemadmin) && !hasAPIRole(req.Body.Roles, api.Systemadmin) {
		if last, err := s.lastAdmin(ctx); err != nil {
			return nil, internal(err)
		} else if last {
			return nil, unprocessable("last_admin", "The last system administrator cannot lose the role")
		}
	}
	u, err := s.DB.UserSetRoles(ctx, req.UserId, mwRoles(req.Body.Roles))
	if err != nil {
		switch db.Status(err) {
		case http.StatusNotFound:
			return nil, notFound()
		case http.StatusUnprocessableEntity:
			return nil, unprocessable(db.Code(err), "Invalid roles")
		}
		return nil, internal(err)
	}
	s.audit(ctx, "roles_changed", &u.Id, nil)
	return api.AdminSetRoles200JSONResponse(adminUserOf(u)), nil
}

// AdminResetPassword sends a reset link and ends all sessions of the user (UF-10).
func (s *Server) AdminResetPassword(ctx context.Context, req api.AdminResetPasswordRequestObject) (api.AdminResetPasswordResponseObject, error) {
	u, err := s.DB.UserGetByID(ctx, req.UserId)
	if err != nil {
		if db.NotFound(err) {
			return nil, notFound()
		}
		return nil, internal(err)
	}
	if u.Status == mwclient.Disabled {
		return nil, notFound()
	}
	if err := s.sendLink(ctx, u.Id, mwclient.EmailTokenCreateParamsPurposeReset, adminResetTokenTTL, nil, func(tok string) {
		s.Mail.PasswordReset(ctx, string(u.Email), u.FirstName, tok)
	}); err != nil {
		return nil, internal(err)
	}
	if err := s.DB.UserRevokeSessions(ctx, u.Id); err != nil {
		return nil, internal(err)
	}
	s.audit(ctx, "password_reset_requested", &u.Id, nil)
	return api.AdminResetPassword202Response{}, nil
}

// AdminUnlockUser lifts a lock (SR.05).
func (s *Server) AdminUnlockUser(ctx context.Context, req api.AdminUnlockUserRequestObject) (api.AdminUnlockUserResponseObject, error) {
	if err := s.DB.UserUnlock(ctx, req.UserId); err != nil {
		if db.NotFound(err) {
			return nil, notFound()
		}
		return nil, internal(err)
	}
	s.audit(ctx, "user_unlocked", &req.UserId, nil)
	return api.AdminUnlockUser204Response{}, nil
}

// AdminDeleteUser deletes another account (UF-13).
func (s *Server) AdminDeleteUser(ctx context.Context, req api.AdminDeleteUserRequestObject) (api.AdminDeleteUserResponseObject, error) {
	id, _ := identityFrom(ctx)
	if req.UserId == id.UserID {
		return nil, unprocessable("own_account", "Use the own account deletion instead")
	}
	u, err := s.DB.UserGetByID(ctx, req.UserId)
	if err != nil {
		if db.NotFound(err) {
			return nil, notFound()
		}
		return nil, internal(err)
	}
	if hasRole(u.Roles, mwclient.Systemadmin) {
		if last, err := s.lastAdmin(ctx); err != nil {
			return nil, internal(err)
		} else if last {
			return nil, unprocessable("last_admin", "The last system administrator cannot be deleted")
		}
	}
	if err := s.deleteAccount(ctx, u); err != nil {
		return nil, err
	}
	return api.AdminDeleteUser204Response{}, nil
}

// AdminListAuditLog reads the audit log. It contains no appointment contents.
func (s *Server) AdminListAuditLog(ctx context.Context, req api.AdminListAuditLogRequestObject) (api.AdminListAuditLogResponseObject, error) {
	page, err := s.DB.AuditList(ctx, mwclient.AuditListParams{From: req.Params.From, To: req.Params.To, Action: req.Params.Action,
		Limit: (*int)(req.Params.Limit), Offset: (*int)(req.Params.Offset)})
	if err != nil {
		return nil, internal(err)
	}
	out := api.AuditPage{Items: make([]api.AuditEntry, len(page.Items)), Total: page.Total}
	for i, e := range page.Items {
		out.Items[i] = api.AuditEntry{Id: e.Id, OccurredAt: e.OccurredAt, ActorUserId: e.ActorUserId, Action: e.Action,
			DataCategory: e.DataCategory, TargetUserId: e.TargetUserId, AffectedCount: e.AffectedCount}
	}
	return api.AdminListAuditLog200JSONResponse(out), nil
}

// AdminGetRetention reads the retention periods.
func (s *Server) AdminGetRetention(ctx context.Context, _ api.AdminGetRetentionRequestObject) (api.AdminGetRetentionResponseObject, error) {
	r, err := s.DB.RetentionGet(ctx)
	if err != nil {
		return nil, internal(err)
	}
	return api.AdminGetRetention200JSONResponse(api.Retention(*r)), nil
}

// AdminSetRetention stores the retention periods; C must be at least B.
func (s *Server) AdminSetRetention(ctx context.Context, req api.AdminSetRetentionRequestObject) (api.AdminSetRetentionResponseObject, error) {
	id, _ := identityFrom(ctx)
	b := req.Body
	if b.RetentionCDays < b.RetentionBDays {
		return nil, validation("retention_c_days", "c_before_b")
	}
	r, err := s.DB.RetentionSet(ctx, mwclient.RetentionSetParams{
		RetentionADays: b.RetentionADays, RetentionBDays: b.RetentionBDays, RetentionCDays: b.RetentionCDays,
		RetentionDDays: b.RetentionDDays, RetentionEDays: b.RetentionEDays, UpdatedBy: id.UserID,
	})
	if err != nil {
		if db.Status(err) == http.StatusUnprocessableEntity {
			return nil, validation("retention_c_days", db.Code(err))
		}
		return nil, internal(err)
	}
	s.audit(ctx, "retention_changed", nil, nil)
	return api.AdminSetRetention200JSONResponse(api.Retention(*r)), nil
}

func hasRole(roles []mwclient.Role, want mwclient.Role) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

func hasAPIRole(roles []api.Role, want api.Role) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}
