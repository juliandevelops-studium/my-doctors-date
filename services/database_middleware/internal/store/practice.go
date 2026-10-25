package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/juliandevelops-studium/my-doctors-date/services/database_middleware/internal/api"
)

const activeDoctor = `EXISTS (SELECT 1 FROM user_roles r WHERE r.user_id = u.id AND r.role = 'arzt')
  AND u.deleted_at IS NULL AND u.status = 'active'`

// SpecialtyList returns all specialties.
func (s *Store) SpecialtyList(ctx context.Context) (api.SpecialtyList, error) {
	out := api.SpecialtyList{Items: []api.Specialty{}}
	rows, err := s.pool.Query(ctx, `SELECT id, name FROM specialties ORDER BY lower(name), id`)
	if err != nil {
		return out, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var sp api.Specialty
		if err := rows.Scan(&sp.Id, &sp.Name); err != nil {
			return out, mapErr(err)
		}
		out.Items = append(out.Items, sp)
	}
	return out, mapErr(rows.Err())
}

// SpecialtyCreate inserts a specialty.
func (s *Store) SpecialtyCreate(ctx context.Context, p api.SpecialtyInput) (api.Specialty, error) {
	var sp api.Specialty
	err := s.pool.QueryRow(ctx, `INSERT INTO specialties (name) VALUES ($1) RETURNING id, name`, p.Name).Scan(&sp.Id, &sp.Name)
	return sp, mapErr(err)
}

