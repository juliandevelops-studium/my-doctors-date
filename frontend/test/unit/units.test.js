import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { Backend, BackendUnavailable } from '../../src/api.js';
import { cookieNames, cookieOptions, parseCookies, tokenInfo } from '../../src/cookies.js';
import { tokenFor } from '../../src/csrf.js';
import { fieldErrors, FLASH, problemMessage } from '../../src/errors.js';
import { emptyToUndefined, idParam, intParam, isUuid, list, oneOf, secret, text, TOKEN } from '../../src/form.js';
import { makeFormatters } from '../../src/format.js';
import { groupByDay, INSTANT, weekRange } from '../../src/calendar.js';
import { homeFor, safeNext } from '../../src/middleware.js';

const fmt = makeFormatters('Europe/Berlin');

describe('dates in the practice time zone', () => {
  it('formats German date and time', () => {
    assert.equal(fmt.dateTime('2030-01-02T08:30:00Z'), 'Mi. 02.01.2030 09:30 Uhr');
    assert.equal(fmt.dateTime('2030-07-02T08:30:00Z'), 'Di. 02.07.2030 10:30 Uhr');
    assert.equal(fmt.time('2030-01-02T23:30:00Z'), '00:30 Uhr');
    assert.equal(fmt.ymd('2030-01-02T23:30:00Z'), '2030-01-03');
    assert.equal(fmt.dateTime(''), '');
    assert.equal(fmt.dateTime('not a date'), '');
  });
  it('start of day is midnight of the practice, also around the change of the clocks', () => {
    assert.equal(fmt.startOfDay('2030-01-15').toISOString(), '2030-01-14T23:00:00.000Z');
    assert.equal(fmt.startOfDay('2030-07-15').toISOString(), '2030-07-14T22:00:00.000Z');
    assert.equal(fmt.startOfDay('2030-03-31').toISOString(), '2030-03-30T23:00:00.000Z');
    assert.equal(fmt.startOfDay('2030-04-01').toISOString(), '2030-03-31T22:00:00.000Z');
    assert.equal(fmt.startOfDay('2030-10-27').toISOString(), '2030-10-26T22:00:00.000Z');
    assert.equal(fmt.startOfDay('2030-10-28').toISOString(), '2030-10-27T23:00:00.000Z');
  });
  it('refuses anything that is not a real date', () => {
    for (const bad of [undefined, null, '', '2030-13-01', '2030-02-30', '30.01.2030', '2030-1-1', "2030-01-01'", 5]) assert.equal(fmt.startOfDay(bad), null, String(bad));
  });
  it('adds days across the month and year', () => {
    assert.equal(fmt.addDays('2030-12-29', 7), '2031-01-05');
    assert.equal(fmt.addDays('2030-03-01', -1), '2030-02-28');
  });
  it('builds weeks and groups by day', () => {
    const w = weekRange(fmt, '2030-01-07', { allowPast: true });
    assert.deepEqual([w.from, w.to, w.prev, w.next], ['2030-01-07', '2030-01-14', '2029-12-31', '2030-01-14']);
    assert.equal(w.toInstant - w.fromInstant, 7 * 24 * 3600 * 1000);
    const g = groupByDay([{ starts_at: '2030-01-07T08:00:00Z' }, { starts_at: '2030-01-07T09:00:00Z' }, { starts_at: '2030-01-08T09:00:00Z' }], fmt);
    assert.deepEqual(g.map((d) => [d.day, d.items.length]), [['2030-01-07', 2], ['2030-01-08', 1]]);
    assert.ok(INSTANT.test('2030-01-07T09:00:00+01:00') && INSTANT.test('2030-01-07T09:00:00Z') && !INSTANT.test('2030-01-07 09:00') && !INSTANT.test('x'));
  });
});

