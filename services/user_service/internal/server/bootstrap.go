package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mwclient"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/password"
)

// EnsureBootstrapAdmin creates the first system administrator (UF-03) if none exists. The password comes from a
// Kubernetes Secret created by `make up`. The account must change the password and set up MFA before anything else.
func (s *Server) EnsureBootstrapAdmin(ctx context.Context, email, pw string) (created bool, err error) {
	role, limit := mwclient.Systemadmin, 1
	page, err := s.DB.UserList(ctx, mwclient.UserListParams{Role: &role, Limit: &limit})
	if err != nil {
		return false, err
	}
	if page.Total > 0 {
		return false, nil
	}
	if v := password.Validate(pw, email); len(v) > 0 {
		return false, fmt.Errorf("bootstrap admin password violates the policy: %w", errors.New(v[0].Code))
	}
	hash, err := password.Hash(pw)
	if err != nil {
		return false, err
	}
	if _, err := s.DB.UserCreate(ctx, mwclient.UserCreate{
		FirstName: "System", LastName: "Administrator", Email: mwEmail(email), PasswordHash: hash,
		Roles: []mwclient.Role{mwclient.Systemadmin}, Status: mwclient.Active, MustChangePassword: true,
	}); err != nil {
		return false, err
	}
	return true, nil
}
