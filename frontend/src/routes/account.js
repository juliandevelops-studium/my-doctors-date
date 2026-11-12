// The own account: profile, e-mail, password, second factor, deletion (UF-04, UF-13).
import { clearSession, requireFullSession, requireLogin, setSession } from '../middleware.js';
import { fieldErrors, HttpError, problemMessage } from '../errors.js';
import { secret, text } from '../form.js';
import { rotateCsrf } from '../csrf.js';

export default function accountRoutes(app, { config }) {
  const guard = [requireLogin, requireFullSession];

  const render = (req, res, status = 200, extra = {}) =>
    res.status(status).render('account.njk', { title: 'Mein Konto', isPatient: req.session.me.roles.includes('patient'), ...extra });

  app.get('/account', guard, (req, res) => render(req, res));

  app.post('/account', guard, async (req, res) => {
    const v = {
      first_name: text(req.body, 'first_name', 100), last_name: text(req.body, 'last_name', 100), phone: text(req.body, 'phone', 30),
      street: text(req.body, 'street', 200), postal_code: text(req.body, 'postal_code', 20), city: text(req.body, 'city', 100),
    };
    const body = { first_name: v.first_name, last_name: v.last_name, phone: v.phone };
    if (req.session.me.roles.includes('patient')) body.address = { street: v.street, postal_code: v.postal_code, city: v.city };
    const r = await req.api.raw('user', 'PATCH', '/v1/me', { body });
    if (r.status === 200) return res.redirect('/account?m=profile_saved');
    if (r.status === 422) {
      res.locals.me = { ...req.session.me, ...v, address: { street: v.street, postal_code: v.postal_code, city: v.city } };
      return render(req, res, 422, { error: problemMessage(r.body, 'Bitte prüfen Sie Ihre Eingaben.'), errors: fieldErrors(r.body), section: 'profile' });
    }
    throw new HttpError(r.status, r.body);
  });

  app.post('/account/email', guard, async (req, res) => {
    const body = { new_email: text(req.body, 'new_email', 254), current_password: secret(req.body, 'current_password') };
    const r = await req.api.raw('user', 'POST', '/v1/me/email', { body });
    if (r.status === 202) return res.redirect('/account?m=email_requested');
    if ([422, 429].includes(r.status)) {
      return render(req, res, r.status, { error: problemMessage(r.body), errors: fieldErrors(r.body), section: 'email' });
    }
    throw new HttpError(r.status, r.body);
  });

  app.post('/account/password', guard, async (req, res) => {
    const next = secret(req.body, 'new_password');
    if (next !== secret(req.body, 'password2')) {
      return render(req, res, 422, { errors: { password2: ['Die beiden Passwörter stimmen nicht überein.'] }, section: 'password' });
    }
    const r = await req.api.raw('user', 'POST', '/v1/me/password', { body: { current_password: secret(req.body, 'current_password'), new_password: next } });
    if (r.status === 200) {
      setSession(config, res, r.body.access_token); // all older tokens are dead, this one is the new session
      return res.redirect('/account?m=password_saved');
    }
    if (r.status === 422 || r.status === 429) {
      return render(req, res, r.status, { error: problemMessage(r.body), errors: fieldErrors(r.body), section: 'password' });
    }
    throw new HttpError(r.status, r.body);
  });

  app.post('/account/mfa/recovery-codes', guard, async (req, res) => {
    const r = await req.api.raw('user', 'POST', '/v1/me/mfa/recovery-codes', { body: { code: text(req.body, 'code', 20) } });
    if (r.status === 200) return res.render('recovery_codes.njk', { title: 'Neue Recovery-Codes', codes: r.body.recovery_codes, next: '/account' });
    if (r.status === 422 || r.status === 429 || r.status === 409) {
      return render(req, res, r.status, { error: problemMessage(r.body), section: 'mfa' });
    }
    throw new HttpError(r.status, r.body);
  });

  app.post('/account/mfa/disable', guard, async (req, res) => {
    const body = { password: secret(req.body, 'password') };
    if (text(req.body, 'mode', 10) === 'recovery') body.recovery_code = text(req.body, 'recovery_code', 32);
    else body.code = text(req.body, 'code', 20);
    const r = await req.api.raw('user', 'POST', '/v1/me/mfa/disable', { body });
    if (r.status === 204) {
      clearSession(config, res); // the backend ended all sessions
      rotateCsrf(config, res);
      return res.redirect('/login?m=mfa_removed');
    }
    if ([409, 422, 429].includes(r.status)) return render(req, res, r.status, { error: problemMessage(r.body), section: 'mfa-disable' });
    throw new HttpError(r.status, r.body);
  });

  app.post('/account/delete', guard, async (req, res) => {
    if (text(req.body, 'confirm', 5) !== 'yes') {
      return render(req, res, 422, { error: 'Bitte bestätigen Sie, dass Sie das Konto endgültig löschen möchten.', section: 'delete' });
    }
    const r = await req.api.raw('user', 'POST', '/v1/me/deletion', { body: { password: secret(req.body, 'password'), code: text(req.body, 'code', 20) } });
    if (r.status === 204) {
      clearSession(config, res);
      rotateCsrf(config, res);
      return res.redirect('/login?m=deleted');
    }
    if ([422, 429].includes(r.status)) return render(req, res, r.status, { error: problemMessage(r.body), section: 'delete' });
    throw new HttpError(r.status, r.body);
  });
}
