package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/juliandevelops-studium/my-doctors-date/services/database_middleware/internal/api"
)

func readRetention(ctx context.Context, q querier) (api.Retention, error) {
	var r api.Retention
	rows, err := q.Query(ctx, `SELECT key, value_int FROM config WHERE key LIKE 'retention\_%'`)
	if err != nil {
		return r, mapErr(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var k string
		var v int
		if err := rows.Scan(&k, &v); err != nil {
			return r, mapErr(err)
		}
		n++
		switch k {
		case "retention_a_days":
			r.RetentionADays = v
		case "retention_b_days":
			r.RetentionBDays = v
		case "retention_c_days":
			r.RetentionCDays = v
		case "retention_d_days":
			r.RetentionDDays = v
		case "retention_e_days":
			r.RetentionEDays = v
		}
	}
	if err := rows.Err(); err != nil {
		return r, mapErr(err)
	}
	if n != 5 {
		return r, unprocessable("config_incomplete", "Retention settings are incomplete")
	}
	return r, nil
}

// RetentionGet reads the retention periods A to E.
func (s *Store) RetentionGet(ctx context.Context) (api.Retention, error) {
	return readRetention(ctx, s.pool)
}

// RetentionSet stores the retention periods A to E. C must not be shorter than B.
func (s *Store) RetentionSet(ctx context.Context, p api.RetentionSetParams) (api.Retention, error) {
	if p.RetentionCDays < p.RetentionBDays {
		return api.Retention{}, unprocessable("c_before_b", "retention_c_days must be at least retention_b_days")
	}
	var out api.Retention
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE config c SET value_int = v.val, updated_by = $6, updated_at = now()
			FROM (VALUES ('retention_a_days', $1::int), ('retention_b_days', $2::int), ('retention_c_days', $3::int),
			             ('retention_d_days', $4::int), ('retention_e_days', $5::int)) AS v(key, val)
			WHERE c.key = v.key`, p.RetentionADays, p.RetentionBDays, p.RetentionCDays, p.RetentionDDays, p.RetentionEDays, p.UpdatedBy)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() != 5 {
			return unprocessable("config_incomplete", "Retention settings are incomplete")
		}
		out, err = readRetention(ctx, tx)
		return err
	})
	return out, err
}

// AuditWrite appends an audit row. It never carries appointment contents.
func (s *Store) AuditWrite(ctx context.Context, p api.AuditWriteParams) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_log (actor_user_id, action, data_category, target_user_id, affected_count) VALUES ($1, $2, $3, $4, $5)`,
		p.ActorUserId, p.Action, p.DataCategory, p.TargetUserId, p.AffectedCount)
	return mapErr(err)
}

// AuditList reads audit rows, newest first.
func (s *Store) AuditList(ctx context.Context, p api.AuditListParams) (api.AuditPage, error) {
	const where = `WHERE ($1::timestamptz IS NULL OR occurred_at >= $1) AND ($2::timestamptz IS NULL OR occurred_at < $2) AND ($3::text IS NULL OR action = $3)`
	limit, offset := limitOffset(p.Limit, p.Offset)
	page := api.AuditPage{Items: []api.AuditEntry{}}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log `+where, p.From, p.To, p.Action).Scan(&page.Total); err != nil {
		return page, mapErr(err)
	}
	rows, err := s.pool.Query(ctx, `SELECT id, occurred_at, actor_user_id, action, data_category, target_user_id, affected_count FROM audit_log `+
		where+` ORDER BY occurred_at DESC, id DESC LIMIT $4 OFFSET $5`, p.From, p.To, p.Action, limit, offset)
	if err != nil {
		return page, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var e api.AuditEntry
		if err := rows.Scan(&e.Id, &e.OccurredAt, &e.ActorUserId, &e.Action, &e.DataCategory, &e.TargetUserId, &e.AffectedCount); err != nil {
			return page, mapErr(err)
		}
		page.Items = append(page.Items, e)
	}
	return page, mapErr(rows.Err())
}

// PurgeExpired hard-deletes expired rows of one category and writes one audit row.
func (s *Store) PurgeExpired(ctx context.Context, p api.PurgeParams) (api.PurgeResult, error) {
	var res api.PurgeResult
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		r, err := readRetention(ctx, tx)
		if err != nil {
			return err
		}
		var sql string
		var arg any = 0
		switch p.Category {
		case api.BookingsCancelled:
			sql, arg = `DELETE FROM bookings WHERE status = 'storniert' AND deleted_at < now() - make_interval(days => $1)`, r.RetentionADays
		case api.BookingsCompletedContent:
			sql, arg = `UPDATE bookings SET reason = NULL, info = NULL, updated_at = now() WHERE status = 'abgeschlossen'
				AND ends_at < now() - make_interval(days => $1) AND (reason IS NOT NULL OR info IS NOT NULL)`, r.RetentionBDays
		case api.BookingsCompleted:
			sql, arg = `DELETE FROM bookings WHERE status = 'abgeschlossen' AND ends_at < now() - make_interval(days => $1)`, r.RetentionCDays
		case api.Users:
			sql, arg = `DELETE FROM users WHERE status = 'disabled' AND deleted_at < now() - make_interval(days => $1)
				AND NOT EXISTS (SELECT 1 FROM bookings b WHERE b.doctor_id = users.id)`, r.RetentionDDays
		case api.AuditLog:
			sql, arg = `DELETE FROM audit_log WHERE occurred_at < now() - make_interval(days => $1)`, r.RetentionEDays
		case api.Tokens:
			sql = `DELETE FROM token_revocations WHERE expires_at < now() AND $1::int = 0`
		case api.EmailTokens:
			sql = `DELETE FROM email_tokens WHERE (expires_at < now() OR used_at IS NOT NULL) AND $1::int = 0`
		default:
			return unprocessable("unknown_category", "Unknown purge category")
		}
		tag, err := tx.Exec(ctx, sql, arg)
		if err != nil {
			return mapErr(err)
		}
		res.AffectedCount = int(tag.RowsAffected())
		cat := string(p.Category)
		_, err = tx.Exec(ctx, `INSERT INTO audit_log (action, data_category, affected_count) VALUES ('purge', $1, $2)`, cat, res.AffectedCount)
		return mapErr(err)
	})
	return res, err
}

// BookingsCompletePast marks ended, confirmed bookings as abgeschlossen.
func (s *Store) BookingsCompletePast(ctx context.Context) (api.PurgeResult, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE bookings SET status = 'abgeschlossen', updated_at = now()
		WHERE status = 'bestaetigt' AND deleted_at IS NULL AND ends_at <= now()`)
	return api.PurgeResult{AffectedCount: int(tag.RowsAffected())}, mapErr(err)
}
