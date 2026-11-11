package server_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// UF-05: book, mail, see it, cancel it. The doctor is assigned automatically.
func TestPatientBooksAndCancels(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 1)
	pat := p.patient("Paula")
	start := p.at(0, 9, 0)

	b := p.mustBook(pat, p.service, start, obj{"reason": "Husten", "info": "seit drei Tagen"})
	if b["status"] != "bestaetigt" || !sameInstant(t, str(b, "starts_at"), start) || !sameInstant(t, str(b, "ends_at"), start.Add(30*time.Minute)) {
		t.Fatalf("booking: %v", b)
	}
	if b["doctor"].(obj)["id"] != p.doctors[0].ID || b["reason"] != "Husten" || b["patient"] != nil {
		t.Fatalf("doctor, reason and no patient block for the patient: %v", b)
	}
	m, ok := h.mail.last("booking_confirmed", pat.Email)
	if !ok || m.FirstName != "Paula" || m.Doctor != "Doc1 Test" {
		t.Fatalf("confirmation mail: %+v", m)
	}

	id := str(b, "id")
	got := h.want(200, "GET", "/v1/bookings/"+id, pat.tok(), nil)
	if got["info"] != "seit drei Tagen" {
		t.Fatalf("get: %v", got)
	}
	list := h.want(200, "GET", "/v1/bookings?when=upcoming", pat.tok(), nil)
	if num(list, "total") != 1 {
		t.Fatalf("list: %v", list)
	}

	// The slot is taken for everybody else; the patient cancels and it is free again (UF-07).
	other := p.patient("Otto")
	st, out := p.book(other, p.service, start, nil)
	if st != 409 || out["code"] != "slot_taken" {
		t.Fatalf("second booking: %d %v", st, out)
	}
	h.want(204, "POST", "/v1/bookings/"+id+"/cancel", pat.tok(), nil)
	if _, ok := h.mail.last("booking_cancelled", pat.Email); !ok {
		t.Fatal("cancellation mail missing")
	}
	h.wantCode(404, "not_found", "GET", "/v1/bookings/"+id, pat.tok(), nil) // soft-deleted bookings are invisible
	h.wantCode(404, "not_found", "POST", "/v1/bookings/"+id+"/cancel", pat.tok(), nil)
	p.mustBook(other, p.service, start, nil)
	if num(h.want(200, "GET", "/v1/bookings", pat.tok(), nil), "total") != 0 {
		t.Fatal("a cancelled booking must not be listed")
	}
}

// SR.03: a foreign booking does not exist for the caller (404), and nothing changes.
func TestForeignBookingIs404(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 2)
	patA, patB := p.patient("Anna"), p.patient("Bernd")
	b := p.mustBook(patA, p.service, p.at(0, 9, 0), obj{"reason": "geheim"})
	id := str(b, "id")
	bookedDoctor := b["doctor"].(obj)["id"].(string)
	var otherDoctor *user
	for _, d := range p.doctors {
		if d.ID != bookedDoctor {
			otherDoctor = d
		}
	}

	for _, c := range []struct {
		who    *user
		method string
		path   string
	}{
		{patB, "GET", "/v1/bookings/" + id},
		{patB, "POST", "/v1/bookings/" + id + "/cancel"},
		{otherDoctor, "GET", "/v1/bookings/" + id},
		{otherDoctor, "POST", "/v1/bookings/" + id + "/cancel"},
	} {
		h.wantCode(404, "not_found", c.method, c.path, c.who.tok(), nil)
	}
	// The booking is unchanged and visible to the owner, the booked doctor and staff.
	for _, u := range []*user{patA, p.front, p.org} {
		if got := h.want(200, "GET", "/v1/bookings/"+id, u.tok(), nil); got["status"] != "bestaetigt" {
			t.Fatalf("booking changed: %v", got)
		}
	}
	var booked *user
	for _, d := range p.doctors {
		if d.ID == bookedDoctor {
			booked = d
		}
	}
	got := h.want(200, "GET", "/v1/bookings/"+id, booked.tok(), nil)
	if got["reason"] != "geheim" || got["patient"].(obj)["id"] != patA.ID {
		t.Fatalf("the doctor sees reason and patient: %v", got)
	}
	// Lists are scoped, too.
	if num(h.want(200, "GET", "/v1/bookings", patB.tok(), nil), "total") != 0 {
		t.Fatal("patient B must not see bookings of A")
	}
	if num(h.want(200, "GET", "/v1/bookings", otherDoctor.tok(), nil), "total") != 0 {
		t.Fatal("another doctor must not see the booking")
	}
	if num(h.want(200, "GET", "/v1/bookings", booked.tok(), nil), "total") != 1 {
		t.Fatal("the booked doctor sees it in the own calendar")
	}
	// Filters are for staff only: a patient cannot widen the list with patient_id or doctor_id.
	if num(h.want(200, "GET", "/v1/bookings?patient_id="+patA.ID, patB.tok(), nil), "total") != 0 {
		t.Fatal("patient_id must be ignored for a patient")
	}
	if num(h.want(200, "GET", "/v1/bookings?doctor_id="+bookedDoctor, patB.tok(), nil), "total") != 0 {
		t.Fatal("doctor_id must be ignored for a patient")
	}
	if num(h.want(200, "GET", "/v1/bookings?patient_id="+patA.ID, p.front.tok(), nil), "total") != 1 {
		t.Fatal("staff may filter by patient")
	}
}

