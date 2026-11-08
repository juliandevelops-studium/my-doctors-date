package server_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fixture struct {
	doc, doc2, patA, patB, svc string
}

func newFixture(e *env) fixture {
	sp := e.newSpecialty()
	return fixture{doc: e.newDoctor(), doc2: e.newDoctor(), patA: e.newPatient(), patB: e.newPatient(), svc: e.newService(sp, 30)}
}

func TestBookingOverlapIsRejectedByTheDatabase(t *testing.T) {
	e := newEnv(t)
	f := newFixture(e)
	start := slot()

	first := e.book("calendar_service", f.patA, f.doc, f.svc, start, 201)
	e.expect("calendar_service", "bookingCreate", obj{"patient_id": f.patB, "doctor_id": f.doc, "service_id": f.svc, "starts_at": ts(start.Add(10 * time.Minute)), "ends_at": ts(start.Add(40 * time.Minute))}, 409, "slot_taken")
	e.book("calendar_service", f.patB, f.doc, f.svc, start.Add(30*time.Minute), 201) // adjacent is fine
	e.book("calendar_service", f.patB, f.doc2, f.svc, start, 201)                    // other doctor is fine

	// Cancelling frees the slot.
	e.ok("calendar_service", "bookingCancel", obj{"booking_id": first["id"], "owner_patient_id": f.patA}, 200)
	e.book("calendar_service", f.patB, f.doc, f.svc, start, 201)

	// Validation and references.
	e.expect("calendar_service", "bookingCreate", obj{"patient_id": f.patA, "service_id": f.svc, "starts_at": ts(start), "ends_at": ts(start)}, 422, "check_violation")
	e.expect("calendar_service", "bookingCreate", obj{"patient_id": uuid.NewString(), "service_id": f.svc, "starts_at": ts(start), "ends_at": ts(start.Add(time.Hour))}, 404, "not_found")
	e.expect("calendar_service", "bookingCreate", obj{"patient_id": f.patA, "doctor_id": f.patB, "service_id": f.svc, "starts_at": ts(start), "ends_at": ts(start.Add(time.Hour))}, 422, "not_a_doctor")
}

