// Package templates holds the fixed set of mails
package templates

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
	"time"
	_ "time/tzdata" // the distroless image has no zoneinfo
)

//go:embed tmpl/*.txt
var files embed.FS

// Param names as in the API.
const (
	FirstName  = "first_name"
	Link       = "link"
	Service    = "service"
	DoctorName = "doctor_name"
	StartsAt   = "starts_at"
	Time       = "time"
)

// Spec says which parameters a template needs and may take, and which service may send it.
type Spec struct {
	Required []string
	Optional []string
	Callers  []string
}

var (
	user     = []string{"user_service"}
	calendar = []string{"calendar_service"}
)

// Specs is the contract of api/internal.md section 5. A parameter that is not listed is rejected.
var Specs = map[string]Spec{
	"verify_email":         {Required: []string{FirstName, Link}, Callers: user},
	"password_reset":       {Required: []string{FirstName, Link}, Callers: user},
	"email_change_confirm": {Required: []string{FirstName, Link}, Callers: user},
	"email_change_notice":  {Required: []string{FirstName}, Callers: user},
	"login_warning":        {Required: []string{FirstName, Time}, Callers: user},
	"account_deleted":      {Required: []string{FirstName}, Callers: user},
	"booking_confirmed":    {Required: []string{FirstName, Service, StartsAt}, Optional: []string{DoctorName}, Callers: calendar},
	"booking_changed":      {Required: []string{FirstName, Service, StartsAt}, Optional: []string{DoctorName}, Callers: calendar},
	"booking_cancelled":    {Required: []string{FirstName, Service, StartsAt}, Callers: calendar},
	"booking_reminder":     {Required: []string{FirstName, Service, StartsAt}, Callers: calendar},
}

// Allowed reports whether the caller may send the template.
func (s Spec) Allowed(caller string) bool {
	for _, c := range s.Callers {
		if c == caller {
			return true
		}
	}
	return false
}

// Data is the input of a template. Times are already formatted.
type Data struct {
	Practice   string
	FirstName  string
	Link       string
	Service    string
	DoctorName string
	StartsAt   string
	Time       string
}

// Renderer renders the templates.
type Renderer struct {
	tpl *template.Template
	loc *time.Location
}

// New parses the embedded templates. Times are shown in the practice time zone (decision 18).
func New(timezone string) (*Renderer, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, err
	}
	t, err := template.ParseFS(files, "tmpl/*.txt")
	if err != nil {
		return nil, err
	}
	for name := range Specs {
		if t.Lookup(name+".txt") == nil {
			return nil, fmt.Errorf("template %s missing", name)
		}
	}
	return &Renderer{tpl: t, loc: loc}, nil
}

// FormatTime formats a time for the mail, e.g. "02.01.2030, 09:30 Uhr".
func (r *Renderer) FormatTime(t time.Time) string {
	return t.In(r.loc).Format("02.01.2006, 15:04") + " Uhr"
}

// Render returns subject and body of a template.
func (r *Renderer) Render(name string, d Data) (subject, body string, err error) {
	var sb strings.Builder
	if err := r.tpl.ExecuteTemplate(&sb, name+".txt", d); err != nil {
		return "", "", err
	}
	head, rest, ok := strings.Cut(sb.String(), "\n\n")
	subject, found := strings.CutPrefix(head, "Subject: ")
	if !ok || !found || strings.ContainsAny(subject, "\r\n") {
		return "", "", fmt.Errorf("template %s: malformed", name)
	}
	return subject, rest, nil
}
