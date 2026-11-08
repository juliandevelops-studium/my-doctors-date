package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/juliandevelops-studium/my-doctors-date/services/database_middleware/internal/api"
)

const (
	// userColumns lists the selected columns and the FROM clause. Callers prepend SELECT and append a WHERE clause.
	// Roles are aggregated into an array, mfa is true once a secret is confirmed, and the patient columns are NULL
	// for users without a patients row.
	userColumns = `u.id, u.first_name, u.last_name, u.email, u.phone, u.status, u.email_verified_at, u.failed_attempts,
  u.locked_until, u.permanently_locked, u.must_change_password, u.sessions_valid_after, u.created_at, u.password_hash,
  COALESCE((SELECT array_agg(r.role ORDER BY r.role) FROM user_roles r WHERE r.user_id = u.id), '{}'::text[]),
  EXISTS (SELECT 1 FROM mfa_secrets m WHERE m.user_id = u.id AND m.confirmed_at IS NOT NULL),
  p.street, p.postal_code, p.city, (p.user_id IS NOT NULL)
  FROM users u LEFT JOIN patients p ON p.user_id = u.id`

	// userListWhere filters the user list. $1 is a search pattern, $2 a role, $3 a status; a NULL parameter disables
	// its filter.
	userListWhere = `WHERE u.deleted_at IS NULL
  AND ($1::text IS NULL OR u.first_name ILIKE $1 OR u.last_name ILIKE $1 OR u.email ILIKE $1 OR (u.first_name || ' ' || u.last_name) ILIKE $1)
  AND ($2::text IS NULL OR EXISTS (SELECT 1 FROM user_roles r WHERE r.user_id = u.id AND r.role = $2))
  AND ($3::text IS NULL OR u.status = $3)`
)

// userRow is one result row of userColumns.
type userRow struct {
	id                     api.Uuid
	first, last, email     string
	phone                  *string
	status                 string
	verifiedAt             *time.Time
	failed                 int
	lockedUntil            *time.Time
	permLocked, mustChange bool
	validAfter, createdAt  time.Time
	hash                   string
	roles                  []string
	mfa                    bool
	street, postal, city   *string
	isPatient              bool
}

// scanUser reads one row selected with userColumns.
func scanUser(row pgx.Row) (*userRow, error) {
	var u userRow
	err := row.Scan(&u.id, &u.first, &u.last, &u.email, &u.phone, &u.status, &u.verifiedAt, &u.failed, &u.lockedUntil,
		&u.permLocked, &u.mustChange, &u.validAfter, &u.createdAt, &u.hash, &u.roles, &u.mfa, &u.street, &u.postal, &u.city, &u.isPatient)
	if err != nil {
		return nil, mapErr(err)
	}
	return &u, nil
}

// record converts the row to the API type (without the password hash).
func (u *userRow) record() api.UserRecord {
	roles := make([]api.Role, len(u.roles))
	for i, r := range u.roles {
		roles[i] = api.Role(r)
	}
	r := api.UserRecord{
		Id: u.id, FirstName: u.first, LastName: u.last, Email: toEmail(u.email), Phone: u.phone,
		Status: api.UserStatus(u.status), EmailVerifiedAt: u.verifiedAt, FailedAttempts: &u.failed, LockedUntil: u.lockedUntil,
		PermanentlyLocked: u.permLocked, MustChangePassword: u.mustChange, MfaEnabled: u.mfa,
		SessionsValidAfter: u.validAfter, CreatedAt: u.createdAt, Roles: roles,
	}
	if u.isPatient {
		r.Address = &api.Address{Street: u.street, PostalCode: u.postal, City: u.city}
	}
	return r
}

// withCredentials is record plus the password hash, for the login flow.
func (u *userRow) withCredentials() api.UserWithCredentials {
	r := u.record()
	return api.UserWithCredentials{
		Id: r.Id, FirstName: r.FirstName, LastName: r.LastName, Email: r.Email, Phone: r.Phone, Status: r.Status,
		EmailVerifiedAt: r.EmailVerifiedAt, FailedAttempts: r.FailedAttempts, LockedUntil: r.LockedUntil,
		PermanentlyLocked: r.PermanentlyLocked, MustChangePassword: r.MustChangePassword, MfaEnabled: r.MfaEnabled,
		SessionsValidAfter: r.SessionsValidAfter, CreatedAt: r.CreatedAt, Roles: r.Roles, Address: r.Address,
		PasswordHash: u.hash,
	}
}

