// Settings of frontend_provider, read from the environment. The session secret comes from a mounted file
// (Kubernetes Secret), never from the image or the repository.
import { readFileSync } from 'node:fs';

function required(env, name) {
  if (!env[name]) throw new Error(`${name} is required`);
  return env[name];
}

export function loadConfig(env = process.env) {
  let secret = env.SESSION_SECRET;
  if (!secret && env.SESSION_SECRET_FILE) secret = readFileSync(env.SESSION_SECRET_FILE, 'utf8').trim();
  if (!secret || secret.length < 32) throw new Error('SESSION_SECRET_FILE must contain at least 32 characters');
  const secure = env.COOKIE_SECURE !== 'false';
  return {
    listen: env.LISTEN_ADDR || '0.0.0.0:8080',
    userServiceUrl: required(env, 'USER_SERVICE_URL'),
    calendarServiceUrl: required(env, 'CALENDAR_SERVICE_URL'),
    sessionSecret: secret,
    // __Host- cookies need HTTPS. Plain HTTP is only for local development and tests.
    cookieSecure: secure,
    trustProxy: Number(env.TRUST_PROXY ?? 1),
    timezone: env.PRACTICE_TZ || 'Europe/Berlin',
    practiceName: env.PRACTICE_NAME || 'Arztpraxis',
    backendTimeoutMs: Number(env.BACKEND_TIMEOUT_MS ?? 10000),
  };
}
