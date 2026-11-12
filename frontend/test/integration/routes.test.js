import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import { Browser, FakeBackend, listRoutes, startApp, UUID } from '../helpers.js';

// Checks every route the application registers, so that a new route cannot silently skip CSRF or login.
describe('every route', () => {
  let backend, app, routes;
  before(async () => {
    backend = new FakeBackend();
    app = await startApp(backend);
    routes = listRoutes(app.app).filter((r) => r.path !== '/healthz');
  });
  after(() => app.close());

  const concrete = (p) => p.replace(/:[A-Za-z]+/g, UUID(1));
  const PUBLIC_GET = new Set(['/', '/login', '/login/mfa', '/register', '/verify-email', '/confirm-email-change', '/forgot-password', '/reset-password']);
  const PUBLIC_POST = new Set(['/login', '/login/mfa', '/register', '/verify-email', '/confirm-email-change', '/forgot-password', '/reset-password']);

  it('knows the routes', () => {
    assert.ok(routes.length > 50, `only ${routes.length} routes found`);
    assert.ok(routes.some((r) => r.method === 'POST') && routes.some((r) => r.method === 'GET'));
  });

  it('refuses every POST that has no valid CSRF token, before anything else happens', async () => {
    const b = new Browser(app.base);
    await b.get('/login');
    for (const r of routes.filter((x) => x.method === 'POST')) {
      const res = await b.request('POST', concrete(r.path), { form: { x: 'y' } });
      assert.equal(res.status, 403, `POST ${r.path}`);
    }
    assert.equal(backend.calls.filter((c) => c.method !== 'GET').length, 0, 'no backend was called');
  });

  it('sends every visitor without a session from a protected page to the login', async () => {
    const b = new Browser(app.base);
    for (const r of routes.filter((x) => x.method === 'GET' && !PUBLIC_GET.has(x.path))) {
      const res = await b.get(concrete(r.path));
      assert.equal(res.status, 302, `GET ${r.path}`);
      assert.match(res.location, /^\/login/, `GET ${r.path}`);
    }
  });

  it('answers every protected POST without a session with a redirect to the login (token present)', async () => {
    const b = new Browser(app.base);
    await b.open('/login');
    for (const r of routes.filter((x) => x.method === 'POST' && !PUBLIC_POST.has(x.path) && x.path !== '/logout')) {
      const res = await b.post(concrete(r.path), {});
      assert.equal(res.status, 302, `POST ${r.path}`);
      assert.match(res.location, /^\/login/, `POST ${r.path}`);
    }
    assert.equal(backend.calls.filter((c) => c.method !== 'GET').length, 0);
  });

  it('only GET, HEAD and POST exist: nothing changes state through GET', () => {
    for (const r of routes) assert.ok(['GET', 'POST'].includes(r.method), `${r.method} ${r.path}`);
    const names = routes.filter((r) => r.method === 'GET').map((r) => r.path);
    for (const n of names) assert.doesNotMatch(n, /delete|cancel|assign|unlock|reset-password-now|logout/, n);
  });
});