// SR.03: a manipulated patient_id in the body has no effect for a patient; the token decides.
func TestPatientIdInBodyIsIgnored(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 1)
	attacker, victim := p.patient("Mallory"), p.patient("Viktor")
	b := p.mustBook(attacker, p.service, p.at(0, 9, 0), obj{"patient_id": victim.ID})
	if num(h.want(200, "GET", "/v1/bookings", victim.tok(), nil), "total") != 0 {
		t.Fatal("the victim must not get a booking")
	}
	if got := h.want(200, "GET", "/v1/bookings/"+str(b, "id"), attacker.tok(), nil); got["status"] != "bestaetigt" {
		t.Fatalf("the booking belongs to the caller: %v", got)
	}
	if _, ok := h.mail.last("booking_confirmed", victim.Email); ok {
		t.Fatal("the victim must not get a mail")
	}
	// The doctor cannot be chosen either: a doctor_id in a patient's body is ignored, not even checked.
	outsider := makeUser(t, "Zed", "doctor") // works, but does not offer the service
	other := str(h.want(201, "POST", "/v1/specialties", p.org.tok(), obj{"name": "Fremd-" + fmt.Sprint(time.Now().UnixNano())}), "id")
	p.setSchedule(outsider, other, 8, 12)
	got := p.mustBook(p.patient("Pia"), p.service, p.at(1, 9, 0), obj{"doctor_id": outsider.ID})
	if got["doctor"].(obj)["id"] != p.doctors[0].ID {
		t.Fatalf("the doctor follows the rules, not the body: %v", got["doctor"])
	}
}

// Automatic assignment: the least busy doctor of the day; the main doctor if the patient has one.
func TestAutomaticAssignment(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 2)
	d1, d2 := p.doctors[0], p.doctors[1]

	first := p.mustBook(p.patient("A"), p.service, p.at(0, 8, 0), nil)
	second := p.mustBook(p.patient("B"), p.service, p.at(0, 9, 0), nil)
	if first["doctor"].(obj)["id"] == second["doctor"].(obj)["id"] {
		t.Fatalf("the second booking of the day goes to the less busy doctor: %v / %v", first["doctor"], second["doctor"])
	}

	// The same time again: both doctors are busy at 08:00 and 09:00 respectively, so the other one takes it.
	third := p.mustBook(p.patient("C"), p.service, p.at(0, 8, 0), nil)
	if third["doctor"].(obj)["id"] == first["doctor"].(obj)["id"] {
		t.Fatalf("the busy doctor cannot take it: %v", third["doctor"])
	}
	st, out := p.book(p.patient("D"), p.service, p.at(0, 8, 0), nil)
	if st != 409 || out["code"] != "slot_taken" {
		t.Fatalf("all doctors busy: %d %v", st, out)
	}

	// A patient with a main doctor is never given another doctor, even when that one is free (UF-05 case A).
	mainPat := p.patient("Main")
	h.want(200, "PUT", "/v1/patients/"+mainPat.ID+"/main-doctor", p.front.tok(), obj{"doctor_id": d1.ID})
	for day := 1; day <= 3; day++ {
		got := p.mustBook(mainPat, p.service, p.at(day, 9, 0), nil)
		if got["doctor"].(obj)["id"] != d1.ID {
			t.Fatalf("main doctor expected: %v", got["doctor"])
		}
	}
	// If the main doctor is busy the booking fails although the other doctor is free: the patient asked for this doctor.
	p.h.want(201, "POST", "/v1/bookings", p.front.tok(), obj{"service_id": p.service, "starts_at": ts(p.at(4, 9, 0)), "patient_id": p.patient("F").ID, "doctor_id": d1.ID})
	st, out = p.book(mainPat, p.service, p.at(4, 9, 0), nil)
	if st != 409 || out["code"] != "slot_taken" {
		t.Fatalf("main doctor busy: %d %v", st, out)
	}
	_ = d2
}

