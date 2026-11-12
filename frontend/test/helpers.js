// Test tools: a fake for the two backend services and a tiny browser (cookie jar, forms with CSRF token).
import http from 'node:http';
import { createApp } from '../src/app.js';
import { BackendUnavailable } from '../src/api.js';

export const config = {
  listen: '127.0.0.1:0', userServiceUrl: 'http://user', calendarServiceUrl: 'http://calendar',
  sessionSecret: 'test-secret-test-secret-test-secret-1234', cookieSecure: true, trustProxy: 1,
  timezone: 'Europe/Berlin', practiceName: 'Praxis Test', backendTimeoutMs: 1000,
};

// A JWT-shaped string whose payload the frontend can read (it never verifies it).
export function fakeToken({ scope = 'full', exp = Math.floor(Date.now() / 1000) + 900, sub = 'u1' } = {}) {
  const b = (o) => Buffer.from(JSON.stringify(o)).toString('base64url');
  return `${b({ alg: 'EdDSA', kid: 'k' })}.${b({ sub, scope, exp })}.c2ln`;
}

export class FakeBackend {
  constructor() {
    this.handlers = [];
    this.calls = [];
    this.down = false;
  }

  // on('POST /v1/auth/login', (call) => ({ status: 200, body: {...} }))
  on(route, handler) {
    this.handlers.unshift({ route, handler: typeof handler === 'function' ? handler : () => handler });
    return this;
  }

  async call(service, method, path, opts = {}) {
    const call = { service, method, path, ...opts };
    this.calls.push(call);
    if (this.down) throw new BackendUnavailable(service, new Error('down'));
    const key = `${method} ${path}`;
    const h = this.handlers.find((x) => x.route === key) ?? this.handlers.find((x) => x.route.endsWith('*') && key.startsWith(x.route.slice(0, -1)));
    if (!h) return { status: 404, body: { code: 'not_found' }, headers: new Headers() };
    const r = await h.handler(call);
    return { status: r.status ?? 200, body: r.body ?? null, headers: new Headers(r.headers ?? {}) };
  }

  called(route) {
    return this.calls.filter((c) => `${c.method} ${c.path}` === route);
  }
}

export async function startApp(backend, overrides = {}) {
  const logs = [];
  const app = createApp({ ...config, ...overrides }, { backend, log: (e) => logs.push(e) });
  const server = await new Promise((resolve) => {
    const s = app.listen(0, '127.0.0.1', () => resolve(s));
  });
  const base = { host: '127.0.0.1', port: server.address().port };
  return { app, server, base, logs, close: () => new Promise((r) => server.close(r)) };
}

export class Browser {
  constructor(base, { cookieSecure = true } = {}) {
    this.base = base;
    this.jar = new Map();
    this.last = null;
    this.prefix = cookieSecure ? '__Host-' : '';
  }

  cookie(name) {
    return this.jar.get(this.prefix + name);
  }

  request(method, path, { form, raw, headers = {}, follow = false } = {}) {
    return new Promise((resolve, reject) => {
      const body = raw ? raw.body : form ? encodeForm(form) : undefined;
      const h = { host: `${this.base.host}:${this.base.port}`, ...headers };
      if (this.jar.size) h.cookie = [...this.jar].map(([k, v]) => `${k}=${v}`).join('; ');
      if (body !== undefined) {
        h['content-type'] = raw ? raw.contentType : 'application/x-www-form-urlencoded';
        h['content-length'] = Buffer.byteLength(body);
      }
      const req = http.request({ ...this.base, method, path, headers: h }, (res) => {
        const chunks = [];
        res.on('data', (c) => chunks.push(c));
        res.on('end', () => {
          const setCookies = res.headers['set-cookie'] ?? [];
          for (const sc of setCookies) {
            const [pair] = sc.split(';');
            const i = pair.indexOf('=');
            const name = pair.slice(0, i);
            const value = pair.slice(i + 1);
            if (/max-age=0/i.test(sc) || /expires=thu, 01 jan 1970/i.test(sc)) this.jar.delete(name);
            else this.jar.set(name, value);
          }
          const out = { status: res.statusCode, headers: res.headers, setCookies, text: Buffer.concat(chunks).toString('utf8'), location: res.headers.location };
          this.last = out;
          if (follow && out.location && [301, 302, 303, 307, 308].includes(out.status)) {
            return resolve(this.request('GET', out.location, { follow: true }));
          }
          resolve(out);
        });
      });
      req.on('error', reject);
      if (body !== undefined) req.write(body);
      req.end();
    });
  }

