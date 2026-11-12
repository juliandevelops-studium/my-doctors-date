import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { Browser, FakeBackend, fakeToken, loginAs, sessionBrowser, startApp } from '../helpers.js';

describe('security headers, CSRF and cookies', () => {
  let backend, app;
  before(async () => {
    backend = new FakeBackend();
    app = await startApp(backend);
  });
  after(() => app.close());

  it('sends the security headers on every page', async () => {
    const r = await new Browser(app.base).get('/login');
    const h = r.headers;
    assert.equal(r.status, 200);
    assert.equal(h['content-security-policy'], "default-src 'none';style-src 'self';img-src 'self';form-action 'self';frame-ancestors 'none';base-uri 'none'");
    assert.match(h['strict-transport-security'], /max-age=31536000; includeSubDomains/);
    assert.equal(h['x-content-type-options'], 'nosniff');
    assert.equal(h['referrer-policy'], 'no-referrer');
    assert.equal(h['x-frame-options'], 'DENY');
    assert.equal(h['cross-origin-opener-policy'], 'same-origin');
    assert.equal(h['cache-control'], 'no-store');
    assert.match(h['permissions-policy'], /camera=\(\)/);
    assert.equal(h['x-powered-by'], undefined);
    assert.doesNotMatch(r.text, /<script/i, 'the application ships no JavaScript');
    assert.doesNotMatch(r.text, /\son[a-z]+=/i, 'no inline event handlers');
  });

  it('sets the CSRF cookie HttpOnly, Secure, SameSite=Strict with the __Host- prefix', async () => {
    const r = await new Browser(app.base).get('/login');
    const c = r.setCookies.find((x) => x.startsWith('__Host-csrf='));
    assert.ok(c, 'csrf cookie');
    assert.match(c, /; Path=\//);
    assert.match(c, /; HttpOnly/);
    assert.match(c, /; Secure/);
    assert.match(c, /; SameSite=Strict/);
    assert.doesNotMatch(c, /Domain=/i);
  });

  it('rejects a POST without a CSRF token', async () => {
    const b = new Browser(app.base);
    await b.get('/login');
    const r = await b.request('POST', '/login', { form: { email: 'a@example.org', password: 'x' } });
    assert.equal(r.status, 403);
    assert.equal(backend.called('POST /v1/auth/login').length, 0);
  });

  it('rejects a POST with a wrong CSRF token or a token of another visitor', async () => {
    const b = new Browser(app.base);
    await b.get('/login');
    assert.equal((await b.request('POST', '/login', { form: { _csrf: 'nope', email: 'a@example.org', password: 'x' } })).status, 403);
    const other = new Browser(app.base);
    const tokenOfOther = (await other.open('/login')).text.match(/name="_csrf" value="([^"]+)"/)[1];
    assert.equal((await b.request('POST', '/login', { form: { _csrf: tokenOfOther, email: 'a@example.org', password: 'x' } })).status, 403);
    assert.equal(backend.called('POST /v1/auth/login').length, 0);
  });

  it('rejects a POST from another origin even with a valid token', async () => {
    const b = new Browser(app.base);
    await b.open('/login');
    for (const headers of [{ origin: 'https://evil.example' }, { origin: 'null' }, { 'sec-fetch-site': 'cross-site' }, { 'sec-fetch-site': 'same-site' }]) {
      const r = await b.post('/login', { email: 'a@example.org', password: 'x' }, { headers });
      assert.equal(r.status, 403, JSON.stringify(headers));
    }
    assert.equal(backend.called('POST /v1/auth/login').length, 0);
  });

  it('accepts a POST from the same origin', async () => {
    backend.on('POST /v1/auth/login', { status: 401, body: { code: 'invalid_credentials' } });
    const b = new Browser(app.base);
    await b.open('/login');
    const r = await b.post('/login', { email: 'a@example.org', password: 'x' }, { headers: { origin: `http://127.0.0.1:${app.base.port}`, 'sec-fetch-site': 'same-origin' } });
    assert.equal(r.status, 401);
  });

  it('accepts Origin "null" only when the browser says the post is same-origin (no-referrer policy)', async () => {
    backend.on('POST /v1/auth/login', { status: 401, body: { code: 'invalid_credentials' } });
    const b = new Browser(app.base);
    await b.open('/login');
    const ok = await b.post('/login', { email: 'a@example.org', password: 'x' }, { headers: { origin: 'null', 'sec-fetch-site': 'same-origin' } });
    assert.equal(ok.status, 401);
    const bad = await b.post('/login', { email: 'a@example.org', password: 'x' }, { headers: { origin: 'null', 'sec-fetch-site': 'cross-site' } });
    assert.equal(bad.status, 403);
  });

  it('does not change anything on GET: e-mail links open a page with a button', async () => {
    const token = 'A'.repeat(43);
    const r = await new Browser(app.base).get(`/verify-email?token=${token}`);
    assert.equal(r.status, 200);
    assert.match(r.text, /<form method="post" action="\/verify-email"/);
    assert.equal(backend.called('POST /v1/auth/verify-email').length, 0);
    assert.equal((await new Browser(app.base).get('/verify-email?token=short')).status, 400);
    assert.equal((await new Browser(app.base).get('/reset-password?token=<script>')).status, 400);
  });

  it('serves static files with a cache header and refuses path traversal', async () => {
    const b = new Browser(app.base);
    const css = await b.get('/static/app.css');
    assert.equal(css.status, 200);
    assert.match(css.headers['cache-control'], /public/);
    assert.match(css.headers['content-type'], /text\/css/);
    for (const p of ['/static/../server.js', '/static/%2e%2e/config.js', '/static/..%2f..%2fpackage.json', '/static/', '/src/config.js']) {
      assert.notEqual((await b.get(p)).status, 200, p);
    }
  });

  it('answers /healthz without a session lookup', async () => {
    const before = backend.calls.length;
    const r = await new Browser(app.base).get('/healthz');
    assert.equal(r.status, 200);
    assert.equal(r.text, 'ok');
    assert.equal(backend.calls.length, before);
  });

  it('shows no stack trace or internal detail when something fails', async () => {
    const b2 = new FakeBackend();
    b2.down = true;
    const a2 = await startApp(b2);
    const t = loginAs(new FakeBackend());
    const br = await sessionBrowser(a2, t);
    const r = await br.get('/appointments');
    assert.equal(r.status, 503);
    assert.doesNotMatch(r.text, /at .*\.js|ECONNREFUSED|stack|node_modules/i);
    await a2.close();
    void fakeToken;
  });
});