// checkRoles validates a role set: it must be non-empty, free of duplicates, and systemadmin must stand alone.
func checkRoles(roles []api.Role) error {
	seen := map[api.Role]bool{}
	for _, r := range roles {
		if seen[r] {
			return unprocessable("duplicate_role", "Role listed twice")
		}
		seen[r] = true
	}
	if seen[api.RoleSystemadmin] && len(roles) > 1 {
		return unprocessable("systemadmin_exclusive", "The role systemadmin cannot be combined with other roles")
	}
	if len(roles) == 0 {
		return unprocessable("roles_required", "At least one role is required")
	}
	return nil
}

// roleStrings converts roles to plain strings for use as a text[] query parameter.
func roleStrings(roles []api.Role) []string {
	out := make([]string, len(roles))
	for i, r := range roles {
		out[i] = string(r)
	}
	return out
}

// ensureRoleRows keeps the patients and doctors rows in sync with the roles. Rows are never removed on role loss:
// bookings still reference them.
func ensureRoleRows(ctx context.Context, tx pgx.Tx, id api.Uuid, roles []api.Role, addr *api.Address) error {
	for _, r := range roles {
		switch r {
		case api.RolePatient:
			var street, postal, city *string
			if addr != nil {
				street, postal, city = nilIfEmpty(addr.Street), nilIfEmpty(addr.PostalCode), nilIfEmpty(addr.City)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO patients (user_id, street, postal_code, city) VALUES ($1, $2, $3, $4) ON CONFLICT (user_id) DO NOTHING`,
				id, street, postal, city); err != nil {
				return mapErr(err)
			}
		case api.RoleDoctor:
			if _, err := tx.Exec(ctx, `INSERT INTO doctors (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING`, id); err != nil {
				return mapErr(err)
			}
		}
	}
	return nil
}

// getUser loads one non-deleted user. where is a trusted SQL condition on alias u with a single parameter $1.
func getUser(ctx context.Context, q querier, where string, arg any) (*userRow, error) {
	return scanUser(q.QueryRow(ctx, `SELECT `+userColumns+` WHERE u.deleted_at IS NULL AND `+where, arg))
}

// UserCreate inserts an account with its roles (registration, admin create).
func (s *Store) UserCreate(ctx context.Context, p api.UserCreate) (api.UserRecord, error) {
	if err := checkRoles(p.Roles); err != nil {
		return api.UserRecord{}, err
	}
	var out api.UserRecord
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var id api.Uuid
		err := tx.QueryRow(ctx, `INSERT INTO users (first_name, last_name, email, phone, password_hash, status, must_change_password, email_verified_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, CASE WHEN $6 = 'active' THEN now() END) RETURNING id`,
			p.FirstName, p.LastName, string(p.Email), nilIfEmpty(p.Phone), p.PasswordHash, string(p.Status), p.MustChangePassword).Scan(&id)
		if err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role) SELECT $1, unnest($2::text[])`, id, roleStrings(p.Roles)); err != nil {
			return mapErr(err)
		}
		if err := ensureRoleRows(ctx, tx, id, p.Roles, p.Address); err != nil {
			return err
		}
		u, err := getUser(ctx, tx, `u.id = $1`, id)
		if err != nil {
			return err
		}
		out = u.record()
		return nil
	})
	return out, err
}

// UserGetByEmail loads an account including the password hash. The match is case-insensitive.
func (s *Store) UserGetByEmail(ctx context.Context, email string) (api.UserWithCredentials, error) {
	u, err := getUser(ctx, s.pool, `lower(u.email) = lower($1)`, email)
	if err != nil {
		return api.UserWithCredentials{}, err
	}
	return u.withCredentials(), nil
}

// UserGetByID loads an account including the password hash.
func (s *Store) UserGetByID(ctx context.Context, id api.Uuid) (api.UserWithCredentials, error) {
	u, err := getUser(ctx, s.pool, `u.id = $1`, id)
	if err != nil {
		return api.UserWithCredentials{}, err
	}
	return u.withCredentials(), nil
}

// UserList returns a page of accounts.
func (s *Store) UserList(ctx context.Context, p api.UserListParams) (api.UserPage, error) {
	var q, role, status *string
	if p.Q != nil && *p.Q != "" {
		l := escapeLike(*p.Q)
		q = &l
	}
	if p.Role != nil {
		r := string(*p.Role)
		role = &r
	}
	if p.Status != nil {
		st := string(*p.Status)
		status = &st
	}
	limit, offset := limitOffset(p.Limit, p.Offset)
	page := api.UserPage{Items: []api.UserRecord{}}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users u `+userListWhere, q, role, status).Scan(&page.Total); err != nil {
		return page, mapErr(err)
	}
	rows, err := s.pool.Query(ctx, `SELECT `+userColumns+` `+userListWhere+` ORDER BY lower(u.last_name), lower(u.first_name), u.id LIMIT $4 OFFSET $5`,
		q, role, status, limit, offset)
	if err != nil {
		return page, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, u.record())
	}
	return page, mapErr(rows.Err())
}

// UserUpdateProfile changes name, phone and address. An empty string clears phone or an address field.
func (s *Store) UserUpdateProfile(ctx context.Context, p api.UserProfileUpdate) (api.UserRecord, error) {
	var out api.UserRecord
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET first_name = COALESCE($2, first_name), last_name = COALESCE($3, last_name),
			phone = CASE WHEN $4::text IS NULL THEN phone ELSE NULLIF($4, '') END, updated_at = now()
			WHERE id = $1 AND deleted_at IS NULL`, p.UserId, p.FirstName, p.LastName, p.Phone)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if p.Address != nil {
			tag, err := tx.Exec(ctx, `UPDATE patients SET
				street = CASE WHEN $2::text IS NULL THEN street ELSE NULLIF($2, '') END,
				postal_code = CASE WHEN $3::text IS NULL THEN postal_code ELSE NULLIF($3, '') END,
				city = CASE WHEN $4::text IS NULL THEN city ELSE NULLIF($4, '') END WHERE user_id = $1`,
				p.UserId, p.Address.Street, p.Address.PostalCode, p.Address.City)
			if err != nil {
				return mapErr(err)
			}
			if tag.RowsAffected() == 0 {
				return unprocessable("not_a_patient", "Address data only exists for patients")
			}
		}
		u, err := getUser(ctx, tx, `u.id = $1`, p.UserId)
		if err != nil {
			return err
		}
		out = u.record()
		return nil
	})
	return out, err
}

