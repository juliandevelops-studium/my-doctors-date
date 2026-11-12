// Cookies: parsing and the options of the three cookies this application sets.
//   session  the access token of user_service (15 minutes), HttpOnly, never readable by JavaScript
//   mfa      the MFA challenge between password and code (5 minutes)
//   csrf     random value; the token in the forms is an HMAC of it
// All are HttpOnly, SameSite=Strict, path "/" and, over HTTPS, Secure with the __Host- prefix (bound to this host,
// no Domain attribute, so a sibling sub-domain cannot overwrite them).

export function parseCookies(header) {
  const out = {};
  if (!header) return out;
  for (const part of header.split(';')) {
    const i = part.indexOf('=');
    if (i < 0) continue;
    const name = part.slice(0, i).trim();
    const value = part.slice(i + 1).trim();
    if (name && !(name in out)) out[name] = value; // the first one wins
  }
  return out;
}

export function cookieNames(secure) {
  const p = secure ? '__Host-' : '';
  return { session: `${p}session`, mfa: `${p}mfa`, csrf: `${p}csrf` };
}

export function cookieOptions(secure, maxAgeSeconds) {
  const o = { httpOnly: true, secure, sameSite: 'strict', path: '/' };
  if (maxAgeSeconds) o.maxAge = maxAgeSeconds * 1000;
  return o;
}

// The payload of a JWT, for the user interface only (expiry, scope). It is not verified here: the backend services
// verify the signature on every request, and the frontend makes no decision based on it.
export function tokenInfo(token) {
  try {
    const payload = JSON.parse(Buffer.from(token.split('.')[1], 'base64url').toString('utf8'));
    return { exp: Number(payload.exp) || 0, scope: payload.scope || '' };
  } catch {
    return null;
  }
}