// SpecialtyDelete removes a specialty that no service or doctor uses.
func (s *Store) SpecialtyDelete(ctx context.Context, id api.Uuid) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM specialties WHERE id = $1`, id)
	if err != nil {
		return refAsConflict(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const serviceSelect = `SELECT sv.id, sv.name, sv.duration_minutes, sv.specialty_id, sp.name, sv.is_other
  FROM services sv LEFT JOIN specialties sp ON sp.id = sv.specialty_id`

func scanService(row pgx.Row) (api.Service, error) {
	var sv api.Service
	err := row.Scan(&sv.Id, &sv.Name, &sv.DurationMinutes, &sv.SpecialtyId, &sv.SpecialtyName, &sv.IsOther)
	return sv, mapErr(err)
}

// ServiceList returns the service list.
func (s *Store) ServiceList(ctx context.Context) (api.ServiceList, error) {
	out := api.ServiceList{Items: []api.Service{}}
	rows, err := s.pool.Query(ctx, serviceSelect+` ORDER BY sv.is_other, lower(sv.name), sv.id`)
	if err != nil {
		return out, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		sv, err := scanService(rows)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, sv)
	}
	return out, mapErr(rows.Err())
}

// ServiceCreate inserts a normal service (never "Sonstiges").
func (s *Store) ServiceCreate(ctx context.Context, p api.ServiceInput) (api.Service, error) {
	if p.SpecialtyId == nil {
		return api.Service{}, unprocessable("specialty_required", "A service needs a specialty")
	}
	var id api.Uuid
	err := s.pool.QueryRow(ctx, `INSERT INTO services (name, duration_minutes, specialty_id, is_other) VALUES ($1, $2, $3, false) RETURNING id`,
		p.Name, p.DurationMinutes, p.SpecialtyId).Scan(&id)
	if err != nil {
		return api.Service{}, mapErr(err)
	}
	return scanService(s.pool.QueryRow(ctx, serviceSelect+` WHERE sv.id = $1`, id))
}

// ServiceUpdate replaces name, duration and specialty. "Sonstiges" never gets a specialty.
func (s *Store) ServiceUpdate(ctx context.Context, p api.ServiceUpdateParams) (api.Service, error) {
	var isOther bool
	err := s.pool.QueryRow(ctx, `UPDATE services SET name = $2, duration_minutes = $3,
		specialty_id = CASE WHEN is_other THEN NULL ELSE $4::uuid END WHERE id = $1 RETURNING is_other`,
		p.Id, p.Name, p.DurationMinutes, p.SpecialtyId).Scan(&isOther)
	if err != nil {
		return api.Service{}, mapErr(err)
	}
	return scanService(s.pool.QueryRow(ctx, serviceSelect+` WHERE sv.id = $1`, p.Id))
}

// ServiceDelete removes a service that no booking references. "Sonstiges" cannot be deleted.
func (s *Store) ServiceDelete(ctx context.Context, id api.Uuid) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM services WHERE id = $1 AND NOT is_other`, id)
	if err != nil {
		return refAsConflict(err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM services WHERE id = $1)`, id).Scan(&exists); err != nil {
		return mapErr(err)
	}
	if exists {
		return conflict("is_other", "The service Sonstiges cannot be deleted")
	}
	return ErrNotFound
}

// ServiceImport creates or updates specialties and services from a JSON template. With dry_run nothing is stored.
func (s *Store) ServiceImport(ctx context.Context, p api.ServiceImportParams) (api.ImportResult, error) {
	res := api.ImportResult{DryRun: p.DryRun, Warnings: []string{}}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if p.Specialties != nil {
			for _, sp := range *p.Specialties {
				tag, err := tx.Exec(ctx, `INSERT INTO specialties (name) VALUES ($1) ON CONFLICT (name) DO NOTHING`, sp.Name)
				if err != nil {
					return mapErr(err)
				}
				res.SpecialtiesCreated += int(tag.RowsAffected())
			}
		}
		if p.Services != nil {
			for _, sv := range *p.Services {
				other := sv.IsOther != nil && *sv.IsOther
				var specID *api.Uuid
				if !other {
					if sv.Specialty == nil {
						return unprocessable("specialty_required", fmt.Sprintf("Service %q needs a specialty", sv.Name))
					}
					var id api.Uuid
					if err := tx.QueryRow(ctx, `SELECT id FROM specialties WHERE name = $1`, *sv.Specialty).Scan(&id); err != nil {
						return unprocessable("unknown_specialty", fmt.Sprintf("Unknown specialty %q", *sv.Specialty))
					}
					specID = &id
				}
				var tag int64
				if other {
					t, err := tx.Exec(ctx, `UPDATE services SET name = $1, duration_minutes = $2 WHERE is_other`, sv.Name, sv.DurationMinutes)
					if err != nil {
						return mapErr(err)
					}
					tag = t.RowsAffected()
					if tag == 0 {
						return unprocessable("other_missing", "The service Sonstiges does not exist")
					}
					res.ServicesUpdated++
					continue
				}
				var created bool
				err := tx.QueryRow(ctx, `INSERT INTO services (name, duration_minutes, specialty_id, is_other) VALUES ($1, $2, $3, false)
					ON CONFLICT (name) DO UPDATE SET duration_minutes = EXCLUDED.duration_minutes, specialty_id = EXCLUDED.specialty_id
					WHERE NOT services.is_other
					RETURNING (xmax = 0)`, sv.Name, sv.DurationMinutes, specID).Scan(&created)
				if err != nil {
					return mapErr(err)
				}
				if created {
					res.ServicesCreated++
				} else {
					res.ServicesUpdated++
				}
			}
		}
		if p.DryRun {
			return errDryRun
		}
		return nil
	})
	if errors.Is(err, errDryRun) {
		err = nil
	}
	return res, err
}

var errDryRun = &Error{Status: 200, Code: "dry_run"}

// DoctorList returns the active doctors with their specialties.
func (s *Store) DoctorList(ctx context.Context, p api.DoctorListParams) (api.DoctorList, error) {
	out := api.DoctorList{Items: []api.DoctorSummary{}}
	rows, err := s.pool.Query(ctx, `SELECT u.id, u.first_name, u.last_name FROM doctors d JOIN users u ON u.id = d.user_id
		WHERE `+activeDoctor+` AND ($1::uuid IS NULL OR EXISTS (SELECT 1 FROM doctor_specialties ds WHERE ds.doctor_id = u.id AND ds.specialty_id = $1))
		ORDER BY lower(u.last_name), lower(u.first_name), u.id`, p.SpecialtyId)
	if err != nil {
		return out, mapErr(err)
	}
	defer rows.Close()
	idx := map[api.Uuid]int{}
	var ids []string
	for rows.Next() {
		d := api.DoctorSummary{Specialties: []api.Specialty{}}
		if err := rows.Scan(&d.Id, &d.FirstName, &d.LastName); err != nil {
			return out, mapErr(err)
		}
		idx[d.Id] = len(out.Items)
		ids = append(ids, d.Id.String())
		out.Items = append(out.Items, d)
	}
	if err := rows.Err(); err != nil {
		return out, mapErr(err)
	}
	if len(ids) == 0 {
		return out, nil
	}
	sr, err := s.pool.Query(ctx, `SELECT ds.doctor_id, sp.id, sp.name FROM doctor_specialties ds JOIN specialties sp ON sp.id = ds.specialty_id
		WHERE ds.doctor_id = ANY($1::uuid[]) ORDER BY lower(sp.name)`, ids)
	if err != nil {
		return out, mapErr(err)
	}
	defer sr.Close()
	for sr.Next() {
		var did api.Uuid
		var sp api.Specialty
		if err := sr.Scan(&did, &sp.Id, &sp.Name); err != nil {
			return out, mapErr(err)
		}
		out.Items[idx[did]].Specialties = append(out.Items[idx[did]].Specialties, sp)
	}
	return out, mapErr(sr.Err())
}

func isDoctor(ctx context.Context, q querier, id api.Uuid) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM doctors d JOIN users u ON u.id = d.user_id WHERE d.user_id = $1 AND `+activeDoctor+`)`, id).Scan(&ok)
	return ok, mapErr(err)
}

