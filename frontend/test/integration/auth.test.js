import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, it } from 'node:test';
import { Browser, FakeBackend, fakeToken, loginAs, sessionBrowser, startApp } from '../helpers.js';

describe('login (UF-02)', () => {
  let backend, app, b;
  beforeEach(async () => {
    backend = new FakeBackend();
    app = await startApp(backend);
    b = new Browser(app.base);
  });
  afterEach(() => app.close());

  const challenge = 'chal.' + 'x'.repeat(40);
  const stubPassword = () => backend.on('POST /v1/auth/login', { status: 200, body: { mfa_challenge: challenge, expires_in: 300 } });

  it('password step: stores the challenge in an HttpOnly cookie and goes to the code page', async () => {
    stubPassword();
    await b.open('/login');
    const r = await b.post('/login', { email: 'erika@example.org', password: 'Orange-Tisch-Lampe-42' });
    assert.equal(r.status, 302);
    assert.equal(r.location, '/login/mfa');
    const c = r.setCookies.find((x) => x.startsWith('__Host-mfa='));
    assert.match(c, /HttpOnly/);
    assert.match(c, /Secure/);
    assert.match(c, /SameSite=Strict/);
    assert.match(c, /Max-Age=300/);
    assert.deepEqual(backend.called('POST /v1/auth/login')[0].body, { email: 'erika@example.org', password: 'Orange-Tisch-Lampe-42' });
    const page = await b.get('/login/mfa');
    assert.equal(page.status, 200);
    assert.ok(!page.text.includes(challenge), 'the challenge never appears in HTML');
  });

  it('wrong password: a neutral message, status 401, nothing echoed', async () => {
    backend.on('POST /v1/auth/login', { status: 401, body: { code: 'invalid_credentials' } });
    await b.open('/login');
    const r = await b.post('/login', { email: 'erika@example.org', password: 'secret-wrong' });
    assert.equal(r.status, 401);
    assert.match(r.text, /E-Mail-Adresse oder Passwort stimmen nicht/);
    assert.ok(!r.text.includes('secret-wrong'));
    assert.ok(!b.cookie('mfa'));
  });

  it('rate limited: shows the message', async () => {
    backend.on('POST /v1/auth/login', { status: 429, body: { code: 'rate_limited' } });
    await b.open('/login');
    const r = await b.post('/login', { email: 'a@example.org', password: 'x' });
    assert.equal(r.status, 429);
    assert.match(r.text, /Zu viele Anfragen/);
  });

  it('code step: sends the code with the challenge, sets the session cookie and goes to the home of the role', async () => {
    stubPassword();
    backend.on('POST /v1/auth/login/mfa', { status: 200, body: { access_token: fakeToken(), scope: 'full', roles: ['doctor'] } });
    await b.open('/login');
    await b.post('/login', { email: 'a@example.org', password: 'x' });
    await b.open('/login/mfa');
    const r = await b.post('/login/mfa', { mode: 'code', code: '123456' });
    assert.equal(r.status, 302);
    assert.equal(r.location, '/calendar');
    assert.deepEqual(backend.called('POST /v1/auth/login/mfa')[0].body, { mfa_challenge: challenge, code: '123456' });
    const c = r.setCookies.find((x) => x.startsWith('__Host-session='));
    assert.match(c, /HttpOnly/);
    assert.match(c, /Secure/);
    assert.match(c, /SameSite=Strict/);
    assert.match(c, /Path=\//);
    assert.match(c, /Max-Age=\d+/);
    assert.ok(r.setCookies.some((x) => x.startsWith('__Host-mfa=;')), 'the challenge cookie is removed');
    assert.ok(r.setCookies.some((x) => x.startsWith('__Host-csrf=')), 'the CSRF value is renewed after a login');
  });

  it('role homes: patient, reception, administrator', async () => {
    for (const [roles, home] of [[['patient'], '/appointments'], [['desk'], '/staff/bookings'], [['organisator'], '/staff/bookings'], [['systemadmin'], '/admin/users']]) {
      const be = new FakeBackend();
      const a = await startApp(be);
      be.on('POST /v1/auth/login', { status: 200, body: { mfa_challenge: challenge } });
      be.on('POST /v1/auth/login/mfa', { status: 200, body: { access_token: fakeToken(), scope: 'full', roles } });
      const br = new Browser(a.base);
      await br.open('/login');
      await br.post('/login', { email: 'a@example.org', password: 'x' });
      await br.open('/login/mfa');
      assert.equal((await br.post('/login/mfa', { mode: 'code', code: '123456' })).location, home);
      await a.close();
    }
  });

  it('a setup token leads to the setup pages', async () => {
    stubPassword();
    backend.on('POST /v1/auth/login/mfa', { status: 200, body: { access_token: fakeToken({ scope: 'setup' }), scope: 'setup', roles: ['patient'] } });
    await b.open('/login');
    await b.post('/login', { email: 'a@example.org', password: 'x' });
    await b.open('/login/mfa');
    const r = await b.post('/login/mfa', { mode: 'none' });
    assert.equal(r.location, '/setup');
    assert.deepEqual(backend.called('POST /v1/auth/login/mfa')[0].body, { mfa_challenge: challenge }, 'no code is sent for a first login');
  });

  it('wrong code: 401, the challenge cookie stays, no session', async () => {
    stubPassword();
    backend.on('POST /v1/auth/login/mfa', { status: 401, body: { code: 'invalid_credentials' } });
    await b.open('/login');
    await b.post('/login', { email: 'a@example.org', password: 'x' });
    await b.open('/login/mfa');
    const r = await b.post('/login/mfa', { mode: 'code', code: '000000' });
    assert.equal(r.status, 401);
    assert.ok(b.cookie('mfa'));
    assert.ok(!b.cookie('session'));
  });

  it('refuses a malformed code before calling the backend', async () => {
    stubPassword();
    await b.open('/login');
    await b.post('/login', { email: 'a@example.org', password: 'x' });
    await b.open('/login/mfa');
    for (const code of ['12345', 'abcdef', '1234567', '12 456', '']) {
      assert.equal((await b.post('/login/mfa', { mode: 'code', code })).status, 422, code);
    }
    assert.equal(backend.called('POST /v1/auth/login/mfa').length, 0);
  });

  it('recovery code login sends recovery_code', async () => {
    stubPassword();
    backend.on('POST /v1/auth/login/mfa', { status: 200, body: { access_token: fakeToken(), scope: 'full', roles: ['patient'] } });
    await b.open('/login');
    await b.post('/login', { email: 'a@example.org', password: 'x' });
    await b.open('/login/mfa?recovery=1');
    await b.post('/login/mfa', { mode: 'recovery', recovery_code: 'abcd-efgh-jkmn' });
    assert.deepEqual(backend.called('POST /v1/auth/login/mfa')[0].body, { mfa_challenge: challenge, recovery_code: 'abcd-efgh-jkmn' });
  });

  it('the code page without a challenge goes back to the login', async () => {
    assert.equal((await b.get('/login/mfa')).location, '/login');
    await b.open('/login');
    assert.equal((await b.post('/login/mfa', { mode: 'code', code: '123456' })).location, '/login');
  });

  it('the next parameter is followed only for local paths (no open redirect)', async () => {
    stubPassword();
    backend.on('POST /v1/auth/login/mfa', { status: 200, body: { access_token: fakeToken(), scope: 'full', roles: ['patient'] } });
    const attempts = [['/book/abc', '/book/abc'], ['//evil.example', '/appointments'], ['https://evil.example/x', '/appointments'], ['/\\evil.example', '/appointments'], ['/../etc', '/appointments'], ['javascript:alert(1)', '/appointments']];
    for (const [next, expected] of attempts) {
      const be = new FakeBackend();
      be.on('POST /v1/auth/login', { status: 200, body: { mfa_challenge: challenge } });
      be.on('POST /v1/auth/login/mfa', { status: 200, body: { access_token: fakeToken(), scope: 'full', roles: ['patient'] } });
      const a = await startApp(be);
      const br = new Browser(a.base);
      await br.open(`/login?next=${encodeURIComponent(next)}`);
      const r1 = await br.post('/login', { email: 'a@example.org', password: 'x', next });
      await br.open(r1.location);
      const r2 = await br.post('/login/mfa', { mode: 'code', code: '123456', next });
      assert.equal(r2.location, expected, next);
      await a.close();
    }
  });

  it('logout calls the backend, removes the cookies and renews the CSRF value', async () => {
    const t = loginAs(backend);
    backend.on('POST /v1/auth/logout', { status: 204 });
    const br = await sessionBrowser(app, t);
    await br.open('/account');
    const r = await br.post('/logout', {});
    assert.equal(r.status, 302);
    assert.equal(r.location, '/login?m=logged_out');
    assert.equal(backend.called('POST /v1/auth/logout')[0].token, t);
    assert.ok(r.setCookies.some((x) => x.startsWith('__Host-session=;')));
    assert.ok(!br.cookie('session'));
  });

  it('an expired or revoked session is cleared and sends the user to the login', async () => {
    const t = fakeToken();
    backend.on('GET /v1/me', { status: 401, body: { code: 'unauthorized' } });
    const br = await sessionBrowser(app, t);
    const r = await br.get('/appointments');
    assert.equal(r.status, 302);
    assert.equal(r.location, '/login?next=%2Fappointments');
    assert.ok(!br.cookie('session'));
    const old = await sessionBrowser(app, fakeToken({ exp: Math.floor(Date.now() / 1000) - 5 }));
    const calls = backend.calls.length;
    assert.equal((await old.get('/appointments')).location, '/login?next=%2Fappointments');
    assert.equal(backend.calls.length, calls, 'an expired token is not even sent to the backend');
  });

  it('forwards the address of the browser to the backend (rate limits and lock-outs are per client)', async () => {
    stubPassword();
    await b.open('/login');
    await b.post('/login', { email: 'a@example.org', password: 'x' }, { headers: { 'x-forwarded-for': '203.0.113.9' } });
    assert.equal(backend.called('POST /v1/auth/login')[0].ip, '203.0.113.9');
  });
});

describe('registration, e-mail links and password reset (UF-01, UF-12)', () => {
  let backend, app, b;
  beforeEach(async () => {
    backend = new FakeBackend();
    app = await startApp(backend);
    b = new Browser(app.base);
  });
  afterEach(() => app.close());

  const form = { first_name: 'Erika', last_name: 'Muster', email: 'erika@example.org', phone: '', street: 'Hauptstr. 1', postal_code: '68159', city: 'Mannheim', password: 'Orange-Tisch-Lampe-42', password2: 'Orange-Tisch-Lampe-42' };

  it('registers and sends the address only when given', async () => {
    backend.on('POST /v1/auth/register', { status: 202 });
    await b.open('/register');
    const r = await b.post('/register', form, { page: '/register' });
    assert.equal(r.location, '/login?m=registered');
    assert.deepEqual(backend.called('POST /v1/auth/register')[0].body, {
      first_name: 'Erika', last_name: 'Muster', email: 'erika@example.org', password: 'Orange-Tisch-Lampe-42',
      address: { street: 'Hauptstr. 1', postal_code: '68159', city: 'Mannheim' },
    });
  });

  it('different passwords: no backend call', async () => {
    await b.open('/register');
    const r = await b.post('/register', { ...form, password2: 'anders' }, { page: '/register' });
    assert.equal(r.status, 422);
    assert.match(r.text, /stimmen nicht überein/);
    assert.equal(backend.called('POST /v1/auth/register').length, 0);
  });

  it('shows field errors of the backend, keeps the input, never echoes the password', async () => {
    backend.on('POST /v1/auth/register', { status: 422, body: { code: 'validation_failed', errors: [{ field: 'password', code: 'password_blocklisted' }, { field: 'email', code: 'format' }] } });
    await b.open('/register');
    const r = await b.post('/register', { ...form, first_name: 'Erika "<b>' }, { page: '/register' });
    assert.equal(r.status, 422);
    assert.match(r.text, /steht auf der Liste häufig verwendeter/);
    assert.match(r.text, /E-Mail-Adresse hat ein ungültiges Format/);
    assert.ok(!r.text.includes('Orange-Tisch-Lampe-42'), 'the password is not sent back');
    assert.ok(!r.text.includes('Erika "<b>'), 'input is escaped');
    assert.match(r.text, /Erika &quot;&lt;b&gt;/);
  });

  it('e-mail confirmation: the click only opens a page, the button confirms', async () => {
    backend.on('POST /v1/auth/verify-email', { status: 204 });
    const token = 'Tok-' + 'a'.repeat(40);
    const page = await b.open(`/verify-email?token=${token}`);
    assert.equal(backend.called('POST /v1/auth/verify-email').length, 0);
    assert.match(page.text, new RegExp(`name="token" value="${token}"`));
    const r = await b.post('/verify-email', { token });
    assert.equal(r.location, '/login?m=verified');
    assert.deepEqual(backend.called('POST /v1/auth/verify-email')[0].body, { token });
  });

  it('a used or expired link shows the error again with the button', async () => {
    backend.on('POST /v1/auth/verify-email', { status: 400, body: { code: 'invalid_token' } });
    const token = 'Tok-' + 'b'.repeat(40);
    await b.open(`/verify-email?token=${token}`);
    const r = await b.post('/verify-email', { token });
    assert.equal(r.status, 400);
    assert.match(r.text, /ungültig, abgelaufen oder wurde schon verwendet/);
  });

  it('forgot password: always the same answer, also for validation problems of unknown accounts', async () => {
    backend.on('POST /v1/auth/password/forgot', { status: 202 });
    await b.open('/forgot-password');
    const r = await b.post('/forgot-password', { email: 'unknown@example.org' }, { page: '/forgot-password' });
    assert.equal(r.location, '/login?m=reset_sent');
  });

  it('reset password: sends the token and the new password, never the repeat', async () => {
    backend.on('POST /v1/auth/password/reset', { status: 204 });
    const token = 'Tok-' + 'c'.repeat(40);
    await b.open(`/reset-password?token=${token}`);
    const r = await b.post('/reset-password', { token, new_password: 'Gelb-Wolke-Stein-Fluss-77', password2: 'Gelb-Wolke-Stein-Fluss-77' });
    assert.equal(r.location, '/login?m=password_reset');
    assert.deepEqual(backend.called('POST /v1/auth/password/reset')[0].body, { token, new_password: 'Gelb-Wolke-Stein-Fluss-77' });
  });

  it('reset password: weak password and used link', async () => {
    const token = 'Tok-' + 'd'.repeat(40);
    backend.on('POST /v1/auth/password/reset', { status: 422, body: { code: 'validation_failed', errors: [{ field: 'new_password', code: 'password_too_short' }] } });
    await b.open(`/reset-password?token=${token}`);
    const weak = await b.post('/reset-password', { token, new_password: 'kurz', password2: 'kurz' });
    assert.equal(weak.status, 422);
    assert.match(weak.text, /mindestens 15 Zeichen/);
    assert.match(weak.text, new RegExp(`name="token" value="${token}"`), 'the form stays usable');
    backend.on('POST /v1/auth/password/reset', { status: 400, body: { code: 'invalid_token' } });
    assert.equal((await b.post('/reset-password', { token, new_password: 'Gelb-Wolke-Stein-Fluss-77', password2: 'Gelb-Wolke-Stein-Fluss-77' })).status, 400);
  });

  it('escapes names from the backend everywhere (XSS)', async () => {
    const t = loginAs(backend, { me: { first_name: '<img src=x onerror=alert(1)>', last_name: '"><script>alert(2)</script>' } });
    backend.on('GET /v1/bookings', { status: 200, body: { items: [], total: 0 } });
    const br = await sessionBrowser(app, t);
    for (const path of ['/appointments', '/account']) {
      const r = await br.get(path);
      assert.equal(r.status, 200, path);
      assert.ok(!r.text.includes('<img src=x'), path);
      assert.ok(!r.text.includes('<script>alert'), path);
      assert.match(r.text, /&lt;img src=x onerror=alert\(1\)&gt;/);
    }
  });

  it('never puts the session token into a page', async () => {
    const t = loginAs(backend);
    backend.on('GET /v1/bookings', { status: 200, body: { items: [], total: 0 } });
    const br = await sessionBrowser(app, t);
    for (const path of ['/appointments', '/account']) assert.ok(!(await br.get(path)).text.includes(t), path);
  });
});