// The plan: two parallel bookings of one slot give exactly one success. With two doctors, two succeed, each with a
// different doctor; the loser of a race moves on to the next doctor.
func TestParallelBookings(t *testing.T) {
	h := newHarness(t)
	for _, doctors := range []int{1, 2} {
		p := newPractice(h, doctors)
		start := p.at(0, 10, 0)
		const requests = 12
		patients := make([]*user, requests)
		for i := range patients {
			patients[i] = p.patient(fmt.Sprintf("P%d", i))
		}
		var created, conflicts, other atomic.Int32
		seen := sync.Map{}
		var wg sync.WaitGroup
		for _, pat := range patients {
			wg.Add(1)
			go func() {
				defer wg.Done()
				st, out := p.book(pat, p.service, start, nil)
				switch st {
				case 201:
					created.Add(1)
					if _, dup := seen.LoadOrStore(out["doctor"].(obj)["id"], true); dup {
						other.Add(100)
					}
				case 409:
					conflicts.Add(1)
				default:
					other.Add(1)
				}
			}()
		}
		wg.Wait()
		if int(created.Load()) != doctors || int(conflicts.Load()) != requests-doctors || other.Load() != 0 {
			t.Fatalf("%d doctor(s): created=%d conflicts=%d other=%d", doctors, created.Load(), conflicts.Load(), other.Load())
		}
	}
}

// UF-06: "Sonstiges" has no doctor until reception assigns one.
func TestSonstigesAndAssignment(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 2)
	pat := p.patient("Paula")
	start := p.at(0, 9, 0)

	b := p.mustBook(pat, p.other, start, nil)
	if b["doctor"] != nil || !sameInstant(t, str(b, "ends_at"), start.Add(time.Hour)) {
		t.Fatalf("Sonstiges blocks one hour and has no doctor: %v", b)
	}
	id := str(b, "id")
	if m, _ := h.mail.last("booking_confirmed", pat.Email); m.Doctor != "" {
		t.Fatalf("the mail must say that the doctor is assigned later: %+v", m)
	}
	// It appears in the list "Zuordnung offen" of reception, filtered to this patient.
	open := h.want(200, "GET", "/v1/bookings?unassigned=true&patient_id="+pat.ID, p.front.tok(), nil)
	if num(open, "total") != 1 {
		t.Fatalf("unassigned: %v", open)
	}
	// A patient cannot assign (403); a doctor busy at that time cannot take it (409); an outsider is refused (422).
	h.wantCode(403, "forbidden", "PUT", "/v1/bookings/"+id+"/doctor", pat.tok(), obj{"doctor_id": p.doctors[0].ID})
	h.want(201, "POST", "/v1/bookings", p.front.tok(), obj{"service_id": p.service, "starts_at": ts(p.at(0, 9, 30)), "patient_id": p.patient("Busy").ID, "doctor_id": p.doctors[0].ID})
	h.wantCode(409, "slot_taken", "PUT", "/v1/bookings/"+id+"/doctor", p.front.tok(), obj{"doctor_id": p.doctors[0].ID})
	h.wantCode(422, "doctor_not_offering", "PUT", "/v1/bookings/"+id+"/doctor", p.front.tok(), obj{"doctor_id": pat.ID})
	late := p.mustBook(p.patient("Late"), p.other, p.at(0, 11, 30), nil) // 11:30-12:30 leaves the working time
	h.wantCode(409, "outside_working_time", "PUT", "/v1/bookings/"+str(late, "id")+"/doctor", p.front.tok(), obj{"doctor_id": p.doctors[1].ID})

	got := h.want(200, "PUT", "/v1/bookings/"+id+"/doctor", p.front.tok(), obj{"doctor_id": p.doctors[1].ID})
	if got["doctor"].(obj)["id"] != p.doctors[1].ID {
		t.Fatalf("assigned: %v", got)
	}
	if m, ok := h.mail.last("booking_changed", pat.Email); !ok || m.Doctor != "Doc2 Test" {
		t.Fatalf("change mail: %+v", m)
	}
	if num(h.want(200, "GET", "/v1/bookings?unassigned=true&patient_id="+pat.ID, p.front.tok(), nil), "total") != 0 {
		t.Fatal("assigned bookings leave the list")
	}
	if num(h.want(200, "GET", "/v1/bookings", p.doctors[1].tok(), nil), "total") != 1 {
		t.Fatal("the doctor now sees it in the own calendar")
	}
}