func readSchedule(ctx context.Context, q querier, id api.Uuid) (api.Schedule, error) {
	sch := api.Schedule{SpecialtyIds: []api.Uuid{}, WorkingDays: []api.WorkingDay{}}
	rows, err := q.Query(ctx, `SELECT specialty_id FROM doctor_specialties WHERE doctor_id = $1 ORDER BY specialty_id`, id)
	if err != nil {
		return sch, mapErr(err)
	}
	for rows.Next() {
		var sid api.Uuid
		if err := rows.Scan(&sid); err != nil {
			rows.Close()
			return sch, mapErr(err)
		}
		sch.SpecialtyIds = append(sch.SpecialtyIds, sid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return sch, mapErr(err)
	}
	wr, err := q.Query(ctx, `SELECT weekday, to_char(start_time, 'HH24:MI'), to_char(end_time, 'HH24:MI') FROM working_days
		WHERE doctor_id = $1 ORDER BY weekday, start_time`, id)
	if err != nil {
		return sch, mapErr(err)
	}
	defer wr.Close()
	for wr.Next() {
		var w api.WorkingDay
		if err := wr.Scan(&w.Weekday, &w.StartTime, &w.EndTime); err != nil {
			return sch, mapErr(err)
		}
		sch.WorkingDays = append(sch.WorkingDays, w)
	}
	return sch, mapErr(wr.Err())
}

// DoctorGetSchedule returns specialties and working days.
func (s *Store) DoctorGetSchedule(ctx context.Context, id api.Uuid) (api.Schedule, error) {
	ok, err := isDoctor(ctx, s.pool, id)
	if err != nil {
		return api.Schedule{}, err
	}
	if !ok {
		return api.Schedule{}, ErrNotFound
	}
	return readSchedule(ctx, s.pool, id)
}

func validateWorkingDays(days []api.WorkingDay) error {
	type span struct{ start, end string }
	byDay := map[int][]span{}
	for _, d := range days {
		st, err1 := time.Parse("15:04", d.StartTime)
		en, err2 := time.Parse("15:04", d.EndTime)
		if err1 != nil || err2 != nil {
			return unprocessable("invalid_time", "Times must be HH:MM")
		}
		if !en.After(st) {
			return unprocessable("end_before_start", "A working block must end after it starts")
		}
		byDay[d.Weekday] = append(byDay[d.Weekday], span{d.StartTime, d.EndTime})
	}
	for _, spans := range byDay {
		sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
		for i := 1; i < len(spans); i++ {
			if spans[i].start < spans[i-1].end {
				return unprocessable("working_days_overlap", "Working blocks of one weekday must not overlap")
			}
		}
	}
	return nil
}

// DoctorSetSchedule replaces specialties and working days and lists future bookings now outside the working time.
func (s *Store) DoctorSetSchedule(ctx context.Context, p api.DoctorScheduleParams) (api.ScheduleResult, error) {
	if err := validateWorkingDays(p.WorkingDays); err != nil {
		return api.ScheduleResult{}, err
	}
	res := api.ScheduleResult{BookingsOutsideHours: []api.Uuid{}}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var id api.Uuid
		if err := tx.QueryRow(ctx, `SELECT d.user_id FROM doctors d JOIN users u ON u.id = d.user_id WHERE d.user_id = $1 AND `+activeDoctor+` FOR UPDATE OF d`, p.DoctorId).Scan(&id); err != nil {
			return mapErr(err)
		}
		spec := make([]string, len(p.SpecialtyIds))
		for i, sid := range p.SpecialtyIds {
			spec[i] = sid.String()
		}
		wd, st, en := make([]int16, len(p.WorkingDays)), make([]string, len(p.WorkingDays)), make([]string, len(p.WorkingDays))
		for i, d := range p.WorkingDays {
			wd[i], st[i], en[i] = int16(d.Weekday), d.StartTime, d.EndTime //nolint:gosec // weekday validated 1..7 by the schema
		}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM doctor_specialties WHERE doctor_id = $1`, []any{id}},
			{`INSERT INTO doctor_specialties (doctor_id, specialty_id) SELECT $1, unnest($2::uuid[])`, []any{id, spec}},
			{`DELETE FROM working_days WHERE doctor_id = $1`, []any{id}},
			{`INSERT INTO working_days (doctor_id, weekday, start_time, end_time) SELECT $1, w, s::time, e::time FROM unnest($2::smallint[], $3::text[], $4::text[]) AS t(w, s, e)`, []any{id, wd, st, en}},
		} {
			if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
				return mapErr(err)
			}
		}
		sch, err := readSchedule(ctx, tx, id)
		if err != nil {
			return err
		}
		res.Schedule = sch
		rows, err := tx.Query(ctx, `SELECT b.id FROM bookings b
			WHERE b.doctor_id = $1 AND b.deleted_at IS NULL AND b.status IN ('angefragt', 'bestaetigt') AND b.ends_at > now()
			  AND NOT EXISTS (SELECT 1 FROM working_days w WHERE w.doctor_id = b.doctor_id
			    AND w.weekday = EXTRACT(ISODOW FROM b.starts_at AT TIME ZONE $2::text)
			    AND (b.starts_at AT TIME ZONE $2::text)::date = (b.ends_at AT TIME ZONE $2::text)::date
			    AND (b.starts_at AT TIME ZONE $2::text)::time >= w.start_time
			    AND (b.ends_at AT TIME ZONE $2::text)::time <= w.end_time)
			ORDER BY b.starts_at LIMIT 500`, id, s.tz)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			var bid api.Uuid
			if err := rows.Scan(&bid); err != nil {
				return mapErr(err)
			}
			res.BookingsOutsideHours = append(res.BookingsOutsideHours, bid)
		}
		return mapErr(rows.Err())
	})
	return res, err
}

const patientSelect = `SELECT u.id, u.first_name, u.last_name, u.email, u.phone, p.main_doctor_id
  FROM patients p JOIN users u ON u.id = p.user_id`

const patientWhere = `WHERE u.deleted_at IS NULL AND u.status = 'active'
  AND EXISTS (SELECT 1 FROM user_roles r WHERE r.user_id = u.id AND r.role = 'patient')`

func scanPatient(row pgx.Row) (api.Patient, error) {
	var pt api.Patient
	var email string
	err := row.Scan(&pt.Id, &pt.FirstName, &pt.LastName, &email, &pt.Phone, &pt.MainDoctorId)
	pt.Email = toEmail(email)
	return pt, mapErr(err)
}

// PatientList searches patients by name or e-mail.
func (s *Store) PatientList(ctx context.Context, p api.PatientListParams) (api.PatientPage, error) {
	var q *string
	if p.Q != nil && *p.Q != "" {
		l := escapeLike(*p.Q)
		q = &l
	}
	where := patientWhere + ` AND ($1::text IS NULL OR u.first_name ILIKE $1 OR u.last_name ILIKE $1 OR u.email ILIKE $1 OR (u.first_name || ' ' || u.last_name) ILIKE $1)`
	limit, offset := limitOffset(p.Limit, p.Offset)
	page := api.PatientPage{Items: []api.Patient{}}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM patients p JOIN users u ON u.id = p.user_id `+where, q).Scan(&page.Total); err != nil {
		return page, mapErr(err)
	}
	rows, err := s.pool.Query(ctx, patientSelect+` `+where+` ORDER BY lower(u.last_name), lower(u.first_name), u.id LIMIT $2 OFFSET $3`, q, limit, offset)
	if err != nil {
		return page, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		pt, err := scanPatient(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, pt)
	}
	return page, mapErr(rows.Err())
}

// PatientGet loads one patient.
func (s *Store) PatientGet(ctx context.Context, id api.Uuid) (api.Patient, error) {
	return scanPatient(s.pool.QueryRow(ctx, patientSelect+` `+patientWhere+` AND u.id = $1`, id))
}

// PatientSetMainDoctor sets or clears the main doctor.
func (s *Store) PatientSetMainDoctor(ctx context.Context, p api.MainDoctorParams) (api.Patient, error) {
	var out api.Patient
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := scanPatient(tx.QueryRow(ctx, patientSelect+` `+patientWhere+` AND u.id = $1 FOR UPDATE OF p`, p.PatientId)); err != nil {
			return err
		}
		if p.DoctorId != nil {
			ok, err := isDoctor(ctx, tx, *p.DoctorId)
			if err != nil {
				return err
			}
			if !ok {
				return unprocessable("not_a_doctor", "The main doctor must be an active doctor")
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE patients SET main_doctor_id = $2 WHERE user_id = $1`, p.PatientId, p.DoctorId); err != nil {
			return mapErr(err)
		}
		var err error
		out, err = scanPatient(tx.QueryRow(ctx, patientSelect+` `+patientWhere+` AND u.id = $1`, p.PatientId))
		return err
	})
	return out, err
}