  get(path, opts) {
    return this.request('GET', path, opts);
  }

  // Posts a form the way a browser does: with the CSRF token of the page that was loaded last (loads /login if none).
  async post(path, fields = {}, { page = '/login', headers, follow } = {}) {
    if (!this.jar.has(`${this.prefix}csrf`)) await this.get(page);
    const token = this.token ?? (await this.freshToken(page));
    return this.request('POST', path, { form: { _csrf: token, ...fields }, headers, follow });
  }

  async freshToken(page) {
    const r = await this.get(page);
    return tokenOf(r.text);
  }

  // Loads a page and remembers its CSRF token for the next post.
  async open(path) {
    const r = await this.get(path);
    this.token = tokenOf(r.text);
    return r;
  }
}

// multipart/form-data like a browser sends it: parts = [{ name, value } | { name, filename, type, data }]
export function multipart(parts, boundary = '----testboundary7MA4YWxkTrZu0gW') {
  const out = [];
  for (const p of parts) {
    const disp = `Content-Disposition: form-data; name="${p.name}"${p.filename ? `; filename="${p.filename}"` : ''}`;
    out.push(Buffer.from(`--${boundary}\r\n${disp}\r\n${p.type ? `Content-Type: ${p.type}\r\n` : ''}\r\n`), Buffer.from(p.data ?? p.value), Buffer.from('\r\n'));
  }
  out.push(Buffer.from(`--${boundary}--\r\n`));
  return { contentType: `multipart/form-data; boundary=${boundary}`, body: Buffer.concat(out) };
}

// application/x-www-form-urlencoded; an array becomes a repeated field, like several ticked checkboxes.
function encodeForm(fields) {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(fields)) for (const x of Array.isArray(v) ? v : [v]) p.append(k, String(x));
  return p.toString();
}

export function tokenOf(html) {
  return /name="_csrf" value="([^"]+)"/.exec(html)?.[1];
}

export const hasText = (html, s) => html.includes(s);

// Logs a browser in through the fake backend: a user with the given roles and a token of the given scope.
export function loginAs(backend, { roles = ['patient'], scope = 'full', me = {}, token } = {}) {
  const t = token ?? fakeToken({ scope });
  backend.on('GET /v1/me', ({ token: sent }) => {
    if (sent !== t) return { status: 401, body: { code: 'unauthorized' } };
    return { status: 200, body: { id: 'u1', first_name: 'Erika', last_name: 'Muster', email: 'erika@example.org', roles, status: 'active', mfa_enabled: scope === 'full', must_change_password: scope !== 'full', phone: null, address: { street: null, postal_code: null, city: null }, ...me } };
  });
  return t;
}

export async function sessionBrowser(app, token, opts) {
  const b = new Browser(app.base, opts);
  b.jar.set(`${b.prefix}session`, token);
  return b;
}

export const UUID = (n) => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`;

export function booking(o = {}) {
  return {
    id: UUID(100), status: 'bestaetigt', starts_at: '2030-01-02T09:00:00+01:00', ends_at: '2030-01-02T09:30:00+01:00',
    service: { id: UUID(30), name: 'Kontrolle' }, doctor: { id: UUID(10), first_name: 'Dora', last_name: 'Doktor' },
    reason: 'Husten', info: null, ...o,
  };
}

// Routes of the Express app: [{ method, path }]
export function listRoutes(app) {
  const out = [];
  for (const layer of app.router.stack) {
    if (!layer.route) continue;
    for (const method of Object.keys(layer.route.methods)) out.push({ method: method.toUpperCase(), path: layer.route.path });
  }
  return out;
}

export const TOTP_SECRET = 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP';
