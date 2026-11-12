import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, it } from 'node:test';
import { FakeBackend, fakeToken, loginAs, sessionBrowser, startApp, TOTP_SECRET } from '../helpers.js';

describe('first login: password and second factor (UF-03, UF-04)', () => {
  let backend, app, token, b;
  beforeEach(async () => {
    backend = new FakeBackend();
    app = await startApp(backend);
    token = loginAs(backend, { scope: 'setup', me: { must_change_password: true, mfa_enabled: false } });
    b = await sessionBrowser(app, token);
  });
  afterEach(() => app.close());

  it('a user with a setup token only reaches the setup pages', async () => {
    for (const path of ['/book', '/appointments', '/account', '/staff/bookings', '/admin/users', '/calendar']) {
      const r = await b.get(path);
      assert.equal(r.status, 302, path);
      assert.equal(r.location, '/setup', path);
    }
    const page = await b.get('/setup');
    assert.match(page.text, /Passwort fest/);
    assert.doesNotMatch(page.text, /href="\/book"/, 'no navigation to the rest of the application');
    assert.match(page.text, /aria-current="page">Einrichtung/);
  });

  it('step 1: a new password, then the new token replaces the session', async () => {
    const newToken = fakeToken({ scope: 'setup', sub: 'new' });
    backend.on('POST /v1/me/password', { status: 200, body: { access_token: newToken, scope: 'setup' } });
    await b.open('/setup');
    const mismatch = await b.post('/setup/password', { current_password: 'old', new_password: 'Gelb-Wolke-Stein-Fluss-77', password2: 'x' });
    assert.equal(mismatch.status, 422);
    assert.equal(backend.called('POST /v1/me/password').length, 0);
    const r = await b.post('/setup/password', { current_password: 'old-password', new_password: 'Gelb-Wolke-Stein-Fluss-77', password2: 'Gelb-Wolke-Stein-Fluss-77' });
    assert.equal(r.location, '/setup');
    assert.deepEqual(backend.called('POST /v1/me/password')[0].body, { current_password: 'old-password', new_password: 'Gelb-Wolke-Stein-Fluss-77' });
    assert.equal(b.cookie('session'), newToken);
  });

  it('step 1: shows the reason when the backend refuses', async () => {
    backend.on('POST /v1/me/password', { status: 422, body: { code: 'invalid_current_password' } });
    await b.open('/setup');
    const r = await b.post('/setup/password', { current_password: 'x', new_password: 'Gelb-Wolke-Stein-Fluss-77', password2: 'Gelb-Wolke-Stein-Fluss-77' });
    assert.equal(r.status, 422);
    assert.match(r.text, /aktuelle Passwort ist nicht richtig/);
    assert.ok(!r.text.includes('Gelb-Wolke'), 'passwords are never sent back');
  });

  describe('step 2: second factor', () => {
    beforeEach(() => {
      backend.on('GET /v1/me', { status: 200, body: { id: 'u1', first_name: 'Erika', last_name: 'Muster', email: 'erika@example.org', roles: ['patient'], mfa_enabled: false, must_change_password: false } });
    });

    it('shows the QR code and the key once, and keeps them when the code is wrong', async () => {
      backend.on('POST /v1/me/mfa/setup', { status: 200, body: { secret: TOTP_SECRET.slice(0, 32), otpauth_uri: 'otpauth://x' } });
      backend.on('POST /v1/me/mfa/confirm', { status: 422, body: { code: 'invalid_code' } });
      await b.open('/setup');
      const start = await b.post('/setup/mfa/start', {});
      assert.equal(start.status, 200);
      assert.match(start.text, /<svg[^>]*viewBox/, 'QR code is inline SVG (no image request, no script)');
      assert.match(start.text, new RegExp(TOTP_SECRET.slice(0, 32)));
      b.token = start.text.match(/name="_csrf" value="([^"]+)"/)[1];
      const wrong = await b.post('/setup/mfa/confirm', { code: '000000', secret: TOTP_SECRET.slice(0, 32) });
      assert.equal(wrong.status, 422);
      assert.match(wrong.text, /Der Code ist nicht richtig/);
      assert.match(wrong.text, new RegExp(TOTP_SECRET.slice(0, 32)), 'the same key is shown again, nothing needs rescanning');
      const bad = await b.post('/setup/mfa/confirm', { code: 'abc', secret: TOTP_SECRET.slice(0, 32) });
      assert.equal(bad.status, 422);
      assert.equal(backend.called('POST /v1/me/mfa/confirm').length, 1, 'a malformed code is not sent');
      const tampered = await b.post('/setup/mfa/confirm', { code: '123456', secret: 'not-a-key' });
      assert.equal(tampered.location, '/setup');
    });

    it('confirming shows the recovery codes exactly once and switches to the full session', async () => {
      const full = fakeToken({ scope: 'full', sub: 'full' });
      const codes = Array.from({ length: 10 }, (_, i) => `abcd-efgh-jk${String(i).padStart(2, 'm')}`);
      backend.on('POST /v1/me/mfa/confirm', { status: 200, body: { recovery_codes: codes, token: { access_token: full, scope: 'full', roles: ['patient'] } } });
      await b.open('/setup');
      b.token = (await b.post('/setup/mfa/start', {}).catch(() => ({ text: '' }))).text?.match?.(/name="_csrf" value="([^"]+)"/)?.[1] ?? b.token;
      const r = await b.post('/setup/mfa/confirm', { code: '123456', secret: TOTP_SECRET.slice(0, 32) });
      assert.equal(r.status, 200);
      for (const c of codes) assert.match(r.text, new RegExp(c));
      assert.match(r.text, /nur jetzt angezeigt/);
      assert.equal(b.cookie('session'), full);
      assert.equal(r.headers['cache-control'], 'no-store', 'the codes are not cached');
      backend.on('GET /v1/me', { status: 200, body: { id: 'u1', first_name: 'E', last_name: 'M', email: 'e@example.org', roles: ['patient'], mfa_enabled: true, must_change_password: false } });
      const again = await b.get('/setup');
      assert.equal(again.status, 302, 'a reload does not show the codes again');
    });
  });

  it('a full session does not see the setup pages', async () => {
    const t = loginAs(backend, { roles: ['patient'], scope: 'full' });
    const full = await sessionBrowser(app, t);
    assert.equal((await full.get('/setup')).location, '/appointments');
    assert.equal((await full.get('/')).location, '/appointments');
  });
});

