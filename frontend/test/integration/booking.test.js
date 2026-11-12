import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, it } from 'node:test';
import { makeFormatters } from '../../src/format.js';
import { booking, FakeBackend, loginAs, sessionBrowser, startApp, UUID } from '../helpers.js';

const fmt = makeFormatters('Europe/Berlin');
const service = { id: UUID(30), name: 'Kontrolle', duration_minutes: 30, specialty_id: UUID(20), specialty_name: 'Allgemeinmedizin', is_other: false };
const other = { id: UUID(31), name: 'Sonstiges', duration_minutes: 60, specialty_id: null, specialty_name: null, is_other: true };

describe('patient: book and cancel (UF-05, UF-06, UF-07)', () => {
  let backend, app, b;
  beforeEach(async () => {
    backend = new FakeBackend();
    app = await startApp(backend);
    b = await sessionBrowser(app, loginAs(backend, { roles: ['patient'] }));
    backend.on('GET /v1/services', { status: 200, body: { items: [service, other] } });
  });
  afterEach(() => app.close());

  it('lists the services and tells that "Sonstiges" gets a doctor later', async () => {
    const r = await b.get('/book');
    assert.match(r.text, /Kontrolle/);
    assert.match(r.text, /30 Minuten · Allgemeinmedizin/);
    assert.match(r.text, /Die Praxis ordnet Ihrem Termin eine Ärztin oder einen Arzt zu/);
    assert.match(r.text, new RegExp(`href="/book/${service.id}"`));
  });

  it('asks for a week in UTC from midnight of the practice time zone and groups the times by day', async () => {
    backend.on('GET /v1/calendar/availability', { status: 200, body: { items: [
      { starts_at: '2030-01-07T09:00:00+01:00', ends_at: '2030-01-07T09:30:00+01:00' },
      { starts_at: '2030-01-07T09:15:00+01:00', ends_at: '2030-01-07T09:45:00+01:00' },
      { starts_at: '2030-01-08T10:00:00+01:00', ends_at: '2030-01-08T10:30:00+01:00' },
    ] } });
    const r = await b.get(`/book/${service.id}?from=2030-01-07`);
    const q = backend.called('GET /v1/calendar/availability')[0].query;
    assert.equal(q.service_id, service.id);
    assert.equal(q.from, '2030-01-06T23:00:00.000Z', '00:00 in Berlin in winter is 23:00 UTC');
    assert.equal(q.to, '2030-01-13T23:00:00.000Z', 'exactly seven days later');
    assert.match(r.text, /Mo\. 07\.01\./);
    assert.match(r.text, /Di\. 08\.01\./);
    assert.equal((r.text.match(/class="day[ "]/g) || []).length, 7, 'the calendar shows all seven days');
    assert.match(r.text, /class="day off"/, 'Sunday is greyed out');
    assert.match(r.text, /name="starts_at" value="2030-01-07T09:15:00\+01:00"/);
    assert.match(r.text, />09:00 Uhr</);
    assert.doesNotMatch(r.text, /doctor_id/, 'no doctor information for a patient');
  });

  it('summer time: the week boundaries follow the time zone', async () => {
    backend.on('GET /v1/calendar/availability', { status: 200, body: { items: [] } });
    await b.get(`/book/${service.id}?from=2030-07-01`);
    assert.equal(backend.called('GET /v1/calendar/availability')[0].query.from, '2030-06-30T22:00:00.000Z');
    assert.equal(fmt.startOfDay('2030-03-31').toISOString(), '2030-03-30T23:00:00.000Z');
    assert.equal(fmt.startOfDay('2030-04-01').toISOString(), '2030-03-31T22:00:00.000Z', 'the day after the change to summer time');
    assert.equal(fmt.startOfDay('2030-02-30'), null);
    assert.equal(fmt.startOfDay("2030-01-01'; drop"), null);
  });

  it('never starts in the past and handles a week without times', async () => {
    backend.on('GET /v1/calendar/availability', { status: 200, body: { items: [] } });
    const r = await b.get(`/book/${service.id}?from=2001-01-01`);
    const q = backend.called('GET /v1/calendar/availability')[0].query;
    assert.ok(new Date(q.from) > new Date('2020-01-01'));
    assert.match(r.text, /keine Termine frei/);
    assert.match(r.text, /<span class="off" aria-disabled="true">← Vorige Woche<\/span>/, 'previous week is shown but disabled, not hidden');
  });

  it('books with the chosen time, the reason and the note', async () => {
    backend.on('GET /v1/calendar/availability', { status: 200, body: { items: [] } });
    backend.on('POST /v1/bookings', { status: 201, body: booking() });
    await b.open(`/book/${service.id}`);
    const r = await b.post(`/book/${service.id}`, { starts_at: '2030-01-07T09:15:00+01:00', reason: 'Husten', info: 'seit drei Tagen' });
    assert.equal(r.location, '/appointments?m=booked');
    assert.deepEqual(backend.called('POST /v1/bookings')[0].body, { service_id: service.id, starts_at: '2030-01-07T09:15:00+01:00', reason: 'Husten', info: 'seit drei Tagen' });
  });

  it('never sends a patient or doctor id: the identity is the token', async () => {
    backend.on('POST /v1/bookings', { status: 201, body: booking() });
    await b.open('/book');
    await b.post(`/book/${service.id}`, { starts_at: '2030-01-07T09:15:00+01:00', patient_id: UUID(66), doctor_id: UUID(77) });
    const sent = backend.called('POST /v1/bookings')[0].body;
    assert.equal(sent.patient_id, undefined);
    assert.equal(sent.doctor_id, undefined);
  });

  it('a taken time shows a message and the fresh list', async () => {
    backend.on('GET /v1/calendar/availability', { status: 200, body: { items: [{ starts_at: '2030-01-07T10:00:00+01:00', ends_at: '2030-01-07T10:30:00+01:00' }] } });
    backend.on('POST /v1/bookings', { status: 409, body: { code: 'slot_taken' } });
    await b.open(`/book/${service.id}?from=2030-01-07`);
    const r = await b.post(`/book/${service.id}`, { starts_at: '2030-01-07T09:00:00+01:00', from: '2030-01-07' });
    assert.equal(r.status, 409);
    assert.match(r.text, /Dieser Termin wurde gerade vergeben/);
    assert.match(r.text, /value="2030-01-07T10:00:00\+01:00"/);
  });

  it('refuses a malformed time or service id before any backend call', async () => {
    backend.on('GET /v1/calendar/availability', { status: 200, body: { items: [] } });
    await b.open('/book');
    for (const t of ['', 'morgen', '2030-01-07', '2030-01-07T09:00:00', '2030-01-07T09:00:00Z; drop']) {
      assert.equal((await b.post(`/book/${service.id}`, { starts_at: t })).status, 422, t);
    }
    assert.equal(backend.called('POST /v1/bookings').length, 0);
    for (const id of ['abc', '../services', '%2e%2e', `${UUID(1)}/cancel`]) {
      assert.equal((await b.get(`/book/${encodeURIComponent(id)}`)).status, 404, id);
    }
    assert.equal(backend.calls.filter((c) => c.path.includes('..') || c.path.includes('abc')).length, 0, 'the path of a backend call is never built from unchecked input');
  });

  it('lists upcoming and past appointments and cancels one', async () => {
    backend.on('GET /v1/bookings', ({ query }) => ({ status: 200, body: query.when === 'upcoming' ? { items: [booking({ id: UUID(101), doctor: null })], total: 1 } : { items: [booking({ id: UUID(102), status: 'abgeschlossen' })], total: 1 } }));
    backend.on(`POST /v1/bookings/${UUID(101)}/cancel`, { status: 204 });
    const r = await b.open('/appointments');
    assert.match(r.text, /Kommende Termine/);
    assert.match(r.text, /wird zugeordnet/);
    assert.match(r.text, /abgeschlossen/);
    const c = await b.post(`/appointments/${UUID(101)}/cancel`, {});
    assert.equal(c.location, '/appointments?m=cancelled');
  });

  it('a booking of somebody else is a 404 page, not an error', async () => {
    backend.on(`GET /v1/bookings/${UUID(103)}`, { status: 404, body: { code: 'not_found' } });
    const r = await b.get(`/bookings/${UUID(103)}`);
    assert.equal(r.status, 404);
    assert.match(r.text, /Der Eintrag wurde nicht gefunden/);
  });

  it('shows details with reason and note and offers cancelling of future appointments only', async () => {
    backend.on(`GET /v1/bookings/${UUID(104)}`, { status: 200, body: booking({ id: UUID(104), starts_at: '2099-01-02T09:00:00+01:00', ends_at: '2099-01-02T09:30:00+01:00', info: 'Notiz' }) });
    const f = await b.get(`/bookings/${UUID(104)}`);
    assert.match(f.text, /Husten/);
    assert.match(f.text, /Notiz/);
    assert.match(f.text, /Termin stornieren/);
    assert.doesNotMatch(f.text, /Ärztin \/ Arzt zuordnen/, 'patients get no reception tools');
    backend.on(`GET /v1/bookings/${UUID(105)}`, { status: 200, body: booking({ id: UUID(105), starts_at: '2020-01-02T09:00:00+01:00', ends_at: '2020-01-02T09:30:00+01:00' }) });
    assert.doesNotMatch((await b.get(`/bookings/${UUID(105)}`)).text, /Termin stornieren/);
  });

  it('a patient does not get the pages of other roles', async () => {
    for (const path of ['/staff/bookings', '/staff/patients', '/practice', '/admin/users', '/admin/audit', '/admin/retention', '/calendar']) {
      assert.equal((await b.get(path)).status, 403, path);
    }
    const before = backend.calls.length;
    assert.equal((await b.get('/practice')).status, 403);
    assert.equal(backend.calls.length - before, 1, 'only the profile lookup, nothing about the practice is loaded');
  });
});

