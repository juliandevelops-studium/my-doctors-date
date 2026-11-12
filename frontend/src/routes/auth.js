// Registration, login with MFA, logout, e-mail confirmation and password reset (UF-01, UF-02, UF-12).
import { cookieNames, cookieOptions, parseCookies } from '../cookies.js';
import { rotateCsrf } from '../csrf.js';
import { fieldErrors, HttpError, problemMessage } from '../errors.js';
import { secret, TOKEN, text } from '../form.js';
import { clearSession, homeFor, safeNext, setSession } from '../middleware.js';

const MFA_COOKIE_SECONDS = 300;

export default function authRoutes(app, { config }) {
  const names = cookieNames(config.cookieSecure);

  app.get('/', (req, res) => {
    if (!req.session) return res.redirect('/login');
    res.redirect(req.session.scope === 'full' ? homeFor(req.session.me.roles) : '/setup');
  });

  // ---------------------------------------------------------------- login (password step)
  app.get('/login', (req, res) => {
    if (req.session?.scope === 'full') return res.redirect(homeFor(req.session.me.roles));
    res.render('login.njk', { title: 'Anmelden', next: safeNext(req.query.next, ''), email: '' });
  });

  app.post('/login', async (req, res) => {
    const email = text(req.body, 'email', 254);
    const password = secret(req.body, 'password');
    const next = safeNext(req.body?.next, '');
    const page = (status, error, errors) => res.status(status).render('login.njk', { title: 'Anmelden', next, email, error, errors });
    if (!email || !password) return page(422, 'Bitte geben Sie E-Mail-Adresse und Passwort ein.');
    const r = await req.api.raw('user', 'POST', '/v1/auth/login', { body: { email, password } });
    if (r.status === 200) {
      res.cookie(names.mfa, r.body.mfa_challenge, cookieOptions(config.cookieSecure, MFA_COOKIE_SECONDS));
      return res.redirect(next ? `/login/mfa?next=${encodeURIComponent(next)}` : '/login/mfa');
    }
    if (r.status === 401) return page(401, problemMessage(r.body));
    if (r.status === 422) return page(422, problemMessage(r.body), fieldErrors(r.body));
    if (r.status === 429) return page(429, problemMessage(r.body));
    throw new HttpError(r.status, r.body);
  });

  // ---------------------------------------------------------------- login (second step)
  app.get('/login/mfa', (req, res) => {
    if (!parseCookies(req.headers.cookie)[names.mfa]) return res.redirect('/login');
    res.render('mfa.njk', { title: 'Zwei-Faktor-Anmeldung', recovery: req.query.recovery === '1', next: safeNext(req.query.next, '') });
  });

  app.post('/login/mfa', async (req, res) => {
    const challenge = parseCookies(req.headers.cookie)[names.mfa];
    if (!challenge) return res.redirect('/login');
    const mode = text(req.body, 'mode', 10);
    const next = safeNext(req.body?.next ?? req.query.next, '');
    const body = { mfa_challenge: challenge };
    if (mode === 'code') body.code = text(req.body, 'code', 20);
    else if (mode === 'recovery') body.recovery_code = text(req.body, 'recovery_code', 32);
    else if (mode !== 'none') return res.redirect('/login/mfa');
    const page = (status, error, errors) =>
      res.status(status).render('mfa.njk', { title: 'Zwei-Faktor-Anmeldung', recovery: mode === 'recovery', next, error, errors });
    if ((mode === 'code' && !/^\d{6}$/.test(body.code)) || (mode === 'recovery' && !body.recovery_code)) {
      return page(422, 'Bitte geben Sie den Code ein.');
    }
    const r = await req.api.raw('user', 'POST', '/v1/auth/login/mfa', { body });
    if (r.status === 200) {
      res.clearCookie(names.mfa, cookieOptions(config.cookieSecure));
      setSession(config, res, r.body.access_token);
      rotateCsrf(config, res);
      if (r.body.scope !== 'full') return res.redirect('/setup');
      return res.redirect(next || homeFor(r.body.roles ?? []));
    }
    if (r.status === 401) {
      // The challenge stays valid; the account lock (SR.05) limits the guesses.
      return page(401, 'Der Code ist nicht richtig, oder die Anmeldung ist abgelaufen. Bei abgelaufener Anmeldung beginnen Sie bitte neu.');
    }
    if (r.status === 422) return page(422, problemMessage(r.body), fieldErrors(r.body));
    if (r.status === 429) return page(429, problemMessage(r.body));
    throw new HttpError(r.status, r.body);
  });

  // ---------------------------------------------------------------- logout
  app.post('/logout', async (req, res) => {
    if (req.session) await req.api.raw('user', 'POST', '/v1/auth/logout').catch(() => {});
    clearSession(config, res);
    rotateCsrf(config, res);
    res.redirect('/login?m=logged_out');
  });

  // ---------------------------------------------------------------- registration (UF-01)
  const registerValues = (b) => ({
    first_name: text(b, 'first_name', 100), last_name: text(b, 'last_name', 100), email: text(b, 'email', 254),
    phone: text(b, 'phone', 30), street: text(b, 'street', 200), postal_code: text(b, 'postal_code', 20), city: text(b, 'city', 100),
  });

  app.get('/register', (req, res) => {
    if (req.session) return res.redirect('/');
    res.render('register.njk', { title: 'Konto anlegen', v: {} });
  });

  app.post('/register', async (req, res) => {
    const v = registerValues(req.body);
    const password = secret(req.body, 'password');
    const page = (status, error, errors) => res.status(status).render('register.njk', { title: 'Konto anlegen', v, error, errors });
    if (password !== secret(req.body, 'password2')) return page(422, null, { password2: ['Die beiden Passwörter stimmen nicht überein.'] });
    const payload = { first_name: v.first_name, last_name: v.last_name, email: v.email, password };
    if (v.phone) payload.phone = v.phone;
    if (v.street || v.postal_code || v.city) payload.address = { street: v.street || null, postal_code: v.postal_code || null, city: v.city || null };
    const r = await req.api.raw('user', 'POST', '/v1/auth/register', { body: payload });
    if (r.status === 202) return res.redirect('/login?m=registered');
    if (r.status === 422) return page(422, 'Bitte prüfen Sie Ihre Eingaben.', fieldErrors(r.body));
    if (r.status === 429) return page(429, problemMessage(r.body));
    throw new HttpError(r.status, r.body);
  });

  // ---------------------------------------------------------------- links from e-mails (a click must not change anything,
  // so every link opens a page with a button; the button posts)
  const tokenPage = (path, post, title, intro, button) => {
    app.get(path, (req, res) => {
      const token = String(req.query.token ?? '');
      if (!TOKEN.test(token)) {
        return res.status(400).render('error.njk', { title: 'Link ungültig', message: 'Der Link ist ungültig oder unvollständig.' });
      }
      res.render('confirm_token.njk', { title, intro, button, action: path, token });
    });
    app.post(path, async (req, res) => {
      const token = text(req.body, 'token', 256);
      if (!TOKEN.test(token)) return res.status(400).render('error.njk', { title: 'Link ungültig', message: 'Der Link ist ungültig oder unvollständig.' });
      const r = await req.api.raw('user', 'POST', post.api, { body: { token } });
      if (r.status === 204) return res.redirect(post.done);
      const message = r.status === 409 ? problemMessage(r.body) : problemMessage(r.body, 'Der Link ist ungültig, abgelaufen oder wurde schon verwendet.');
      if (r.status === 400 || r.status === 409 || r.status === 422) {
        return res.status(r.status).render('confirm_token.njk', { title, intro, button, action: path, token, error: message });
      }
      throw new HttpError(r.status, r.body);
    });
  };
  tokenPage('/verify-email', { api: '/v1/auth/verify-email', done: '/login?m=verified' }, 'E-Mail-Adresse bestätigen',
    'Bestätigen Sie, dass diese E-Mail-Adresse zu Ihrem Konto gehört.', 'Adresse bestätigen');
  tokenPage('/confirm-email-change', { api: '/v1/auth/email-change/confirm', done: '/login?m=email_changed' }, 'Neue E-Mail-Adresse bestätigen',
    'Bestätigen Sie, dass diese neue E-Mail-Adresse zu Ihrem Konto gehören soll.', 'Änderung bestätigen');

  // ---------------------------------------------------------------- password reset (UF-12)
  app.get('/forgot-password', (req, res) => res.render('forgot.njk', { title: 'Passwort vergessen' }));

  app.post('/forgot-password', async (req, res) => {
    const email = text(req.body, 'email', 254);
    if (!email) return res.status(422).render('forgot.njk', { title: 'Passwort vergessen', error: 'Bitte geben Sie Ihre E-Mail-Adresse ein.' });
    const r = await req.api.raw('user', 'POST', '/v1/auth/password/forgot', { body: { email } });
    if (r.status === 202) return res.redirect('/login?m=reset_sent'); // always the same answer
    if (r.status === 422) return res.status(422).render('forgot.njk', { title: 'Passwort vergessen', error: 'Bitte geben Sie eine gültige E-Mail-Adresse ein.' });
    if (r.status === 429) return res.status(429).render('forgot.njk', { title: 'Passwort vergessen', error: problemMessage(r.body) });
    throw new HttpError(r.status, r.body);
  });

  app.get('/reset-password', (req, res) => {
    const token = String(req.query.token ?? '');
    if (!TOKEN.test(token)) return res.status(400).render('error.njk', { title: 'Link ungültig', message: 'Der Link ist ungültig oder unvollständig.' });
    res.render('reset.njk', { title: 'Neues Passwort', token });
  });

  app.post('/reset-password', async (req, res) => {
    const token = text(req.body, 'token', 256);
    const password = secret(req.body, 'new_password');
    if (!TOKEN.test(token)) return res.status(400).render('error.njk', { title: 'Link ungültig', message: 'Der Link ist ungültig oder unvollständig.' });
    const page = (status, error, errors) => res.status(status).render('reset.njk', { title: 'Neues Passwort', token, error, errors });
    if (password !== secret(req.body, 'password2')) return page(422, null, { password2: ['Die beiden Passwörter stimmen nicht überein.'] });
    const r = await req.api.raw('user', 'POST', '/v1/auth/password/reset', { body: { token, new_password: password } });
    if (r.status === 204) return res.redirect('/login?m=password_reset');
    if (r.status === 400) return page(400, problemMessage(r.body));
    if (r.status === 422) return page(422, 'Das Passwort erfüllt die Anforderungen nicht.', fieldErrors(r.body));
    if (r.status === 429) return page(429, problemMessage(r.body));
    throw new HttpError(r.status, r.body);
  });
}