describe('cookies', () => {
  it('parses a header, the first value wins, junk is ignored', () => {
    assert.deepEqual(parseCookies('a=1; b=2;c=3; a=9; broken; =x'), { a: '1', b: '2', c: '3' });
    assert.deepEqual(parseCookies(undefined), {});
  });
  it('uses the __Host- prefix over HTTPS only', () => {
    assert.deepEqual(cookieNames(true), { session: '__Host-session', mfa: '__Host-mfa', csrf: '__Host-csrf' });
    assert.deepEqual(cookieNames(false), { session: 'session', mfa: 'mfa', csrf: 'csrf' });
  });
  it('sets the options of every cookie', () => {
    assert.deepEqual(cookieOptions(true, 60), { httpOnly: true, secure: true, sameSite: 'strict', path: '/', maxAge: 60000 });
    assert.equal(cookieOptions(true).maxAge, undefined);
    assert.equal(cookieOptions(true).domain, undefined);
  });
  it('reads the claims of a token for the interface only, and survives garbage', () => {
    const t = `x.${Buffer.from(JSON.stringify({ exp: 5, scope: 'setup' })).toString('base64url')}.y`;
    assert.deepEqual(tokenInfo(t), { exp: 5, scope: 'setup' });
    for (const bad of ['', 'a', 'a.b', 'a.!!.c', 'a.e30.c']) assert.ok(tokenInfo(bad) === null || tokenInfo(bad).exp === 0, bad);
  });
});

describe('csrf token', () => {
  it('depends on secret and cookie and is stable', () => {
    assert.equal(tokenFor('s', 'c'), tokenFor('s', 'c'));
    assert.notEqual(tokenFor('s', 'c'), tokenFor('s', 'd'));
    assert.notEqual(tokenFor('s', 'c'), tokenFor('t', 'c'));
    assert.match(tokenFor('s', 'c'), /^[A-Za-z0-9_-]{43}$/);
  });
});

describe('redirect targets', () => {
  it('allows local paths only', () => {
    for (const ok of ['/', '/book/abc', '/staff/bookings?view=open&offset=25', '/calendar?from=2030-01-07']) assert.equal(safeNext(ok, 'X'), ok);
    for (const bad of ['//evil.example', 'https://evil.example', '/\\evil', '/../x', 'javascript:alert(1)', '', undefined, null, 5, '/a b', '/a\nb', '/x'.repeat(300), '/a"b', '/<script>']) {
      assert.equal(safeNext(bad, 'X'), 'X', String(bad));
    }
  });
  it('knows the home page of each role', () => {
    assert.equal(homeFor(['patient']), '/appointments');
    assert.equal(homeFor(['doctor']), '/calendar');
    assert.equal(homeFor(['doctor', 'patient']), '/calendar');
    assert.equal(homeFor(['desk']), '/staff/bookings');
    assert.equal(homeFor(['systemadmin']), '/admin/users');
    assert.equal(homeFor([]), '/appointments');
  });
});

describe('form input', () => {
  it('reads text, never an array or an object', () => {
    assert.equal(text({ a: '  x  ' }, 'a'), 'x');
    assert.equal(text({ a: ['x', 'y'] }, 'a'), '');
    assert.equal(text({ a: { b: 1 } }, 'a'), '');
    assert.equal(text({}, 'a'), '');
    assert.equal(text({ a: 'x'.repeat(50) }, 'a', 5), 'xxxxx');
    assert.equal(secret({ p: '  keep spaces  ' }, 'p'), '  keep spaces  ');
    assert.deepEqual(list({ r: ['a', 'b', { x: 1 }] }, 'r'), ['a', 'b']);
    assert.deepEqual(list({ r: 'a' }, 'r'), ['a']);
    assert.deepEqual(list({}, 'r'), []);
  });
  it('checks ids and numbers', () => {
    assert.ok(isUuid('00000000-0000-4000-8000-000000000001'));
    for (const bad of ['', 'x', '../..', '00000000-0000-4000-8000-00000000000g', '00000000-0000-4000-8000-0000000000012', null, 5]) assert.equal(isUuid(bad), false, String(bad));
    assert.throws(() => idParam('nope'), { status: 404 });
    assert.equal(intParam('25'), 25);
    assert.equal(intParam('-1'), 0);
    assert.equal(intParam('1e9'), 1);
    assert.equal(intParam('abc', { fallback: 7 }), 7);
    assert.equal(oneOf('a', ['a', 'b']), 'a');
    assert.equal(oneOf('c', ['a', 'b']), undefined);
    assert.equal(emptyToUndefined(''), undefined);
    assert.ok(TOKEN.test('A'.repeat(43)) && !TOKEN.test('short') && !TOKEN.test('A'.repeat(20) + '<'));
  });
});

