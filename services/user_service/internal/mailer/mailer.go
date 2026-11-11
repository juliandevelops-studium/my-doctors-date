// Package mailer sends the templated mails of user_service through mail_service. The mail never contains a password.
// Failures are logged and do not fail the request: the caller answers neutrally either way (UF-01, UF-12).
package mailer

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/juliandevelops-studium/my-doctors-date/libs/auth"
	"github.com/juliandevelops-studium/my-doctors-date/services/user_service/internal/mailclient"
)

// Mailer sends mails.
type Mailer struct {
	c       *mailclient.ClientWithResponses
	baseURL string
	log     *slog.Logger
}

// New creates a mailer. baseURL is the public frontend address used in links.
func New(mailServiceURL, publicBaseURL string, signer *praxisauth.ServiceSigner, log *slog.Logger) (*Mailer, error) {
	c, err := mailclient.NewClientWithResponses(mailServiceURL,
		mailclient.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}),
		mailclient.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+signer.Token("mail_service", ""))
			return nil
		}))
	if err != nil {
		return nil, err
	}
	return &Mailer{c: c, baseURL: publicBaseURL, log: log}, nil
}

// Link builds a frontend link with a one-time token.
func (m *Mailer) Link(path, token string) string {
	return m.baseURL + path + "?token=" + url.QueryEscape(token)
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

// VerifyEmail sends the confirmation link of the registration (UF-01).
func (m *Mailer) VerifyEmail(ctx context.Context, to, firstName, token string) {
	link := m.Link("/verify-email", token)
	m.send(ctx, mailclient.VerifyEmail, to, mailclient.MailParams{FirstName: &firstName, Link: &link})
}

// PasswordReset sends the reset link (UF-10, UF-12).
func (m *Mailer) PasswordReset(ctx context.Context, to, firstName, token string) {
	link := m.Link("/reset-password", token)
	m.send(ctx, mailclient.PasswordReset, to, mailclient.MailParams{FirstName: &firstName, Link: &link})
}

// EmailChangeConfirm sends the confirmation link to the new address.
func (m *Mailer) EmailChangeConfirm(ctx context.Context, to, firstName, token string) {
	link := m.Link("/confirm-email-change", token)
	m.send(ctx, mailclient.EmailChangeConfirm, to, mailclient.MailParams{FirstName: &firstName, Link: &link})
}

// EmailChangeNotice tells the old address about the requested change.
func (m *Mailer) EmailChangeNotice(ctx context.Context, to, firstName string) {
	m.send(ctx, mailclient.EmailChangeNotice, to, mailclient.MailParams{FirstName: &firstName})
}

// LoginWarning reports a login (UF-02 step 7, optional).
func (m *Mailer) LoginWarning(ctx context.Context, to, firstName string, at time.Time) {
	m.send(ctx, mailclient.LoginWarning, to, mailclient.MailParams{FirstName: &firstName, Time: &at})
}

// AccountDeleted confirms the deletion (UF-13).
func (m *Mailer) AccountDeleted(ctx context.Context, to, firstName string) {
	m.send(ctx, mailclient.AccountDeleted, to, mailclient.MailParams{FirstName: &firstName})
}
