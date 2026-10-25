package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/juliandevelops-studium/my-doctors-date/services/database_middleware/internal/api"
)

// maxWindowDays bounds calendarGetWindows.
const maxWindowDays = 63

const bookingFrom = `FROM bookings b
  JOIN services s ON s.id = b.service_id
  LEFT JOIN users du ON du.id = b.doctor_id
  LEFT JOIN users pu ON pu.id = b.patient_id`

const bookingColumns = `SELECT b.id, b.service_id, s.name, b.doctor_id, du.first_name, du.last_name, b.patient_id,
  pu.first_name, pu.last_name, pu.email, pu.phone, b.starts_at, b.ends_at, b.status, b.reason, b.info `

// Soft-deleted bookings are never visible. The owner filters ($2 patient, $3 doctor) are applied in SQL.
const bookingOwned = `b.id = $1 AND b.deleted_at IS NULL AND ($2::uuid IS NULL OR b.patient_id = $2) AND ($3::uuid IS NULL OR b.doctor_id = $3)`

func scanBooking(row pgx.Row) (api.BookingRecord, error) {
	var b api.BookingRecord
	var email *string
	var status string
	err := row.Scan(&b.Id, &b.ServiceId, &b.ServiceName, &b.DoctorId, &b.DoctorFirstName, &b.DoctorLastName, &b.PatientId,
		&b.PatientFirstName, &b.PatientLastName, &email, &b.PatientPhone, &b.StartsAt, &b.EndsAt, &status, &b.Reason, &b.Info)
	if err != nil {
		return b, mapErr(err)
	}
	b.Status = api.BookingStatus(status)
	if email != nil {
		e := toEmail(*email)
		b.PatientEmail = &e
	}
	return b, nil
}

func getBooking(ctx context.Context, q querier, id api.Uuid, ownerPatient, ownerDoctor *api.Uuid, forUpdate bool) (api.BookingRecord, error) {
	sql := bookingColumns + bookingFrom + ` WHERE ` + bookingOwned
	if forUpdate {
		sql += ` FOR UPDATE OF b`
	}
	return scanBooking(q.QueryRow(ctx, sql, id, ownerPatient, ownerDoctor))
}

// CalendarGetWindows returns working windows and busy intervals per doctor. No booking details.
func (s *Store) CalendarGetWindows(ctx context.Context, p api.WindowsParams) (api.WindowsResult, error) {
	res := api.WindowsResult{Doctors: []api.DoctorWindows{}}
	if !p.To.After(p.From) || p.To.Sub(p.From) > maxWindowDays*24*time.Hour {
		return res, unprocessable("invalid_range", "The range must be positive and at most 63 days")
	}
	rows, err := s.pool.Query(ctx, `SELECT u.id FROM doctors d JOIN users u ON u.id = d.user_id
		WHERE `+activeDoctor+` AND ($1::uuid IS NULL OR u.id = $1)
		  AND ($2::uuid IS NULL OR EXISTS (SELECT 1 FROM doctor_specialties ds WHERE ds.doctor_id = u.id AND ds.specialty_id = $2))
		ORDER BY u.id`, p.DoctorId, p.SpecialtyId)
	if err != nil {
		return res, mapErr(err)
	}
	var ids []string
	idx := map[api.Uuid]int{}
	for rows.Next() {
		var id api.Uuid
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return res, mapErr(err)
		}
		idx[id] = len(res.Doctors)
		ids = append(ids, id.String())
		res.Doctors = append(res.Doctors, api.DoctorWindows{DoctorId: id, Working: []api.Interval{}, Busy: []api.Interval{}})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, mapErr(err)
	}
	if len(ids) == 0 {
		return res, nil
	}
	wr, err := s.pool.Query(ctx, `SELECT w.doctor_id, ((g.day + w.start_time) AT TIME ZONE $4::text) AS s, ((g.day + w.end_time) AT TIME ZONE $4::text) AS e
		FROM (SELECT generate_series(($2::timestamptz AT TIME ZONE $4::text)::date - 1, ($3::timestamptz AT TIME ZONE $4::text)::date, interval '1 day')::date AS day) g
		JOIN working_days w ON w.weekday = EXTRACT(ISODOW FROM g.day) AND w.doctor_id = ANY($1::uuid[])
		WHERE ((g.day + w.end_time) AT TIME ZONE $4::text) > $2 AND ((g.day + w.start_time) AT TIME ZONE $4::text) < $3
		ORDER BY w.doctor_id, s`, ids, p.From, p.To, s.tz)
	if err != nil {
		return res, mapErr(err)
	}
	err = scanIntervals(wr, func(id api.Uuid, iv api.Interval) {
		d := &res.Doctors[idx[id]]
		d.Working = append(d.Working, iv)
	})
	if err != nil {
		return res, err
	}
	br, err := s.pool.Query(ctx, `SELECT b.doctor_id, b.starts_at, b.ends_at FROM bookings b
		WHERE b.doctor_id = ANY($1::uuid[]) AND b.deleted_at IS NULL AND b.status IN ('angefragt', 'bestaetigt')
		  AND b.starts_at < $3 AND b.ends_at > $2 ORDER BY b.starts_at`, ids, p.From, p.To)
	if err != nil {
		return res, mapErr(err)
	}
	err = scanIntervals(br, func(id api.Uuid, iv api.Interval) {
		d := &res.Doctors[idx[id]]
		d.Busy = append(d.Busy, iv)
	})
	return res, err
}

