// Reading form input. Everything that comes from the browser is checked here before it is passed on or put into a
// backend path; the backends validate again (they are authoritative).
import { HttpError } from './errors.js';

const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
export const TOKEN = /^[A-Za-z0-9_-]{20,256}$/;

// A single-line or multi-line text value, trimmed, limited in length. Arrays (repeated fields) become the empty string.
export function text(source, name, max = 200) {
  const v = source?.[name];
  if (typeof v !== 'string') return '';
  return v.trim().slice(0, max);
}

// Like text(), but passwords are never trimmed.
export function secret(source, name, max = 512) {
  const v = source?.[name];
  return typeof v === 'string' ? v.slice(0, max) : '';
}

export function list(source, name) {
  const v = source?.[name];
  if (v === undefined) return [];
  return (Array.isArray(v) ? v : [v]).filter((x) => typeof x === 'string');
}

export function isUuid(v) {
  return typeof v === 'string' && UUID.test(v);
}

// A path parameter that ends up in a backend URL. Anything but a UUID is a 404.
export function idParam(v) {
  if (!isUuid(v)) throw new HttpError(404, { code: 'not_found' });
  return v;
}

export function optionalUuid(v) {
  return isUuid(v) ? v : undefined;
}

export function intParam(v, { min = 0, max = 1_000_000, fallback = 0 } = {}) {
  const n = Number.parseInt(v, 10);
  return Number.isInteger(n) && n >= min && n <= max ? n : fallback;
}

export function emptyToUndefined(v) {
  return v === '' ? undefined : v;
}

export function oneOf(v, allowed) {
  return allowed.includes(v) ? v : undefined;
}

export const ROLES = ['patient', 'doctor', 'desk', 'organisator', 'systemadmin'];
