package server

import (
	"context"
	"strings"

	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mwclient"
)

func equalFold(a, b string) bool { return strings.EqualFold(a, b) }

// lastAdmin reports whether at most one active system administrator exists.
func (s *Server) lastAdmin(ctx context.Context) (bool, error) {
	role, status, limit := mwclient.Systemadmin, mwclient.Active, 1
	page, err := s.DB.UserList(ctx, mwclient.UserListParams{Role: &role, Status: &status, Limit: &limit})
	if err != nil {
		return false, err
	}
	return page.Total <= 1, nil
}