// scanIntervals reads rows of (doctor id, start, end), hands each to add and closes the rows.
func scanIntervals(rows pgx.Rows, add func(id api.Uuid, iv api.Interval)) error {
	defer rows.Close()
	for rows.Next() {
		var id api.Uuid
		var iv api.Interval
		if err := rows.Scan(&id, &iv.StartsAt, &iv.EndsAt); err != nil {
			return mapErr(err)
		}
		add(id, iv)
	}
	return mapErr(rows.Err())
}

// BookingCreate inserts a booking. The exclusion constraint rejects an overlap with 409 slot_taken.
func (s *Store) BookingCreate(ctx context.Context, p api.BookingInsert) (api.BookingRecord, error) {
	var out api.BookingRecord
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if p.DoctorId != nil {
			ok, err := isDoctor(ctx, tx, *p.DoctorId)
			if err != nil {
				return err
			}
			if !ok {
				return unprocessable("not_a_doctor", "The doctor must be an active doctor")
			}
		}
		var id api.Uuid
		err := tx.QueryRow(ctx, `INSERT INTO bookings (patient_id, doctor_id, service_id, starts_at, ends_at, reason, info)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			p.PatientId, p.DoctorId, p.ServiceId, p.StartsAt, p.EndsAt, nilIfEmpty(p.Reason), nilIfEmpty(p.Info)).Scan(&id)
		if err != nil {
			return refAsNotFound(err)
		}
		out, err = getBooking(ctx, tx, id, nil, nil, false)
		return err
	})
	return out, err
}

const bookingListWhere = `WHERE b.deleted_at IS NULL
  AND ($1::uuid IS NULL OR b.patient_id = $1)
  AND ($2::uuid IS NULL OR b.doctor_id = $2)
  AND ($3::uuid IS NULL OR b.patient_id = $3)
  AND ($4::uuid IS NULL OR b.doctor_id = $4)
  AND ($5::bool IS NOT TRUE OR b.doctor_id IS NULL)
  AND ($6::text IS NULL OR ($6 = 'upcoming' AND b.ends_at > now()) OR ($6 = 'past' AND b.ends_at <= now()))
  AND ($7::timestamptz IS NULL OR b.ends_at > $7)
  AND ($8::timestamptz IS NULL OR b.starts_at < $8)`

// BookingList lists bookings. owner_* filters are the ownership rule of the caller.
func (s *Store) BookingList(ctx context.Context, p api.BookingListParams) (api.BookingPage, error) {
	var when *string
	if p.When != nil {
		w := string(*p.When)
		when = &w
	}
	limit, offset := limitOffset(p.Limit, p.Offset)
	args := []any{p.OwnerPatientId, p.OwnerDoctorId, p.PatientId, p.DoctorId, p.Unassigned, when, p.From, p.To}
	page := api.BookingPage{Items: []api.BookingRecord{}}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) `+bookingFrom+` `+bookingListWhere, args...).Scan(&page.Total); err != nil {
		return page, mapErr(err)
	}
	rows, err := s.pool.Query(ctx, bookingColumns+bookingFrom+` `+bookingListWhere+` ORDER BY b.starts_at, b.id LIMIT $9 OFFSET $10`, append(args, limit, offset)...)
	if err != nil {
		return page, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		b, err := scanBooking(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, b)
	}
	return page, mapErr(rows.Err())
}