// execOne runs a statement that must change exactly one user row and returns ErrNotFound if it matched none.
func (s *Store) execOne(ctx context.Context, sql string, args ...any) error {
	tag, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UserSetPassword stores a new hash and invalidates all sessions of the user.
func (s *Store) UserSetPassword(ctx context.Context, p api.SetPasswordParams) error {
	return s.execOne(ctx, `UPDATE users SET password_hash = $2, must_change_password = $3, sessions_valid_after = date_trunc('second', now()) + interval '1 second', updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, p.UserId, p.PasswordHash, p.MustChangePassword)
}

// UserSetEmailVerified confirms the address and activates a pending account.
func (s *Store) UserSetEmailVerified(ctx context.Context, id api.Uuid) error {
	return s.execOne(ctx, `UPDATE users SET email_verified_at = COALESCE(email_verified_at, now()),
		status = CASE WHEN status = 'pending' THEN 'active' ELSE status END, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, id)
}

// UserChangeEmail replaces the address. The new address was confirmed by the e-mail link.
func (s *Store) UserChangeEmail(ctx context.Context, p api.ChangeEmailParams) error {
	return s.execOne(ctx, `UPDATE users SET email = $2, email_verified_at = now(), updated_at = now() WHERE id = $1 AND deleted_at IS NULL`,
		p.UserId, string(p.NewEmail))
}

// UserSetRoles replaces the roles and revokes the sessions of the user (decision 13).
func (s *Store) UserSetRoles(ctx context.Context, p api.SetRolesParams) (api.UserRecord, error) {
	if err := checkRoles(p.Roles); err != nil {
		return api.UserRecord{}, err
	}
	var out api.UserRecord
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var id api.Uuid
		if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, p.UserId).Scan(&id); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, id); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role) SELECT $1, unnest($2::text[])`, id, roleStrings(p.Roles)); err != nil {
			return mapErr(err)
		}
		if err := ensureRoleRows(ctx, tx, id, p.Roles, nil); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET sessions_valid_after = date_trunc('second', now()) + interval '1 second', updated_at = now() WHERE id = $1`, id); err != nil {
			return mapErr(err)
		}
		u, err := getUser(ctx, tx, `u.id = $1`, id)
		if err != nil {
			return err
		}
		out = u.record()
		return nil
	})
	return out, err
}

