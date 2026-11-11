package server_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
)

// UF-09: only the organisator manages the Leistungsangebot and the schedules.
func TestPracticeManagement(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 1)
	patient := p.patient("Pia")
	doc := p.doctors[0]

	// Reading is open to everyone who books or works; writing only to the organisator.
	h.want(200, "GET", "/v1/specialties", patient.tok(), nil)
	h.want(200, "GET", "/v1/services", p.doctors[0].tok(), nil)
	for _, c := range []struct{ method, path string }{
		{"POST", "/v1/specialties"}, {"POST", "/v1/services"}, {"POST", "/v1/services/import"},
		{"PUT", "/v1/services/" + p.service}, {"DELETE", "/v1/services/" + p.service},
		{"DELETE", "/v1/specialties/" + p.specialty}, {"PUT", "/v1/doctors/" + doc.ID + "/schedule"},
	} {
		for _, u := range []*user{patient, doc, p.front} {
			h.wantCode(403, "forbidden", c.method, c.path, u.tok(), obj{})
		}
	}

	// Validation.
	h.wantCode(422, "specialty_required", "POST", "/v1/services", p.org.tok(), obj{"name": "x-" + uuid.NewString(), "duration_minutes": 30})
	h.wantCode(422, "validation_failed", "POST", "/v1/services", p.org.tok(), obj{"name": "x", "duration_minutes": 4, "specialty_id": p.specialty})
	name := "Dup-" + uuid.NewString()
	h.want(201, "POST", "/v1/specialties", p.org.tok(), obj{"name": name})
	h.wantCode(409, "duplicate", "POST", "/v1/specialties", p.org.tok(), obj{"name": name})

	// Update and delete; a specialty in use cannot be deleted; "Sonstiges" cannot be deleted.
	upd := h.want(200, "PUT", "/v1/services/"+p.service, p.org.tok(), obj{"name": "Neu-" + uuid.NewString(), "duration_minutes": 45, "specialty_id": p.specialty})
	if num(upd, "duration_minutes") != 45 {
		t.Fatalf("update: %v", upd)
	}
	h.wantCode(409, "in_use", "DELETE", "/v1/specialties/"+p.specialty, p.org.tok(), nil)
	h.wantCode(409, "is_other", "DELETE", "/v1/services/"+p.other, p.org.tok(), nil)
	h.want(204, "DELETE", "/v1/services/"+p.service60, p.org.tok(), nil)
	h.wantCode(404, "not_found", "DELETE", "/v1/services/"+p.service60, p.org.tok(), nil)

	// Schedule: overlap and unknown specialty are rejected, the doctor list shows the specialty.
	day := func(wd int, a, b string) obj { return obj{"weekday": wd, "start_time": a, "end_time": b} }
	h.wantCode(422, "working_days_overlap", "PUT", "/v1/doctors/"+doc.ID+"/schedule", p.org.tok(),
		obj{"specialty_ids": []string{p.specialty}, "working_days": []obj{day(1, "08:00", "12:00"), day(1, "11:00", "14:00")}})
	h.wantCode(422, "unknown_specialty", "PUT", "/v1/doctors/"+doc.ID+"/schedule", p.org.tok(),
		obj{"specialty_ids": []string{uuid.NewString()}, "working_days": []obj{}})
	h.wantCode(404, "not_found", "PUT", "/v1/doctors/"+uuid.NewString()+"/schedule", p.org.tok(), obj{"specialty_ids": []string{}, "working_days": []obj{}})
	sch := h.want(200, "GET", "/v1/doctors/"+doc.ID+"/schedule", patient.tok(), nil)
	if len(items(obj{"items": sch["working_days"]})) != 5 {
		t.Fatalf("schedule: %v", sch)
	}
	found := false
	for _, d := range items(h.want(200, "GET", "/v1/doctors?specialty_id="+p.specialty, patient.tok(), nil)) {
		if str(d.(obj), "id") == doc.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("the doctor must be listed under the specialty")
	}
}

// UF-09: JSON template import with dry run.
func TestServiceImport(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 0)
	spName, svName := "Imp-"+uuid.NewString(), "ImpSv-"+uuid.NewString()
	file := func(dry bool) string {
		return fmt.Sprintf("/v1/services/import?dry_run=%v", dry)
	}
	body := obj{"specialties": []obj{{"name": spName}}, "services": []obj{{"name": svName, "duration_minutes": 20, "specialty": spName}}}

	dry := h.want(200, "POST", file(true), p.org.tok(), body)
	if num(dry, "services_created") != 1 || dry["dry_run"] != true {
		t.Fatalf("dry run: %v", dry)
	}
	has := func() bool {
		for _, s := range items(h.want(200, "GET", "/v1/services", p.org.tok(), nil)) {
			if str(s.(obj), "name") == svName {
				return true
			}
		}
		return false
	}
	if has() {
		t.Fatal("a dry run must not store anything")
	}
	h.want(200, "POST", file(false), p.org.tok(), body)
	if !has() {
		t.Fatal("the import must store the service")
	}
	h.wantCode(422, "unknown_specialty", "POST", file(false), p.org.tok(), obj{"services": []obj{{"name": "Q-" + uuid.NewString(), "duration_minutes": 20, "specialty": "gibt-es-nicht"}}})
	h.wantCode(422, "validation_failed", "POST", file(false), p.org.tok(), obj{"services": []obj{{"name": "Q", "duration_minutes": 20, "price": 5}}})
}

// UF-08: patient search and main doctor, reception and organisator only (the system administrator has no access).
func TestPatientsAndMainDoctor(t *testing.T) {
	h := newHarness(t)
	p := newPractice(h, 1)
	pat := p.patient("Paula")
	doc := p.doctors[0]

	for _, u := range []*user{pat, doc, makeUser(t, "Admin", "systemadmin")} {
		h.wantCode(403, "forbidden", "GET", "/v1/patients", u.tok(), nil)
		h.wantCode(403, "forbidden", "PUT", "/v1/patients/"+pat.ID+"/main-doctor", u.tok(), obj{"doctor_id": doc.ID})
	}
	res := h.want(200, "GET", "/v1/patients?q="+pat.Email, p.front.tok(), nil)
	if num(res, "total") != 1 || str(items(res)[0].(obj), "id") != pat.ID {
		t.Fatalf("search: %v", res)
	}
	if num(h.want(200, "GET", "/v1/patients?q=%25%25", p.front.tok(), nil), "total") != 0 {
		t.Fatal("LIKE wildcards in the search must be literal")
	}
	got := h.want(200, "PUT", "/v1/patients/"+pat.ID+"/main-doctor", p.front.tok(), obj{"doctor_id": doc.ID})
	if got["main_doctor_id"] != doc.ID {
		t.Fatalf("main doctor: %v", got)
	}
	h.wantCode(422, "not_a_doctor", "PUT", "/v1/patients/"+pat.ID+"/main-doctor", p.front.tok(), obj{"doctor_id": pat.ID})
	h.wantCode(404, "not_found", "PUT", "/v1/patients/"+doc.ID+"/main-doctor", p.front.tok(), obj{"doctor_id": doc.ID}) // a doctor is no patient
	got = h.want(200, "PUT", "/v1/patients/"+pat.ID+"/main-doctor", p.org.tok(), obj{"doctor_id": nil})
	if got["main_doctor_id"] != nil {
		t.Fatalf("cleared: %v", got)
	}
}
