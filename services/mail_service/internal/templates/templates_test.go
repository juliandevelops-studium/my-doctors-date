package templates

import (
	"strings"
	"testing"
	"time"

	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/api"
)

// The templates and the API enum must stay in step.
func TestSpecsMatchTheApiEnum(t *testing.T) {
	spec, err := api.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	var enum []string
	for _, v := range spec.Components.Schemas["MailTemplate"].Value.Enum {
		enum = append(enum, v.(string))
	}
	if len(enum) != len(Specs) {
		t.Fatalf("enum has %d templates, Specs %d", len(enum), len(Specs))
	}
	for _, name := range enum {
		if _, ok := Specs[name]; !ok {
			t.Errorf("template %s is in the API but has no spec", name)
		}
	}
	props := spec.Components.Schemas["MailParams"].Value.Properties
	for name, s := range Specs {
		for _, p := range append(append([]string{}, s.Required...), s.Optional...) {
			if _, ok := props[p]; !ok {
				t.Errorf("%s: parameter %s is not part of MailParams", name, p)
			}
		}
	}
}

func TestEveryTemplateRendersGerman(t *testing.T) {
	r, err := New("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2030, 1, 2, 8, 30, 0, 0, time.UTC) // 09:30 in Berlin
	d := Data{Practice: "Praxis Dr. Muster", FirstName: "Erika", Link: "https://praxis.test/x?token=abc", Service: "Kontrolltermin",
		DoctorName: "Dr. Muster", StartsAt: r.FormatTime(when), Time: r.FormatTime(when)}
	for name, spec := range Specs {
		subject, body, err := r.Render(name, d)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if subject == "" || strings.ContainsAny(subject, "\r\n") {
			t.Errorf("%s: subject %q", name, subject)
		}
		if !strings.Contains(body, "Guten Tag Erika,") || !strings.Contains(body, "Praxis Dr. Muster") {
			t.Errorf("%s: greeting or signature missing", name)
		}
		for _, p := range spec.Required {
			switch p {
			case Link:
				if !strings.Contains(body, d.Link) {
					t.Errorf("%s: link missing", name)
				}
			case StartsAt, Time:
				if !strings.Contains(body, "02.01.2030, 09:30 Uhr") {
					t.Errorf("%s: time missing or in the wrong zone: %s", name, body)
				}
			case Service:
				if !strings.Contains(body, "Kontrolltermin") {
					t.Errorf("%s: service missing", name)
				}
			}
		}
		// Data minimisation: nothing about the reason of the visit.
		for _, bad := range []string{"Grund", "Infos", "Diagnose"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: body mentions %q", name, bad)
			}
		}
	}
}

func TestUnassignedDoctor(t *testing.T) {
	r, _ := New("Europe/Berlin")
	_, with, _ := r.Render("booking_confirmed", Data{FirstName: "A", Service: "S", StartsAt: "x", DoctorName: "Dr. Muster"})
	_, without, _ := r.Render("booking_confirmed", Data{FirstName: "A", Service: "S", StartsAt: "x"})
	if !strings.Contains(with, "Dr. Muster") || !strings.Contains(without, "wird von der Praxis zugeordnet") {
		t.Fatalf("with:\n%s\nwithout:\n%s", with, without)
	}
}

func TestCallerAllowList(t *testing.T) {
	for name, s := range Specs {
		isBooking := strings.HasPrefix(name, "booking_")
		if s.Allowed("calendar_service") != isBooking || s.Allowed("user_service") == isBooking {
			t.Errorf("%s: wrong callers %v", name, s.Callers)
		}
		if s.Allowed("frontend_provider") || s.Allowed("") {
			t.Errorf("%s: unknown callers must not be allowed", name)
		}
	}
}