// UF-08: staff book for a patient and may choose the doctor.
func TestStaffBooksForPatient(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 2)
	pat := p.patient("Paula")
	start := p.at(0, 9, 0)
	body := func(extra obj) obj {
		b := obj{"service_id": p.service, "starts_at": ts(start)}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}

	h.wantCode(422, "validation_failed", "POST", "/v1/bookings", p.front.tok(), body(nil))                                               // patient_id required
	h.wantCode(404, "not_found", "POST", "/v1/bookings", p.front.tok(), body(obj{"patient_id": "00000000-0000-0000-0000-000000000042"})) // unknown patient
	h.wantCode(404, "not_found", "POST", "/v1/bookings", p.front.tok(), body(obj{"patient_id": p.doctors[0].ID}))                        // a doctor is no patient
	outsider := makeUser(t, "Zed", "doctor")
	h.wantCode(409, "doctor_unavailable", "POST", "/v1/bookings", p.front.tok(), body(obj{"patient_id": pat.ID, "doctor_id": outsider.ID}))
	got := h.want(201, "POST", "/v1/bookings", p.front.tok(), body(obj{"patient_id": pat.ID, "doctor_id": p.doctors[1].ID}))
	if got["doctor"].(obj)["id"] != p.doctors[1].ID || got["patient"].(obj)["id"] != pat.ID {
		t.Fatalf("staff booking: %v", got)
	}
	if _, ok := h.mail.last("booking_confirmed", pat.Email); !ok {
		t.Fatal("the patient gets a confirmation")
	}
	// The organisator may do the same; a staff member who is also a patient may book for himself or herself.
	h.want(201, "POST", "/v1/bookings", p.org.tok(), body(obj{"patient_id": pat.ID, "starts_at": ts(p.at(1, 9, 0))}))
	both := makeUser(t, "Both", "desk", "patient")
	self := h.want(201, "POST", "/v1/bookings", both.tok(), body(obj{"starts_at": ts(p.at(2, 9, 0))}))
	if self["patient"].(obj)["id"] != both.ID {
		t.Fatalf("self booking: %v", self)
	}
	// Doctors cannot book, and neither can the system administrator.
	h.wantCode(403, "forbidden", "POST", "/v1/bookings", p.doctors[0].tok(), body(nil))
	h.wantCode(403, "forbidden", "POST", "/v1/bookings", makeUser(t, "Admin", "systemadmin").tok(), body(nil))
}

func TestBookingValidation(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 1)
	pat := p.patient("Paula")
	post := func(b obj) (int, obj) { return h.req("POST", "/v1/bookings", pat.tok(), b) }

	for name, c := range map[string]struct {
		body obj
		want int
		code string
	}{
		"in the past":       {obj{"service_id": p.service, "starts_at": ts(time.Now().Add(-time.Hour).Truncate(time.Minute))}, 422, "starts_in_past"},
		"with seconds":      {obj{"service_id": p.service, "starts_at": p.at(0, 9, 0).Add(17 * time.Second).Format(time.RFC3339)}, 422, "validation_failed"},
		"outside work time": {obj{"service_id": p.service, "starts_at": ts(p.at(0, 18, 0))}, 409, "slot_taken"},
		"weekend":           {obj{"service_id": p.service, "starts_at": ts(p.at(5, 9, 0))}, 409, "slot_taken"},
		"half outside":      {obj{"service_id": p.service, "starts_at": ts(p.at(0, 11, 45))}, 409, "slot_taken"},
		"unknown service":   {obj{"service_id": "00000000-0000-0000-0000-000000000042", "starts_at": ts(p.at(0, 9, 0))}, 404, "not_found"},
		"unknown field":     {obj{"service_id": p.service, "starts_at": ts(p.at(0, 9, 0)), "status": "abgeschlossen"}, 422, "validation_failed"},
		"reason too long":   {obj{"service_id": p.service, "starts_at": ts(p.at(0, 9, 0)), "reason": string(make([]byte, 201))}, 422, "validation_failed"},
		"missing time":      {obj{"service_id": p.service}, 422, "validation_failed"},
	} {
		st, out := post(c.body)
		if st != c.want || out["code"] != c.code {
			t.Errorf("%s: %d %v, want %d %s", name, st, out, c.want, c.code)
		}
	}
	if num(h.want(200, "GET", "/v1/bookings", pat.tok(), nil), "total") != 0 {
		t.Fatal("no invalid request may create a booking")
	}
}