// Plan, Phase 3: two parallel bookings of one slot give exactly one success.
func TestParallelBookingsOneWins(t *testing.T) {
	e := newEnv(t)
	f := newFixture(e)
	start := slot()
	var created, conflicts, other atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, _ := e.call("calendar_service", "bookingCreate", obj{"patient_id": f.patA, "doctor_id": f.doc, "service_id": f.svc, "starts_at": ts(start), "ends_at": ts(start.Add(30 * time.Minute))})
			switch got {
			case 201:
				created.Add(1)
			case 409:
				conflicts.Add(1)
			default:
				other.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 || conflicts.Load() != 11 || other.Load() != 0 {
		t.Fatalf("created=%d conflicts=%d other=%d, want 1/11/0", created.Load(), conflicts.Load(), other.Load())
	}
}

// SR.03: ownership is part of the SQL, a foreign booking is simply not found.
func TestOwnershipIsAppliedInSQL(t *testing.T) {
	e := newEnv(t)
	f := newFixture(e)
	b := e.book("calendar_service", f.patA, f.doc, f.svc, slot(), 201)
	id := b["id"]

	e.ok("calendar_service", "bookingGet", obj{"booking_id": id, "owner_patient_id": f.patA}, 200)
	e.ok("calendar_service", "bookingGet", obj{"booking_id": id, "owner_doctor_id": f.doc}, 200)
	e.expect("calendar_service", "bookingGet", obj{"booking_id": id, "owner_patient_id": f.patB}, 404, "not_found")
	e.expect("calendar_service", "bookingGet", obj{"booking_id": id, "owner_doctor_id": f.doc2}, 404, "not_found")
	e.expect("calendar_service", "bookingCancel", obj{"booking_id": id, "owner_patient_id": f.patB}, 404, "not_found")
	e.ok("calendar_service", "bookingGet", obj{"booking_id": id}, 200) // still there, unchanged

	own := e.ok("calendar_service", "bookingList", obj{"owner_patient_id": f.patB}, 200)
	if own["total"].(float64) != 0 {
		t.Fatalf("patient B must not see bookings of A: %v", own)
	}
	if e.ok("calendar_service", "bookingList", obj{"owner_doctor_id": f.doc}, 200)["total"].(float64) != 1 {
		t.Fatal("the doctor must see the booking in the own calendar")
	}
	if e.ok("calendar_service", "bookingList", obj{"owner_doctor_id": f.doc2}, 200)["total"].(float64) != 0 {
		t.Fatal("another doctor must not see it")
	}
}

func TestSoftDeletedBookingsAreInvisible(t *testing.T) {
	e := newEnv(t)
	f := newFixture(e)
	b := e.book("calendar_service", f.patA, f.doc, f.svc, slot(), 201)
	b["reason"] = nil
	e.ok("calendar_service", "bookingUpdate", obj{"booking_id": b["id"], "reason": "Husten", "info": "seit drei Tagen"}, 200)

	cancelled := e.ok("calendar_service", "bookingCancel", obj{"booking_id": b["id"]}, 200)
	if cancelled["status"] != "storniert" || cancelled["reason"] != nil || cancelled["info"] != nil || str(cancelled, "patient_email") == "" {
		t.Fatalf("cancel result must keep mail data but no contents: %v", cancelled)
	}
	e.expect("calendar_service", "bookingGet", obj{"booking_id": b["id"]}, 404, "not_found")
	e.expect("calendar_service", "bookingCancel", obj{"booking_id": b["id"]}, 404, "not_found")
	e.expect("calendar_service", "bookingUpdate", obj{"booking_id": b["id"], "reason": "x"}, 404, "not_found")
	if e.ok("calendar_service", "bookingList", obj{"patient_id": f.patA}, 200)["total"].(float64) != 0 {
		t.Fatal("a cancelled booking must not be listed")
	}
	var reason, info *string
	if err := e.pool.QueryRow(t.Context(), `SELECT reason, info FROM bookings WHERE id = $1`, b["id"]).Scan(&reason, &info); err != nil {
		t.Fatal(err)
	}
	if reason != nil || info != nil {
		t.Fatal("reason and info must be cleared in the database on cancel (Löschkonzept)")
	}
}

func TestPastBookingsCannotBeChanged(t *testing.T) {
	e := newEnv(t)
	f := newFixture(e)
	past := time.Now().Add(-48 * time.Hour).Truncate(time.Hour)
	b := e.book("calendar_service", f.patA, f.doc, f.svc, past, 201)
	e.expect("calendar_service", "bookingCancel", obj{"booking_id": b["id"], "owner_patient_id": f.patA}, 409, "booking_in_past")
	e.expect("calendar_service", "bookingUpdate", obj{"booking_id": b["id"], "reason": "x"}, 409, "booking_in_past")
	e.expect("calendar_service", "bookingAssignDoctor", obj{"booking_id": b["id"], "doctor_id": f.doc2}, 409, "booking_in_past")

	// bookingsCompletePast closes it; only purge_job may call that.
	if e.ok("purge_job", "bookingsCompletePast", nil, 200)["affected_count"].(float64) < 1 {
		t.Fatal("the ended booking must be completed")
	}
	got := e.ok("calendar_service", "bookingGet", obj{"booking_id": b["id"]}, 200)
	if got["status"] != "abgeschlossen" {
		t.Fatalf("status = %v", got["status"])
	}
	past2 := e.ok("calendar_service", "bookingList", obj{"owner_patient_id": f.patA, "when": "past"}, 200)
	if past2["total"].(float64) != 1 || e.ok("calendar_service", "bookingList", obj{"owner_patient_id": f.patA, "when": "upcoming"}, 200)["total"].(float64) != 0 {
		t.Fatalf("when filter: %v", past2)
	}
}

func TestUnassignedBookingAndAssignment(t *testing.T) {
	e := newEnv(t)
	f := newFixture(e)
	start := slot()
	other := e.book("calendar_service", f.patB, f.doc, f.svc, start, 201) // doc is busy
	_ = other
	b := e.book("calendar_service", f.patA, "", f.svc, start, 201) // "Sonstiges": no doctor yet
	if b["doctor_id"] != nil {
		t.Fatalf("doctor must be empty: %v", b)
	}
	list0 := e.ok("calendar_service", "bookingList", obj{"unassigned": true, "patient_id": f.patA}, 200)
	if list0["total"].(float64) != 1 {
		t.Fatalf("unassigned list: %v", list0)
	}
	e.expect("calendar_service", "bookingAssignDoctor", obj{"booking_id": b["id"], "doctor_id": f.doc}, 409, "slot_taken")
	e.expect("calendar_service", "bookingAssignDoctor", obj{"booking_id": b["id"], "doctor_id": f.patA}, 422, "not_a_doctor")
	got := e.ok("calendar_service", "bookingAssignDoctor", obj{"booking_id": b["id"], "doctor_id": f.doc2}, 200)
	if got["doctor_id"] != f.doc2 {
		t.Fatalf("assigned: %v", got)
	}
	if e.ok("calendar_service", "bookingList", obj{"unassigned": true, "patient_id": f.patA}, 200)["total"].(float64) != 0 {
		t.Fatal("assigned booking must leave the unassigned list")
	}
}

func TestBookingUpdate(t *testing.T) {
	e := newEnv(t)
	f := newFixture(e)
	start := slot()
	b := e.book("calendar_service", f.patA, f.doc, f.svc, start, 201)
	e.book("calendar_service", f.patB, f.doc, f.svc, start.Add(2*time.Hour), 201)

	e.expect("calendar_service", "bookingUpdate", obj{"booking_id": b["id"], "starts_at": ts(start.Add(time.Hour))}, 422, "time_pair_required")
	e.expect("calendar_service", "bookingUpdate", obj{"booking_id": b["id"], "starts_at": ts(start.Add(2 * time.Hour)), "ends_at": ts(start.Add(150 * time.Minute))}, 409, "slot_taken")
	got := e.ok("calendar_service", "bookingUpdate", obj{"booking_id": b["id"], "starts_at": ts(start.Add(time.Hour)), "ends_at": ts(start.Add(90 * time.Minute)), "reason": "Kontrolle"}, 200)
	if !sameInstant(t, str(got, "starts_at"), start.Add(time.Hour)) || got["reason"] != "Kontrolle" {
		t.Fatalf("update: %v", got)
	}
}

func TestMainDoctorAndPatientSearch(t *testing.T) {
	e := newEnv(t)
	f := newFixture(e)
	e.ok("calendar_service", "patientSetMainDoctor", obj{"patient_id": f.patA, "doctor_id": f.doc}, 200)
	if got := e.ok("calendar_service", "patientGet", obj{"patient_id": f.patA}, 200); got["main_doctor_id"] != f.doc {
		t.Fatalf("main doctor: %v", got)
	}
	e.expect("calendar_service", "patientSetMainDoctor", obj{"patient_id": f.patA, "doctor_id": f.patB}, 422, "not_a_doctor")
	e.expect("calendar_service", "patientSetMainDoctor", obj{"patient_id": f.doc, "doctor_id": f.doc}, 404, "not_found") // a doctor is no patient
	e.ok("calendar_service", "patientSetMainDoctor", obj{"patient_id": f.patA, "doctor_id": nil}, 200)

	_, email := e.newUser("active", "patient")
	res := e.ok("calendar_service", "patientList", obj{"q": email}, 200)
	if res["total"].(float64) != 1 {
		t.Fatalf("search by e-mail: %v", res)
	}
	// LIKE wildcards in the search text are literal.
	if e.ok("calendar_service", "patientList", obj{"q": "%%"}, 200)["total"].(float64) != 0 {
		t.Fatal("the search must escape LIKE wildcards")
	}
}
