package server_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSpecialtiesAndServices(t *testing.T) {
	e := newEnv(t)
	sp := e.newSpecialty()
	name := "Dup-" + uuid.NewString()
	e.ok("calendar_service", "specialtyCreate", obj{"name": name}, 201)
	e.expect("calendar_service", "specialtyCreate", obj{"name": name}, 409, "duplicate")

	sv := e.newService(sp, 30)
	e.expect("calendar_service", "specialtyDelete", obj{"id": sp}, 409, "in_use")
	e.expect("calendar_service", "serviceCreate", obj{"name": "x-" + uuid.NewString(), "duration_minutes": 30}, 422, "specialty_required")
	e.expect("calendar_service", "serviceCreate", obj{"name": "x-" + uuid.NewString(), "duration_minutes": 1, "specialty_id": sp}, 422, "validation_failed")

	got := e.ok("calendar_service", "serviceUpdate", obj{"id": sv, "name": "Neu-" + uuid.NewString(), "duration_minutes": 45, "specialty_id": sp}, 200)
	if got["duration_minutes"].(float64) != 45 {
		t.Fatalf("update: %v", got)
	}
	e.ok("calendar_service", "serviceDelete", obj{"id": sv}, 204)
	e.expect("calendar_service", "serviceDelete", obj{"id": sv}, 404, "not_found")
	e.ok("calendar_service", "specialtyDelete", obj{"id": sp}, 204)

	// "Sonstiges" can neither be deleted nor get a specialty.
	var other string
	for _, s := range list(e.ok("calendar_service", "serviceList", nil, 200), "items") {
		if s.(obj)["is_other"] == true {
			other = str(s.(obj), "id")
		}
	}
	if other == "" {
		t.Fatal("seeded service Sonstiges missing")
	}
	e.expect("calendar_service", "serviceDelete", obj{"id": other}, 409, "is_other")
}

func TestServiceImport(t *testing.T) {
	e := newEnv(t)
	spName := "Imp-" + uuid.NewString()
	svName := "ImpSv-" + uuid.NewString()
	file := obj{"specialties": []obj{{"name": spName}}, "services": []obj{{"name": svName, "duration_minutes": 20, "specialty": spName}}}

	dry := e.ok("calendar_service", "serviceImport", merge(file, obj{"dry_run": true}), 200)
	if dry["services_created"].(float64) != 1 || dry["specialties_created"].(float64) != 1 {
		t.Fatalf("dry run counts: %v", dry)
	}
	if contains(e.ok("calendar_service", "serviceList", nil, 200), svName) {
		t.Fatal("a dry run must not store anything")
	}
	e.ok("calendar_service", "serviceImport", merge(file, obj{"dry_run": false}), 200)
	if !contains(e.ok("calendar_service", "serviceList", nil, 200), svName) {
		t.Fatal("import must store the service")
	}
	again := e.ok("calendar_service", "serviceImport", merge(file, obj{"dry_run": false}), 200)
	if again["services_updated"].(float64) != 1 || again["services_created"].(float64) != 0 {
		t.Fatalf("second import must update: %v", again)
	}
	bad := obj{"dry_run": false, "services": []obj{{"name": "Q-" + uuid.NewString(), "duration_minutes": 20, "specialty": "gibt-es-nicht"}}}
	e.expect("calendar_service", "serviceImport", bad, 422, "unknown_specialty")
}