// UF-08: reception reschedules; the patient is informed; the working time still applies.
func TestStaffReschedulesBooking(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 1)
	pat := p.patient("Paula")
	b := p.mustBook(pat, p.service, p.at(0, 9, 0), obj{"reason": "Kontrolle"})
	id := str(b, "id")

	h.wantCode(403, "forbidden", "PATCH", "/v1/bookings/"+id, pat.tok(), obj{"reason": "x"})
	h.wantCode(403, "forbidden", "PATCH", "/v1/bookings/"+id, p.doctors[0].tok(), obj{"reason": "x"})
	h.wantCode(404, "not_found", "PATCH", "/v1/bookings/00000000-0000-0000-0000-000000000042", p.front.tok(), obj{"reason": "x"})
	h.wantCode(422, "validation_failed", "PATCH", "/v1/bookings/"+id, p.front.tok(), obj{})

	got := h.want(200, "PATCH", "/v1/bookings/"+id, p.front.tok(), obj{"starts_at": ts(p.at(0, 10, 0))})
	if !sameInstant(t, str(got, "starts_at"), p.at(0, 10, 0)) || !sameInstant(t, str(got, "ends_at"), p.at(0, 10, 30)) {
		t.Fatalf("moved: %v", got)
	}
	if _, ok := h.mail.last("booking_changed", pat.Email); !ok {
		t.Fatal("the patient must be informed")
	}
	h.wantCode(409, "outside_working_time", "PATCH", "/v1/bookings/"+id, p.front.tok(), obj{"starts_at": ts(p.at(0, 18, 0))})
	h.wantCode(422, "starts_in_past", "PATCH", "/v1/bookings/"+id, p.front.tok(), obj{"starts_at": ts(time.Now().Add(-time.Hour).Truncate(time.Minute))})
	// Moving onto a taken time is refused by the database.
	p.mustBook(p.patient("Other"), p.service, p.at(0, 11, 0), nil)
	h.wantCode(409, "slot_taken", "PATCH", "/v1/bookings/"+id, p.front.tok(), obj{"starts_at": ts(p.at(0, 11, 0))})
	// Changing the service recomputes the end; reason and info change without a mail.
	before := len(h.mail.all())
	got = h.want(200, "PATCH", "/v1/bookings/"+id, p.org.tok(), obj{"reason": "Neu", "info": ""})
	if got["reason"] != "Neu" || got["info"] != nil || len(h.mail.all()) != before {
		t.Fatalf("reason/info: %v", got)
	}
	got = h.want(200, "PATCH", "/v1/bookings/"+id, p.org.tok(), obj{"service_id": p.service60})
	if !sameInstant(t, str(got, "ends_at"), p.at(0, 11, 0)) {
		t.Fatalf("a 60 minute service ends an hour after the start: %v", got)
	}
}

// A person who is a doctor and a patient sees both: the own calendar and the own bookings.
func TestDoctorWhoIsAlsoAPatient(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 2)
	both := makeUser(t, "Both", "doctor", "patient")
	p.setSchedule(both, p.specialty, 8, 12)
	patient := p.patient("Paula")
	h.want(200, "PUT", "/v1/patients/"+patient.ID+"/main-doctor", p.front.tok(), obj{"doctor_id": both.ID})
	p.mustBook(patient, p.service, p.at(0, 9, 0), nil) // in the calendar of "both"
	own := p.mustBook(both, p.service, p.at(1, 9, 0), nil)

	list := h.want(200, "GET", "/v1/bookings", both.tok(), nil)
	if num(list, "total") != 2 || len(items(list)) != 2 {
		t.Fatalf("both lists: %v", list)
	}
	h.want(200, "GET", "/v1/bookings/"+str(own, "id"), both.tok(), nil)
	for _, b := range items(list) {
		if b.(obj)["patient"] == nil {
			t.Fatalf("a doctor sees the patient: %v", b)
		}
	}
}

func TestBookingRateLimit(t *testing.T) {
	h := newHarness(t, 2)
	p := newPractice(h, 1)
	pat := p.patient("Paula")
	for i := 0; i < 2; i++ {
		p.mustBook(pat, p.service, p.at(i, 9, 0), nil)
	}
	st, out := p.book(pat, p.service, p.at(3, 9, 0), nil)
	if st != 429 || out["code"] != "rate_limited" {
		t.Fatalf("third booking: %d %v", st, out)
	}
	p.mustBook(p.patient("Other"), p.service, p.at(3, 9, 0), nil) // another user is not limited
}