// UserRecordLoginFailure counts a failed attempt atomically and applies the SR.05 lock.
func (s *Store) UserRecordLoginFailure(ctx context.Context, id api.Uuid) (api.LockState, error) {
	var st api.LockState
	err := s.pool.QueryRow(ctx, `UPDATE users SET
		failed_attempts = failed_attempts + 1,
		permanently_locked = permanently_locked OR failed_attempts + 1 >= 10,
		status = CASE WHEN failed_attempts + 1 >= 10 AND status = 'active' THEN 'locked' ELSE status END,
		locked_until = CASE WHEN failed_attempts + 1 >= 10 THEN locked_until
		                    WHEN failed_attempts + 1 = 3 THEN now() + interval '1 minute'
		                    WHEN failed_attempts + 1 > 3 THEN now() + interval '10 minutes'
		                    ELSE locked_until END,
		updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING failed_attempts, locked_until, permanently_locked`, id).Scan(&st.FailedAttempts, &st.LockedUntil, &st.PermanentlyLocked)
	return st, mapErr(err)
}

// UserRecordLoginSuccess resets the counter. A permanent lock is never lifted here.
func (s *Store) UserRecordLoginSuccess(ctx context.Context, id api.Uuid) error {
	return s.execOne(ctx, `UPDATE users SET failed_attempts = 0, locked_until = NULL, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL AND NOT permanently_locked`, id)
}

// UserUnlock clears counter and locks (system admin, SR.05).
func (s *Store) UserUnlock(ctx context.Context, id api.Uuid) error {
	return s.execOne(ctx, `UPDATE users SET failed_attempts = 0, locked_until = NULL, permanently_locked = false,
		status = CASE WHEN status = 'locked' THEN 'active' ELSE status END, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, id)
}

// UserRevokeSessions invalidates every token issued before now.
func (s *Store) UserRevokeSessions(ctx context.Context, id api.Uuid) error {
	return s.execOne(ctx, `UPDATE users SET sessions_valid_after = date_trunc('second', now()) + interval '1 second', updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, id)
}

// AccountDelete cancels future bookings, detaches past ones, deletes MFA and e-mail tokens, and pseudonymises and
// disables the account.
// Note that updated_at is not set to make deanonymizing impossible.
func (s *Store) AccountDelete(ctx context.Context, id api.Uuid) (api.AccountDeleteResult, error) {
	var res api.AccountDeleteResult
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var uid api.Uuid
		if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&uid); err != nil {
			return mapErr(err)
		}
		tag, err := tx.Exec(ctx, `UPDATE bookings SET status = 'storniert', deleted_at = now(), reason = NULL, info = NULL, updated_at = now()
			WHERE patient_id = $1 AND deleted_at IS NULL AND ends_at > now()`, id)
		if err != nil {
			return mapErr(err)
		}
		res.CancelledBookings = int(tag.RowsAffected())
		tag, err = tx.Exec(ctx, `UPDATE bookings SET patient_id = NULL WHERE patient_id = $1`, id)
		if err != nil {
			return mapErr(err)
		}
		res.AnonymisedBookings = int(tag.RowsAffected()) - res.CancelledBookings
		for _, q := range []string{
			`DELETE FROM mfa_secrets WHERE user_id = $1`,
			`DELETE FROM recovery_codes WHERE user_id = $1`,
			`DELETE FROM email_tokens WHERE user_id = $1`,
			`DELETE FROM user_roles WHERE user_id = $1`,
			`UPDATE patients SET street = NULL, postal_code = NULL, city = NULL, main_doctor_id = NULL WHERE user_id = $1`,
			`UPDATE users SET first_name = 'Gelöscht', last_name = 'Gelöscht', email = 'deleted-' || id::text || '@invalid.example',
				phone = NULL, password_hash = '$argon2id$deleted', status = 'disabled', deleted_at = now(),
				sessions_valid_after = date_trunc('second', now()) + interval '1 second', must_change_password = false, updated_at = now() WHERE id = $1`,
		} {
			if _, err := tx.Exec(ctx, q, id); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
	if res.AnonymisedBookings < 0 {
		res.AnonymisedBookings = 0
	}
	return res, err
}
