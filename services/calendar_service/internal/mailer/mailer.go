// Package mailer sends the booking mails of calendar_service through mail_service. A mail never contains the reason
// or the infos of a booking.
// Failures are logged and do not fail the request: the caller answers neutrally either way (UF-01, UF-12).
package mailer

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/mailclient"
)

// Mailer sends mails.
type Mailer struct {
	c   *mailclient.ClientWithResponses
	log *slog.Logger
}

// New creates a mailer.
func New(mailServiceURL string, signer *praxisauth.ServiceSigner, log *slog.Logger) (*Mailer, error) {
	c, err := mailclient.NewClientWithResponses(mailServiceURL,
		mailclient.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}),
		mailclient.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+signer.Token("mail_service", ""))
			return nil
		}))
	if err != nil {
		return nil, err
	}
	return &Mailer{c: c, log: log}, nil
}

func (m *Mailer) send(ctx context.Context, tpl mailclient.MailTemplate, to string, p mailclient.MailParams) {
	r, err := m.c.SendMailWithResponse(ctx, mailclient.MailRequest{Template: tpl, To: openapi_types.Email(to), Params: p})
	switch {
	case err != nil:
		m.log.Error("mail not sent", "template", tpl, "error", err.Error())
	case r.StatusCode() != http.StatusAccepted:
		m.log.Error("mail rejected", "template", tpl, "status", r.StatusCode())
	}
}

// BookingMail is the data of a booking mail.
type BookingMail struct {
	To, FirstName, Service, Doctor string
	StartsAt                       time.Time
}

func (b BookingMail) params() mailclient.MailParams {
	p := mailclient.MailParams{FirstName: &b.FirstName, Service: &b.Service, StartsAt: &b.StartsAt}
	if b.Doctor != "" {
		p.DoctorName = &b.Doctor
	}
	return p
}

// Confirmed tells the patient about a new booking (UF-05, UF-06).
func (m *Mailer) Confirmed(ctx context.Context, b BookingMail) {
	m.send(ctx, mailclient.BookingConfirmed, b.To, b.params())
}

// Changed tells the patient about a changed booking or a newly assigned doctor (UF-06, UF-08).
func (m *Mailer) Changed(ctx context.Context, b BookingMail) {
	m.send(ctx, mailclient.BookingChanged, b.To, b.params())
}

// Cancelled tells the patient about a cancellation (UF-07, UF-08).
func (m *Mailer) Cancelled(ctx context.Context, b BookingMail) {
	p := b.params()
	p.DoctorName = nil // the cancellation mail has no doctor parameter
	m.send(ctx, mailclient.BookingCancelled, b.To, p)
}