describe('the own account (UF-04, UF-13)', () => {
  let backend, app, b, token;
  beforeEach(async () => {
    backend = new FakeBackend();
    app = await startApp(backend);
    token = loginAs(backend, { roles: ['patient'], me: { phone: '0621 123', address: { street: 'Hauptstr. 1', postal_code: '68159', city: 'Mannheim' } } });
    b = await sessionBrowser(app, token);
  });
  afterEach(() => app.close());

  it('shows the data, with the address for patients only', async () => {
    const r = await b.open('/account');
    assert.match(r.text, /value="Hauptstr. 1"/);
    assert.match(r.text, /value="0621 123"/);
    const be = new FakeBackend();
    const a = await startApp(be);
    const d = await sessionBrowser(a, loginAs(be, { roles: ['doctor'] }));
    assert.doesNotMatch((await d.get('/account')).text, /Straße und Hausnummer/);
    await a.close();
  });

  it('saves the profile (PATCH /v1/me) and shows backend validation errors', async () => {
    backend.on('PATCH /v1/me', { status: 200, body: {} });
    await b.open('/account');
    const r = await b.post('/account', { first_name: 'Erika', last_name: 'Muster', phone: '', street: 'Neue Str. 2', postal_code: '68161', city: 'Mannheim' }, { page: '/account' });
    assert.equal(r.location, '/account?m=profile_saved');
    assert.deepEqual(backend.called('PATCH /v1/me')[0].body, { first_name: 'Erika', last_name: 'Muster', phone: '', address: { street: 'Neue Str. 2', postal_code: '68161', city: 'Mannheim' } });
    backend.on('PATCH /v1/me', { status: 422, body: { code: 'validation_failed', errors: [{ field: 'phone', code: 'max_length' }] } });
    const bad = await b.post('/account', { first_name: 'E', last_name: 'M', phone: '9'.repeat(40) });
    assert.equal(bad.status, 422);
    assert.match(bad.text, /Die Telefonnummer ist zu lang/);
  });

  it('changes the e-mail address; a wrong password shows the reason', async () => {
    backend.on('POST /v1/me/email', { status: 202 });
    await b.open('/account');
    assert.equal((await b.post('/account/email', { new_email: 'neu@example.org', current_password: 'pw' })).location, '/account?m=email_requested');
    backend.on('POST /v1/me/email', { status: 422, body: { code: 'invalid_current_password' } });
    const r = await b.post('/account/email', { new_email: 'neu@example.org', current_password: 'falsch' });
    assert.equal(r.status, 422);
    assert.match(r.text, /aktuelle Passwort ist nicht richtig/);
  });

  it('changes the password and keeps the user logged in with the new token', async () => {
    const t2 = fakeToken({ sub: 'u1-new' });
    backend.on('POST /v1/me/password', { status: 200, body: { access_token: t2, scope: 'full' } });
    await b.open('/account');
    const r = await b.post('/account/password', { current_password: 'old', new_password: 'Gelb-Wolke-Stein-Fluss-77', password2: 'Gelb-Wolke-Stein-Fluss-77' });
    assert.equal(r.location, '/account?m=password_saved');
    assert.equal(b.cookie('session'), t2);
  });

  it('new recovery codes are shown once', async () => {
    backend.on('POST /v1/me/mfa/recovery-codes', { status: 200, body: { recovery_codes: ['aaaa-bbbb-cccc', 'dddd-eeee-ffff'] } });
    await b.open('/account');
    const r = await b.post('/account/mfa/recovery-codes', { code: '123456' });
    assert.equal(r.status, 200);
    assert.match(r.text, /aaaa-bbbb-cccc/);
    backend.on('POST /v1/me/mfa/recovery-codes', { status: 422, body: { code: 'invalid_code' } });
    assert.equal((await b.post('/account/mfa/recovery-codes', { code: '000000' })).status, 422);
  });

  it('removing the second factor ends the session on the client as well', async () => {
    backend.on('POST /v1/me/mfa/disable', { status: 204 });
    await b.open('/account');
    const r = await b.post('/account/mfa/disable', { mode: 'code', password: 'pw', code: '123456' });
    assert.equal(r.location, '/login?m=mfa_removed');
    assert.ok(!b.cookie('session'));
    assert.deepEqual(backend.called('POST /v1/me/mfa/disable')[0].body, { password: 'pw', code: '123456' });
  });

  it('removing with a recovery code sends recovery_code', async () => {
    backend.on('POST /v1/me/mfa/disable', { status: 204 });
    await b.open('/account');
    await b.post('/account/mfa/disable', { mode: 'recovery', password: 'pw', recovery_code: 'abcd-efgh-jkmn' });
    assert.deepEqual(backend.called('POST /v1/me/mfa/disable')[0].body, { password: 'pw', recovery_code: 'abcd-efgh-jkmn' });
  });

  it('deleting the account needs the confirmation box and then clears the session (UF-13)', async () => {
    backend.on('POST /v1/me/deletion', { status: 204 });
    await b.open('/account');
    const no = await b.post('/account/delete', { password: 'pw', code: '123456' });
    assert.equal(no.status, 422);
    assert.equal(backend.called('POST /v1/me/deletion').length, 0);
    const r = await b.post('/account/delete', { password: 'pw', code: '123456', confirm: 'yes' });
    assert.equal(r.location, '/login?m=deleted');
    assert.ok(!b.cookie('session'));
    assert.deepEqual(backend.called('POST /v1/me/deletion')[0].body, { password: 'pw', code: '123456' });
  });

  it('shows the lock message instead of an error page', async () => {
    backend.on('POST /v1/me/deletion', { status: 429, body: { code: 'temporarily_locked' } });
    await b.open('/account');
    const r = await b.post('/account/delete', { password: 'pw', code: '123456', confirm: 'yes' });
    assert.equal(r.status, 429);
    assert.match(r.text, /Zu viele Fehlversuche/);
  });
});