func merge(a, b obj) obj {
	out := obj{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func contains(l obj, name string) bool {
	for _, s := range list(l, "items") {
		if str(s.(obj), "name") == name {
			return true
		}
	}
	return false
}

func TestDoctorSchedule(t *testing.T) {
	e := newEnv(t)
	doc := e.newDoctor()
	sp := e.newSpecialty()
	day := func(wd int, from, to string) obj { return obj{"weekday": wd, "start_time": from, "end_time": to} }

	e.expect("calendar_service", "doctorSetSchedule", obj{"doctor_id": doc, "specialty_ids": []string{sp}, "working_days": []obj{day(1, "08:00", "12:00"), day(1, "11:00", "14:00")}}, 422, "working_days_overlap")
	e.expect("calendar_service", "doctorSetSchedule", obj{"doctor_id": doc, "specialty_ids": []string{sp}, "working_days": []obj{day(1, "12:00", "08:00")}}, 422, "end_before_start")
	e.expect("calendar_service", "doctorSetSchedule", obj{"doctor_id": doc, "specialty_ids": []string{uuid.NewString()}, "working_days": []obj{}}, 422, "unknown_reference")
	e.expect("calendar_service", "doctorSetSchedule", obj{"doctor_id": e.newPatient(), "specialty_ids": []string{}, "working_days": []obj{}}, 404, "not_found")

	res := e.ok("calendar_service", "doctorSetSchedule", obj{"doctor_id": doc, "specialty_ids": []string{sp}, "working_days": []obj{day(1, "08:00", "12:00"), day(1, "13:00", "17:00"), day(3, "09:00", "12:00")}}, 200)
	if n := len(list(res["schedule"].(obj), "working_days")); n != 3 {
		t.Fatalf("working days = %d", n)
	}
	got := e.ok("calendar_service", "doctorGetSchedule", obj{"doctor_id": doc}, 200)
	if list(got, "working_days")[0].(obj)["start_time"] != "08:00" {
		t.Fatalf("schedule: %v", got)
	}
	// Doctors can be found by specialty.
	if !hasDoctor(e.ok("calendar_service", "doctorList", obj{"specialty_id": sp}, 200), doc) {
		t.Fatal("doctor must be listed under the specialty")
	}

	// A future booking outside the new working time is reported, not deleted.
	pat := e.newPatient()
	svc := e.newService(sp, 30)
	start := nextWeekday(time.Wednesday, 20, 0) // Wednesday 20:00 Berlin: outside 09:00-12:00
	b := e.ok("calendar_service", "bookingCreate", obj{"patient_id": pat, "doctor_id": doc, "service_id": svc, "starts_at": ts(start), "ends_at": ts(start.Add(30 * time.Minute))}, 201)
	res = e.ok("calendar_service", "doctorSetSchedule", obj{"doctor_id": doc, "specialty_ids": []string{sp}, "working_days": []obj{day(3, "09:00", "12:00")}}, 200)
	if l := list(res, "bookings_outside_hours"); len(l) != 1 || l[0] != b["id"] {
		t.Fatalf("outside hours = %v, want [%v]", l, b["id"])
	}
	inside := nextWeekday(time.Wednesday, 10, 7)
	e.ok("calendar_service", "bookingCreate", obj{"patient_id": pat, "doctor_id": doc, "service_id": svc, "starts_at": ts(inside), "ends_at": ts(inside.Add(30 * time.Minute))}, 201)
	res = e.ok("calendar_service", "doctorSetSchedule", obj{"doctor_id": doc, "specialty_ids": []string{sp}, "working_days": []obj{day(3, "09:00", "12:00")}}, 200)
	if n := len(list(res, "bookings_outside_hours")); n != 1 {
		t.Fatalf("a booking inside the working time must not be reported, got %d", n)
	}
}

// nextWeekday returns the given local time (Europe/Berlin) on a weekday at least `weeksAhead` weeks in the future.
func nextWeekday(wd time.Weekday, hour, weeksAhead int) time.Time {
	loc, _ := time.LoadLocation("Europe/Berlin")
	d := time.Now().In(loc).AddDate(0, 0, 14+7*weeksAhead)
	for d.Weekday() != wd {
		d = d.AddDate(0, 0, 1)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, loc).UTC()
}

func TestCalendarWindows(t *testing.T) {
	e := newEnv(t)
	doc := e.newDoctor()
	sp := e.newSpecialty()
	svc := e.newService(sp, 30)
	pat := e.newPatient()
	e.ok("calendar_service", "doctorSetSchedule", obj{"doctor_id": doc, "specialty_ids": []string{sp},
		"working_days": []obj{{"weekday": 2, "start_time": "08:00", "end_time": "12:00"}}}, 200)

	tue := nextWeekday(time.Tuesday, 9, 30)
	e.ok("calendar_service", "bookingCreate", obj{"patient_id": pat, "doctor_id": doc, "service_id": svc, "starts_at": ts(tue), "ends_at": ts(tue.Add(30 * time.Minute))}, 201)

	res := e.ok("calendar_service", "calendarGetWindows", obj{"from": ts(tue.Add(-48 * time.Hour)), "to": ts(tue.Add(48 * time.Hour)), "doctor_id": doc}, 200)
	d := list(res, "doctors")[0].(obj)
	work, busy := list(d, "working"), list(d, "busy")
	if len(work) != 1 || len(busy) != 1 {
		t.Fatalf("windows: %v", res)
	}
	w := work[0].(obj)
	if !sameInstant(t, str(w, "starts_at"), nextWeekday(time.Tuesday, 8, 30)) || !sameInstant(t, str(w, "ends_at"), nextWeekday(time.Tuesday, 12, 30)) {
		t.Fatalf("working window must be 08:00-12:00 Berlin time: %v", w)
	}
	if bz := busy[0].(obj); bz["patient_id"] != nil || len(bz) != 2 {
		t.Fatalf("busy intervals must not carry details: %v", bz)
	}
	e.expect("calendar_service", "calendarGetWindows", obj{"from": ts(tue), "to": ts(tue.AddDate(0, 0, 90))}, 422, "invalid_range")
}

func sameInstant(t *testing.T, s string, want time.Time) bool {
	t.Helper()
	got, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return got.Equal(want)
}
