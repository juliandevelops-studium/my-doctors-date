// Tools for the end-to-end test: a browser that speaks HTTPS to the real ingress (certificate of the local CA is checked),
// a mailbox (Mailpit) and TOTP codes like an authenticator app.
import { createHmac } from 'node:crypto';
import { readFileSync } from 'node:fs';
import http from 'node:http';
import https from 'node:https';

export const env = {
  base: process.env.E2E_BASE_URL ?? 'https://praxis.localhost:8443',
  httpBase: process.env.E2E_HTTP_BASE_URL ?? 'http://praxis.localhost:8080',
  ca: process.env.E2E_CA ? readFileSync(process.env.E2E_CA) : undefined,
  mailpit: process.env.E2E_MAILPIT_URL,
  adminFile: process.env.E2E_ADMIN_FILE,
  adminState: process.env.E2E_ADMIN_STATE,
};

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export class Browser {
  constructor(name = 'browser') {
    this.name = name;
    this.jar = new Map();
    this.token = null;
    this.page = null;
  }

  cookie(n) {
    return this.jar.get(n);
  }

  request(method, path, { form, headers = {}, base = env.base } = {}) {
    const url = new URL(base);
    const secure = url.protocol === 'https:';
    return new Promise((resolve, reject) => {
      const body = form ? encode(form) : undefined;
      const h = { host: url.host, ...headers };
      if (this.jar.size) h.cookie = [...this.jar].map(([k, v]) => `${k}=${v}`).join('; ');
      if (body !== undefined) {
        h['content-type'] = 'application/x-www-form-urlencoded';
        h['content-length'] = Buffer.byteLength(body);
        h.origin = base; // like a browser submitting a form
        h['sec-fetch-site'] = 'same-origin';
      }
      const opts = { host: '127.0.0.1', port: url.port || (secure ? 443 : 80), method, path, headers: h };
      if (secure) Object.assign(opts, { ca: env.ca, servername: url.hostname });
      const req = (secure ? https : http).request(opts, (res) => {
        const chunks = [];
        res.on('data', (c) => chunks.push(c));
        res.on('end', () => {
          const setCookies = res.headers['set-cookie'] ?? [];
          for (const sc of setCookies) {
            const [pair] = sc.split(';');
            const i = pair.indexOf('=');
            if (/max-age=0/i.test(sc) || /expires=thu, 01 jan 1970/i.test(sc)) this.jar.delete(pair.slice(0, i));
            else this.jar.set(pair.slice(0, i), pair.slice(i + 1));
          }
          const text = Buffer.concat(chunks).toString('utf8');
          const out = { status: res.statusCode, headers: res.headers, setCookies, text, location: res.headers.location };
          if (res.headers['content-type']?.includes('text/html')) this.page = { text, token: csrfOf(text) };
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

  // Follows redirects (to local paths) and returns the last page.
  async go(path) {
    let r = await this.get(path);
    for (let i = 0; i < 6 && r.location && r.status >= 300 && r.status < 400; i++) r = await this.get(new URL(r.location, env.base).pathname + new URL(r.location, env.base).search);
    return r;
  }

  // Posts a form with the CSRF token of the page that was loaded last. Does not follow the redirect.
  async post(path, fields = {}, { headers } = {}) {
    if (!this.page?.token) await this.get('/login');
    return this.request('POST', path, { form: { _csrf: this.page.token, ...fields }, headers });
  }

  // Submits a form and follows the redirect.
  async submit(path, fields = {}) {
    const r = await this.post(path, fields);
    if (r.location && r.status >= 300 && r.status < 400) return this.go(new URL(r.location, env.base).pathname + new URL(r.location, env.base).search);
    return r;
  }
}

function encode(fields) {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(fields)) for (const x of Array.isArray(v) ? v : [v]) p.append(k, String(x));
  return p.toString();
}

export const csrfOf = (html) => /name="_csrf" value="([^"]+)"/.exec(html)?.[1];

export const decode = (html) => html.replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');

// ---------------------------------------------------------------- mail (Mailpit API)
export async function mails(to) {
  const r = await fetch(`${env.mailpit}/api/v1/search?query=${encodeURIComponent(`to:${to}`)}&limit=50`);
  const body = await r.json();
  return body.messages ?? [];
}

export async function waitMail(to, subjectPart, { since = 0, timeout = 25000 } = {}) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const hit = (await mails(to)).find((m) => m.Subject.includes(subjectPart) && new Date(m.Created).getTime() >= since);
    if (hit) {
      const full = await (await fetch(`${env.mailpit}/api/v1/message/${hit.ID}`)).json();
      return { subject: hit.Subject, text: full.Text, to: full.To?.[0]?.Address };
    }
    await sleep(400);
  }
  throw new Error(`no mail "${subjectPart}" to ${to} within ${timeout} ms`);
}

// The path and query of the link in a mail, e.g. "/reset-password?token=...".
export function linkOf(mail, path) {
  const m = new RegExp(`${env.base.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}(${path}\\?token=[A-Za-z0-9_-]+)`).exec(mail.text);
  if (!m) throw new Error(`no ${path} link in mail "${mail.subject}": ${mail.text.slice(0, 200)}`);
  return m[1];
}

// ---------------------------------------------------------------- TOTP (RFC 6238, like an authenticator app)
const B32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
function b32decode(s) {
  let bits = 0;
  let value = 0;
  const out = [];
  for (const ch of s) {
    value = (value << 5) | B32.indexOf(ch);
    bits += 5;
    if (bits >= 8) { out.push((value >>> (bits - 8)) & 255); bits -= 8; }
  }
  return Buffer.from(out);
}

export function totp(secret, step) {
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(step));
  const mac = createHmac('sha1', b32decode(secret)).update(msg).digest();
  const o = mac[mac.length - 1] & 15;
  return String((mac.readUInt32BE(o) & 0x7fffffff) % 1_000_000).padStart(6, '0');
}

// A code the server has not seen yet: steps are used once (replay protection). Waits for the next step when the
// three valid ones (-1, 0, +1) of this user are used up.
export async function nextCode(user) {
  for (;;) {
    const now = Math.floor(Date.now() / 30000);
    const step = Math.max((user.lastStep ?? 0) + 1, now - 1);
    if (step <= now + 1) {
      user.lastStep = step;
      return totp(user.secret, step);
    }
    await sleep((step - 1) * 30000 - Date.now() + 300);
  }
}
