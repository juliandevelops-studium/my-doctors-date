// CSRF protection, three layers:
//  1. SameSite=Strict cookies (the browser does not send them on cross-site requests),
//  2. a token in every form: HMAC-SHA-256(secret, random csrf cookie), compared in constant time,
//  3. for unsafe methods the Origin / Sec-Fetch-Site header must be same-origin when the browser sends it.
import { createHmac, randomBytes, timingSafeEqual } from 'node:crypto';
import { cookieNames, cookieOptions, parseCookies } from './cookies.js';

export function tokenFor(secret, cookieValue) {
  return createHmac('sha256', secret).update(`csrf:${cookieValue}`).digest('base64url');
}

function same(a, b) {
  const x = Buffer.from(String(a));
  const y = Buffer.from(String(b));
  return x.length === y.length && timingSafeEqual(x, y);
}

export function newCsrfCookie() {
  return randomBytes(32).toString('base64url');
}

// Sets res.locals.csrfToken and enforces the checks on unsafe methods.
export function csrf(config, log = () => {}) {
  const names = cookieNames(config.cookieSecure);
  return (req, res, next) => {
    const cookies = parseCookies(req.headers.cookie);
    let value = cookies[names.csrf];
    const valid = typeof value === 'string' && /^[A-Za-z0-9_-]{43}$/.test(value);
    if (!valid) {
      value = newCsrfCookie();
      res.cookie(names.csrf, value, cookieOptions(config.cookieSecure));
    }
    req.csrfCookie = value;
    res.locals.csrfToken = tokenFor(config.sessionSecret, value);

    if (['GET', 'HEAD', 'OPTIONS'].includes(req.method)) return next();

    const site = req.get('sec-fetch-site');
    if (site && !['same-origin', 'none'].includes(site)) return deny(res, log, req, 'fetch_site');
    const origin = req.get('origin');
    if (origin && origin !== 'null') {
      let host;
      try {
        host = new URL(origin).host;
      } catch {
        return deny(res, log, req, 'origin_unparsable');
      }
      if (host !== req.get('host')) return deny(res, log, req, 'origin_mismatch');
    } else if (origin === 'null' && site !== 'same-origin') {
      // Browsers send "null" for a same-origin form post when the Referrer-Policy is no-referrer. Sandboxed frames and
      // redirects across sites also send it, but then Sec-Fetch-Site is not same-origin.
      return deny(res, log, req, 'origin_null');
    }
    if (!valid) return deny(res, log, req, 'no_csrf_cookie');
    if (!same(req.body?._csrf ?? '', res.locals.csrfToken)) return deny(res, log, req, 'token_mismatch');
    next();
  };
}

// The reason is logged (never a token or cookie value) so that a rejected form can be diagnosed.
function deny(res, log, req, reason) {
  log({ level: 'warn', msg: 'csrf check failed', reason, path: req.path, fetch_site: req.get('sec-fetch-site'), origin_host: safeHost(req.get('origin')), host: req.get('host') });
  res.status(403).render('error.njk', {
    title: 'Anfrage abgelehnt',
    message: 'Die Anfrage konnte nicht geprüft werden. Bitte laden Sie die Seite neu und versuchen Sie es noch einmal.',
  });
}

function safeHost(origin) {
  try {
    return new URL(origin).host;
  } catch {
    return origin ? 'unparsable' : undefined;
  }
}

// After a login or logout the old csrf value must not survive (session fixation of the form token).
export function rotateCsrf(config, res) {
  const value = newCsrfCookie();
  res.cookie(cookieNames(config.cookieSecure).csrf, value, cookieOptions(config.cookieSecure));
  res.locals.csrfToken = tokenFor(config.sessionSecret, value);
}