describe('error texts', () => {
  it('translate codes and never use text of the backend', () => {
    assert.match(problemMessage({ code: 'slot_taken', title: 'EVIL <script>' }), /gerade vergeben/);
    assert.equal(problemMessage({ code: 'unknown_code', title: 'EVIL' }, 'Fallback'), 'Fallback');
    assert.equal(problemMessage(null, 'F'), 'F');
    assert.match(problemMessage({ code: 'validation_failed', errors: [{ field: 'password', code: 'password_blocklisted' }] }), /steht auf der Liste/);
  });
  it('maps field errors by field name, also for nested names', () => {
    const e = fieldErrors({ errors: [{ field: 'password', code: 'min_length' }, { field: 'address.city', code: 'max_length' }, { field: 'x', code: 'unknown' }] });
    assert.equal(e.password[0], 'Passwort ist zu kurz.');
    assert.equal(e.city[0], 'Die Eingabe ist zu lang.');
    assert.equal(e.x[0], 'Die Eingabe ist ungültig.');
    assert.deepEqual(fieldErrors(null), {});
  });
  it('has a text for every message key that the code redirects to', () => {
    for (const key of ['registered', 'verified', 'reset_sent', 'password_reset', 'expired', 'logged_out', 'deleted', 'mfa_removed', 'email_changed', 'email_requested', 'profile_saved', 'password_saved', 'booked', 'cancelled', 'rescheduled', 'assigned', 'saved', 'created', 'deleted_entry', 'user_created', 'roles_saved', 'reset_requested', 'unlocked', 'main_doctor_saved']) {
      assert.ok(FLASH[key], key);
    }
  });
});

describe('backend client', () => {
  const make = (fetchImpl) => new Backend({ userServiceUrl: 'http://user/', calendarServiceUrl: 'http://cal', fetchImpl });
  const reply = (status, body) => async () => new Response(body === undefined ? null : JSON.stringify(body), { status });

  it('forwards token, client address, query and JSON body', async () => {
    let seen;
    const b = make(async (url, init) => { seen = { url: String(url), init }; return new Response('{"ok":true}', { status: 200 }); });
    const r = await b.call('calendar', 'POST', '/v1/x', { token: 'T', ip: '203.0.113.5', query: { a: 1, b: '', c: undefined, d: 'x y' }, body: { n: 1 } });
    assert.equal(seen.url, 'http://cal/v1/x?a=1&d=x+y');
    assert.equal(seen.init.headers.Authorization, 'Bearer T');
    assert.equal(seen.init.headers['X-Forwarded-For'], '203.0.113.5');
    assert.equal(seen.init.body, '{"n":1}');
    assert.equal(seen.init.redirect, 'error', 'a redirect would forward the token elsewhere');
    assert.deepEqual(r, { status: 200, body: { ok: true }, headers: r.headers });
  });
  it('sends no Authorization header without a token', async () => {
    let headers;
    await make(async (u, init) => { headers = init.headers; return new Response(null, { status: 204 }); }).call('user', 'GET', '/x');
    assert.equal(headers.Authorization, undefined);
  });
  it('returns the problem of an error answer and tolerates a body that is no JSON', async () => {
    assert.equal((await make(reply(409, { code: 'slot_taken' })).call('user', 'GET', '/x')).body.code, 'slot_taken');
    assert.equal((await make(async () => new Response('<html>', { status: 502 })).call('user', 'GET', '/x')).body, null);
    assert.equal((await make(reply(204)).call('user', 'GET', '/x')).body, null);
  });
  it('turns a network failure into BackendUnavailable', async () => {
    await assert.rejects(make(async () => { throw new TypeError('fetch failed'); }).call('user', 'GET', '/x'), BackendUnavailable);
  });
});
