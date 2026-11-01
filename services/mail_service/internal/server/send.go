package server

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/api"
	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/smtpout"
	"github.com/juliandevelops-studium/my-doctors-date/services/mail_service/internal/templates"
)

// SendMail sends one templated mail. Checks, in order: the template exists and the caller may send it (403), the
// parameters are exactly the ones of the template (422), the link points to the frontend (422), the rate limits (429).
func (s *Server) SendMail(ctx context.Context, req api.SendMailRequestObject) (api.SendMailResponseObject, error) {
	caller, _ := ctx.Value(ctxKey{}).(string)
	b := req.Body
	name := string(b.Template)
	spec, ok := templates.Specs[name]
	if !ok || !spec.Allowed(caller) {
		s.Log.Warn("template not allowed", "caller", caller, "template", name)
		return nil, &apiError{status: http.StatusForbidden, code: "template_not_allowed", title: "This service may not send this template"}
	}

	given := map[string]string{}
	put := func(key string, v *string) {
		if v != nil {
			given[key] = *v
		}
	}
	put(templates.FirstName, b.Params.FirstName)
	put(templates.Link, b.Params.Link)
	put(templates.Service, b.Params.Service)
	put(templates.DoctorName, b.Params.DoctorName)
	if err := checkParams(spec, given, b.Params.StartsAt != nil, b.Params.Time != nil); err != nil {
		return nil, err
	}
	if link, has := given[templates.Link]; has && !strings.HasPrefix(link, s.LinkBase+"/") {
		return nil, invalid("params.link", "foreign_link")
	}

	to := string(b.To)
	if !smtpout.ValidAddress(to) {
		return nil, invalid("to", "invalid_address")
	}
	if ok, retry := s.byCaller.Allow(caller); !ok {
		return nil, &apiError{status: http.StatusTooManyRequests, code: "rate_limited", title: "Too many mails", retryAfter: retry}
	}
	if ok, retry := s.byRecipient.Allow(strings.ToLower(to)); !ok {
		return nil, &apiError{status: http.StatusTooManyRequests, code: "rate_limited", title: "Too many mails for this recipient", retryAfter: retry}
	}

	d := templates.Data{Practice: s.Practice, FirstName: given[templates.FirstName], Link: given[templates.Link],
		Service: given[templates.Service], DoctorName: given[templates.DoctorName]}
	if b.Params.StartsAt != nil {
		d.StartsAt = s.Renderer.FormatTime(*b.Params.StartsAt)
	}
	if b.Params.Time != nil {
		d.Time = s.Renderer.FormatTime(*b.Params.Time)
	}
	subject, body, err := s.Renderer.Render(name, d)
	if err != nil {
		return nil, err
	}
	if _, err := s.Mailer.Send(ctx, to, subject, body); err != nil {
		s.Log.Error("smtp delivery failed", "template", name, "caller", caller, "error", err.Error())
		return nil, &apiError{status: http.StatusBadGateway, code: "smtp_failed", title: "The mail server did not accept the message"}
	}
	s.Log.Info("mail sent", "template", name, "caller", caller) // no recipient in the log
	return api.SendMail202JSONResponse{MessageId: uuid.New()}, nil
}

func invalid(field, code string) *apiError {
	return &apiError{status: http.StatusUnprocessableEntity, code: "validation_failed", title: "Validation failed: " + field + " (" + code + ")"}
}

// checkParams enforces that exactly the required parameters (and optionally the optional ones) are present, and that
// text parameters are single-line without control characters (no header or layout injection).
func checkParams(spec templates.Spec, given map[string]string, hasStartsAt, hasTime bool) *apiError {
	if hasStartsAt {
		given[templates.StartsAt] = ""
	}
	if hasTime {
		given[templates.Time] = ""
	}
	allowed := map[string]bool{}
	for _, k := range spec.Required {
		allowed[k] = true
		if _, ok := given[k]; !ok {
			return invalid("params."+k, "required")
		}
	}
	for _, k := range spec.Optional {
		allowed[k] = true
	}
	for k, v := range given {
		if !allowed[k] {
			return invalid("params."+k, "not_allowed")
		}
		for _, r := range v {
			if unicode.IsControl(r) {
				return invalid("params."+k, "control_character")
			}
		}
		if k != templates.StartsAt && k != templates.Time && strings.TrimSpace(v) == "" && slices.Contains(spec.Required, k) {
			return invalid("params."+k, "empty")
		}
	}
	return nil
}
