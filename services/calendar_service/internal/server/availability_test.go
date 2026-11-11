package server_test

import (
	"fmt"
	"net/url"
	"testing"
	"time"
)

func (p *practice) avail(u *user, service string, from, to time.Time, extra string) obj {
	p.h.t.Helper()
	q := url.Values{"service_id": {service}, "from": {ts(from)}, "to": {ts(to)}}
	return p.h.want(200, "GET", "/v1/calendar/availability?"+q.Encode()+extra, u.tok(), nil)
}

func startsOf(res obj) []string {
	var out []string
	for _, s := range items(res) {
		out = append(out, str(s.(obj), "starts_at"))
	}
	return out
}

// UF-05 case B and the union rule: a patient without a main doctor sees the union of all matching doctors, without
// doctor ids; a time stays offered as long as one doctor is free.
func TestAvailabilityUnionAndBusy(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 2)
	pat := p.patient("Paula")
	day0, day1 := p.at(0, 0, 0), p.at(1, 0, 0)

	free := p.avail(pat, p.service, day0, day1, "")
	// 08:00 to 12:00 with 30 minutes on a 15-minute grid: 08:00 ... 11:30 = 15 start times, once (union, not twice).
	if n := len(items(free)); n != 15 {
		t.Fatalf("%d start times, want 15: %v", n, startsOf(free))
	}
	if !sameInstant(t, str(items(free)[0].(obj), "starts_at"), p.at(0, 8, 0)) {
		t.Fatalf("first start: %v", items(free)[0])
	}
	for _, s := range items(free) {
		if _, has := s.(obj)["doctor_id"]; has {
			t.Fatalf("a patient without a main doctor must not see doctor ids: %v", s)
		}
	}

	// Doctor 1 is busy 08:00-08:30: the time is still offered (doctor 2 is free).
	p.h.want(201, "POST", "/v1/bookings", p.front.tok(), obj{"service_id": p.service, "starts_at": ts(p.at(0, 8, 0)), "patient_id": p.patient("Other").ID, "doctor_id": p.doctors[0].ID})
	if n := len(items(p.avail(pat, p.service, day0, day1, ""))); n != 15 {
		t.Fatalf("one free doctor keeps the time offered: %d", n)
	}
	// Doctor 2 is busy as well: 08:00 and 08:15 and 07:45... only the times overlapping 08:00-08:30 disappear.
	p.h.want(201, "POST", "/v1/bookings", p.front.tok(), obj{"service_id": p.service, "starts_at": ts(p.at(0, 8, 0)), "patient_id": p.patient("Other2").ID, "doctor_id": p.doctors[1].ID})
	got := startsOf(p.avail(pat, p.service, day0, day1, ""))
	if len(got) != 13 || !sameInstant(t, got[0], p.at(0, 8, 30)) {
		t.Fatalf("both busy: %v", got)
	}

	// Staff see the doctor of each time.
	staff := p.avail(p.front, p.service, day0, day1, "")
	for _, s := range items(staff) {
		if str(s.(obj), "doctor_id") == "" {
			t.Fatalf("staff must see doctor ids: %v", s)
		}
	}
	if len(items(staff)) != 26 { // two doctors x 13 free start times
		t.Fatalf("staff list: %d", len(items(staff)))
	}
}

// Decision: a patient with a main doctor sees only that doctor's times, although another doctor offers the service.
func TestAvailabilityOfAPatientWithMainDoctor(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 2)
	pat := p.patient("Paula")
	h.want(200, "PUT", "/v1/patients/"+pat.ID+"/main-doctor", p.front.tok(), obj{"doctor_id": p.doctors[0].ID})
	day0, day1 := p.at(0, 0, 0), p.at(1, 0, 0)

	res := p.avail(pat, p.service, day0, day1, "")
	if len(items(res)) != 15 {
		t.Fatalf("times of the main doctor only: %d", len(items(res)))
	}
	for _, s := range items(res) {
		if str(s.(obj), "doctor_id") != p.doctors[0].ID {
			t.Fatalf("only the main doctor: %v", s)
		}
	}
	// Staff asking for this patient get the same restriction.
	for _, s := range items(p.avail(p.front, p.service, day0, day1, "&patient_id="+pat.ID)) {
		if str(s.(obj), "doctor_id") != p.doctors[0].ID {
			t.Fatalf("staff for this patient: %v", s)
		}
	}
	// The main doctor does not offer the specialty of a service: nothing is offered.
	other := str(h.want(201, "POST", "/v1/specialties", p.org.tok(), obj{"name": "Anders-" + fmt.Sprint(time.Now().UnixNano())}), "id")
	svc := str(h.want(201, "POST", "/v1/services", p.org.tok(), obj{"name": "Anders-" + fmt.Sprint(time.Now().UnixNano()), "duration_minutes": 15, "specialty_id": other}), "id")
	if n := len(items(p.avail(pat, svc, day0, day1, ""))); n != 0 {
		t.Fatalf("service of another specialty: %d", n)
	}
}

