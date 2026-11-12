// Session loading, route guards and the helper that calls the backends on behalf of the logged-in user.
import { cookieNames, cookieOptions, parseCookies, tokenInfo } from './cookies.js';
import { HttpError } from './errors.js';

// Only local paths are allowed as redirect targets after a login (no open redirect).
export function safeNext(value, fallback = '/') {
  if (typeof value !== 'string' || value.length > 500) return fallback;
  if (!/^\/[A-Za-z0-9\-._~/%?=&:+@,;]*$/.test(value) || value.startsWith('//') || value.includes('/..')) return fallback;
  return value;
}

export function clearSession(config, res) {
  const names = cookieNames(config.cookieSecure);
  res.clearCookie(names.session, cookieOptions(config.cookieSecure));
  res.clearCookie(names.mfa, cookieOptions(config.cookieSecure));
}

export function setSession(config, res, accessToken) {
  const info = tokenInfo(accessToken);
  const seconds = info?.exp ? Math.max(1, info.exp - Math.floor(Date.now() / 1000)) : 900;
  res.cookie(cookieNames(config.cookieSecure).session, accessToken, cookieOptions(config.cookieSecure, seconds));
}

export function attachApi(backend) {
  return (req, res, next) => {
    const call = (service, method, path, opts = {}) =>
      backend.call(service, method, path, { token: req.session?.token, ip: req.ip, ...opts });
    req.api = {
      // Returns { status, body } whatever the status is: for forms that show validation errors.
      raw: call,
      // Throws HttpError unless the answer is 2xx: for page loads and actions that have no form to go back to.
      async need(service, method, path, opts) {
        const r = await call(service, method, path, opts);
        if (r.status >= 200 && r.status < 300) return r;
        throw new HttpError(r.status, r.body);
      },
    };
    next();
  };
}

// Loads the profile of the logged-in user (one call to user_service per request; the backend validates the token).
export function session(config, backend) {
  const names = cookieNames(config.cookieSecure);
  return async (req, res, next) => {
    const token = parseCookies(req.headers.cookie)[names.session];
    res.locals.me = null;
    res.locals.roles = [];
    if (!token) return next();
    const info = tokenInfo(token);
    if (!info || info.exp * 1000 <= Date.now()) {
      clearSession(config, res);
      return next();
    }
    const r = await backend.call('user', 'GET', '/v1/me', { token, ip: req.ip });
    if (r.status === 401) {
      clearSession(config, res);
      return next();
    }
    if (r.status !== 200) throw new HttpError(r.status, r.body);
    req.session = { token, me: r.body, scope: info.scope };
    res.locals.me = r.body;
    res.locals.roles = r.body.roles;
    res.locals.scope = info.scope;
    next();
  };
}

export function requireLogin(req, res, next) {
  if (req.session) return next();
  const target = req.method === 'GET' ? safeNext(req.originalUrl, '/') : '/';
  res.redirect(`/login?next=${encodeURIComponent(target)}`);
}

// A user whose password or MFA setup is incomplete only reaches the setup pages (UF-03). The backends enforce this
// with the scope of the token; this redirect only saves the user from a wall of 401 answers.
export function requireFullSession(req, res, next) {
  if (req.session.scope !== 'full') return res.redirect('/setup');
  next();
}

// Shows a "no permission" page instead of an empty page. The backend decides in any case.
export function showOnlyFor(...roles) {
  return (req, res, next) => {
    if (roles.some((r) => req.session.me.roles.includes(r))) return next();
    throw new HttpError(403, { code: 'forbidden' });
  };
}

export const has = (req, ...roles) => roles.some((r) => req.session?.me?.roles.includes(r));

export function homeFor(roles) {
  if (roles.includes('systemadmin')) return '/admin/users';
  if (roles.includes('desk') || roles.includes('organisator')) return '/staff/bookings';
  if (roles.includes('doctor')) return '/calendar';
  return '/appointments';
}