describe('doctor: the own calendar (UF-11)', () => {
  it('shows the week with patient, reason and links to the details', async () => {
    const backend = new FakeBackend();
    const app = await startApp(backend);
    const b = await sessionBrowser(app, loginAs(backend, { roles: ['doctor'] }));
    backend.on('GET /v1/bookings', { status: 200, body: { total: 2, items: [
      booking({ id: UUID(1), patient: { id: UUID(2), first_name: 'Paula', last_name: 'Patient' } }),
      booking({ id: UUID(3), starts_at: '2030-01-03T11:00:00+01:00', ends_at: '2030-01-03T11:30:00+01:00', patient: { id: UUID(2), first_name: 'Paula', last_name: 'Patient' }, reason: 'Kontrolle' }),
    ] } });
    const r = await b.get('/calendar?from=2030-01-01');
    const q = backend.called('GET /v1/bookings')[0].query;
    assert.equal(q.from, '2029-12-31T23:00:00.000Z');
    assert.equal(q.to, '2030-01-07T23:00:00.000Z');
    assert.match(r.text, /Mittwoch, 02\.01\.2030/);
    assert.match(r.text, /Donnerstag, 03\.01\.2030/);
    assert.match(r.text, /Paula Patient/);
    assert.match(r.text, new RegExp(`href="/bookings/${UUID(1)}"`));
    assert.match(r.text, /Vorige Woche/, 'a doctor may look at past weeks');
    await app.close();
  });
});