func TestAvailabilitySpecialtyDurationAndSonstiges(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 2)
	// Doctor 2 loses the specialty: only doctor 1 offers the service, but both can take "Sonstiges".
	other := str(h.want(201, "POST", "/v1/specialties", p.org.tok(), obj{"name": "Zweit-" + fmt.Sprint(time.Now().UnixNano())}), "id")
	p.setSchedule(p.doctors[1], other, 8, 12)
	pat := p.patient("Paula")
	day0, day1 := p.at(0, 0, 0), p.at(1, 0, 0)

	staff := p.avail(p.front, p.service, day0, day1, "")
	for _, s := range items(staff) {
		if str(s.(obj), "doctor_id") != p.doctors[0].ID {
			t.Fatalf("only doctor 1 offers the service: %v", s)
		}
	}
	// 60 minutes: 08:00 ... 11:00 on the grid = 13 start times.
	if n := len(items(p.avail(pat, p.service60, day0, day1, ""))); n != 13 {
		t.Fatalf("60 minute service: %d start times", n)
	}
	// "Sonstiges" lasts 60 minutes and any doctor may take it; for the patient it is the union again.
	if n := len(items(p.avail(pat, p.other, day0, day1, ""))); n != 13 {
		t.Fatalf("Sonstiges: %d start times", n)
	}
	mine := 0 // other tests share the database, so count only the doctors of this practice
	for _, s := range items(p.avail(p.front, p.other, day0, day1, "")) {
		if id := str(s.(obj), "doctor_id"); id == p.doctors[0].ID || id == p.doctors[1].ID {
			mine++
		}
	}
	if mine != 26 {
		t.Fatalf("Sonstiges for staff: %d", mine)
	}
	// Weekend: no working time, nothing offered.
	if n := len(items(p.avail(pat, p.service, p.at(5, 0, 0), p.at(7, 0, 0), ""))); n != 0 {
		t.Fatalf("weekend: %d", n)
	}
}

func TestAvailabilityNeverOffersThePast(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 1)
	pat := p.patient("Paula")
	// Last week: no start time may be offered.
	past := nextWeekday(time.Monday, -1)
	if n := len(items(p.avail(pat, p.service, past, past.AddDate(0, 0, 1), ""))); n != 0 {
		t.Fatalf("%d start times in the past", n)
	}
}

func TestCalendarBlocksAndRanges(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 2)
	pat := p.patient("Paula")
	booking := p.mustBook(p.patient("Other"), p.service, p.at(0, 9, 0), nil)
	_ = booking

	for _, u := range []*user{pat, p.doctors[1], p.front, p.org} { // everyone who works with the calendar, no details
		var all []any
		for _, d := range p.doctors { // other tests share the database, so ask per doctor of this practice
			res := h.want(200, "GET", "/v1/calendar/blocks?doctor_id="+d.ID+"&from="+url.QueryEscape(ts(p.at(0, 0, 0)))+"&to="+url.QueryEscape(ts(p.at(1, 0, 0))), u.tok(), nil)
			all = append(all, items(res)...)
		}
		busy, free := 0, 0
		for _, b := range all {
			bm := b.(obj)
			if len(bm) != 4 { // doctor_id, starts_at, ends_at, state — and nothing about the patient
				t.Fatalf("block with details: %v", bm)
			}
			switch bm["state"] {
			case "busy":
				busy++
			case "free":
				free++
			}
		}
		if busy != 1 || free != 3 { // doctor 1: 08-09 free, 09-09:30 busy, 09:30-12 free; doctor 2: 08-12 free
			t.Fatalf("busy %d free %d: %v", busy, free, all)
		}
	}
	h.wantCode(403, "forbidden", "GET", "/v1/calendar/blocks?from=2030-01-01T00:00:00Z&to=2030-01-02T00:00:00Z", makeUser(t, "Admin", "systemadmin").tok(), nil)
	for _, q := range []string{
		"from=" + url.QueryEscape(ts(p.at(1, 0, 0))) + "&to=" + url.QueryEscape(ts(p.at(0, 0, 0))),  // to before from
		"from=" + url.QueryEscape(ts(p.at(0, 0, 0))) + "&to=" + url.QueryEscape(ts(p.at(80, 0, 0))), // more than 62 days
	} {
		h.wantCode(422, "validation_failed", "GET", "/v1/calendar/blocks?"+q, pat.tok(), nil)
		h.wantCode(422, "validation_failed", "GET", "/v1/calendar/availability?service_id="+p.service+"&"+q, pat.tok(), nil)
	}
	h.wantCode(422, "validation_failed", "GET", "/v1/calendar/blocks?from=gestern&to=heute", pat.tok(), nil)
	h.wantCode(404, "not_found", "GET", "/v1/calendar/availability?service_id=00000000-0000-0000-0000-000000000042&from="+url.QueryEscape(ts(p.at(0, 0, 0)))+"&to="+url.QueryEscape(ts(p.at(1, 0, 0))), pat.tok(), nil)
}