// BookingGet loads one booking. No match (missing, soft-deleted or foreign) is ErrNotFound.
func (s *Store) BookingGet(ctx context.Context, p api.BookingGetParams) (api.BookingRecord, error) {
	return getBooking(ctx, s.pool, p.BookingId, p.OwnerPatientId, p.OwnerDoctorId, false)
}

func futureBooking(ctx context.Context, tx pgx.Tx, id api.Uuid, ownerPatient, ownerDoctor *api.Uuid) (api.BookingRecord, error) {
	b, err := getBooking(ctx, tx, id, ownerPatient, ownerDoctor, true)
	if err != nil {
		return b, err
	}
	if !b.EndsAt.After(time.Now()) {
		return b, conflict("booking_in_past", "Only future bookings can be changed")
	}
	return b, nil
}

// BookingUpdate changes service, time, reason or info of a future booking.
func (s *Store) BookingUpdate(ctx context.Context, p api.BookingUpdateParams) (api.BookingRecord, error) {
	if (p.StartsAt == nil) != (p.EndsAt == nil) {
		return api.BookingRecord{}, unprocessable("time_pair_required", "starts_at and ends_at must be changed together")
	}
	var out api.BookingRecord
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := futureBooking(ctx, tx, p.BookingId, nil, nil); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE bookings SET service_id = COALESCE($2, service_id), starts_at = COALESCE($3, starts_at), ends_at = COALESCE($4, ends_at),
			reason = CASE WHEN $5::text IS NULL THEN reason ELSE NULLIF($5, '') END,
			info = CASE WHEN $6::text IS NULL THEN info ELSE NULLIF($6, '') END, updated_at = now() WHERE id = $1`,
			p.BookingId, p.ServiceId, p.StartsAt, p.EndsAt, p.Reason, p.Info)
		if err != nil {
			return refAsNotFound(err)
		}
		out, err = getBooking(ctx, tx, p.BookingId, nil, nil, false)
		return err
	})
	return out, err
}

// BookingCancel cancels a future booking: status storniert, deleted_at set, reason and info cleared.
// The returned record still carries patient and service data for the cancellation mail.
func (s *Store) BookingCancel(ctx context.Context, p api.BookingGetParams) (api.BookingRecord, error) {
	var out api.BookingRecord
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		b, err := futureBooking(ctx, tx, p.BookingId, p.OwnerPatientId, p.OwnerDoctorId)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE bookings SET status = 'storniert', deleted_at = now(), reason = NULL, info = NULL, updated_at = now() WHERE id = $1`, p.BookingId); err != nil {
			return mapErr(err)
		}
		b.Status, b.Reason, b.Info = api.Storniert, nil, nil
		out = b
		return nil
	})
	return out, err
}

// BookingAssignDoctor sets the doctor of a future booking. A busy doctor gives 409 slot_taken.
func (s *Store) BookingAssignDoctor(ctx context.Context, p api.BookingAssignParams) (api.BookingRecord, error) {
	var out api.BookingRecord
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := futureBooking(ctx, tx, p.BookingId, nil, nil); err != nil {
			return err
		}
		ok, err := isDoctor(ctx, tx, p.DoctorId)
		if err != nil {
			return err
		}
		if !ok {
			return unprocessable("not_a_doctor", "The doctor must be an active doctor")
		}
		if _, err := tx.Exec(ctx, `UPDATE bookings SET doctor_id = $2, updated_at = now() WHERE id = $1`, p.BookingId, p.DoctorId); err != nil {
			return mapErr(err)
		}
		out, err = getBooking(ctx, tx, p.BookingId, nil, nil, false)
		return err
	})
	return out, err
}
