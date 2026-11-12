// First login of a new account, of an account created by an administrator, and of the bootstrap administrator (UF-03):
// choose a new password and set up the second factor before anything else is reachable.
import QRCode from 'qrcode';
import { fieldErrors, HttpError, problemMessage } from '../errors.js';
import { secret, text } from '../form.js';
import { homeFor, requireLogin, setSession } from '../middleware.js';

const SECRET = /^[A-Z2-7]{32}$/;

export default function setupRoutes(app, { config }) {
  const guard = (req, res, next) => (req.session.scope === 'full' ? res.redirect(homeFor(req.session.me.roles)) : next());

  app.get('/setup', requireLogin, guard, (req, res) => {
    const me = req.session.me;
    if (me.must_change_password) return res.render('setup.njk', { title: 'Einrichtung', step: 'password' });
    if (!me.mfa_enabled) return res.render('setup.njk', { title: 'Einrichtung', step: 'mfa' });
    // Both steps are done but the token is an old one: a new login gives a full token.
    res.render('setup.njk', { title: 'Einrichtung', step: 'relogin' });
  });

  app.post('/setup/password', requireLogin, guard, async (req, res) => {
    const current = secret(req.body, 'current_password');
    const next = secret(req.body, 'new_password');
    const page = (status, error, errors) => res.status(status).render('setup.njk', { title: 'Einrichtung', step: 'password', error, errors });
    if (next !== secret(req.body, 'password2')) return page(422, null, { password2: ['Die beiden Passwörter stimmen nicht überein.'] });
    const r = await req.api.raw('user', 'POST', '/v1/me/password', { body: { current_password: current, new_password: next } });
    if (r.status === 200) {
      setSession(config, res, r.body.access_token);
      return res.redirect('/setup');
    }
    if (r.status === 422) return page(422, problemMessage(r.body), fieldErrors(r.body));
    if (r.status === 429) return page(429, problemMessage(r.body));
    throw new HttpError(r.status, r.body);
  });

  const mfaPage = async (req, res, status, secretValue, extra = {}) => {
    const uri = `otpauth://totp/Praxis:${encodeURIComponent(req.session.me.email)}?secret=${secretValue}&issuer=Praxis&algorithm=SHA1&digits=6&period=30`;
    const qr = await QRCode.toString(uri, { type: 'svg', margin: 1, errorCorrectionLevel: 'M' });
    res.status(status).render('mfa_setup.njk', { title: 'Zwei-Faktor-Anmeldung einrichten', secret: secretValue, qr, action: '/setup/mfa/confirm', ...extra });
  };

  app.post('/setup/mfa/start', requireLogin, guard, async (req, res) => {
    const r = await req.api.raw('user', 'POST', '/v1/me/mfa/setup');
    if (r.status === 200) return mfaPage(req, res, 200, r.body.secret);
    if (r.status === 409) return res.redirect('/setup');
    throw new HttpError(r.status, r.body);
  });

  app.post('/setup/mfa/confirm', requireLogin, guard, async (req, res) => {
    const code = text(req.body, 'code', 20);
    const secretValue = text(req.body, 'secret', 64);
    if (!SECRET.test(secretValue)) return res.redirect('/setup');
    if (!/^\d{6}$/.test(code)) return mfaPage(req, res, 422, secretValue, { error: 'Bitte geben Sie den sechsstelligen Code ein.' });
    const r = await req.api.raw('user', 'POST', '/v1/me/mfa/confirm', { body: { code } });
    if (r.status === 200) {
      setSession(config, res, r.body.token.access_token);
      return res.render('recovery_codes.njk', { title: 'Recovery-Codes', codes: r.body.recovery_codes, next: r.body.token.scope === 'full' ? homeFor(r.body.token.roles ?? []) : '/setup' });
    }
    if (r.status === 422) return mfaPage(req, res, 422, secretValue, { error: problemMessage(r.body) });
    if (r.status === 429) return mfaPage(req, res, 429, secretValue, { error: problemMessage(r.body) });
    if (r.status === 409) return res.redirect('/setup');
    throw new HttpError(r.status, r.body);
  });
}
