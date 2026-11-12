import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, it } from 'node:test';
import { booking, FakeBackend, loginAs, sessionBrowser, startApp, UUID } from '../helpers.js';

const doctors = [
  { id: UUID(10), first_name: 'Dora', last_name: 'Doktor', specialties: [{ id: UUID(20), name: 'Allgemeinmedizin' }] },
  { id: UUID(11), first_name: 'Dirk', last_name: 'Doc', specialties: [] },
];
const patient = { id: UUID(40), first_name: 'Paula', last_name: 'Patient', email: 'paula@example.org', phone: '0621 1', main_doctor_id: null };

describe('reception and organisation (UF-06, UF-08)', () => {
  let backend, app, b;
  beforeEach(async () => {
    backend = new FakeBackend();
    app = await startApp(backend);
    b = await sessionBrowser(app, loginAs(backend, { roles: ['desk'] }));
    backend.on('GET /v1/doctors', { status: 200, body: { items: doctors } });
  });
  afterEach(() => app.close());

  it('lists upcoming bookings and the ones without a doctor', async () => {
    backend.on('GET /v1/bookings', ({ query }) => ({ status: 200, body: { total: 1, items: [booking({ id: UUID(1), doctor: query.unassigned ? null : booking().doctor, patient })] } }));
    const all = await b.get('/staff/bookings');
    assert.deepEqual(backend.called('GET /v1/bookings')[0].query, { limit: 25, offset: 0, patient_id: undefined, doctor_id: undefined, when: 'upcoming' });
    assert.match(all.text, /Paula Patient/);
    const open = await b.get('/staff/bookings?view=open');
    assert.equal(backend.called('GET /v1/bookings')[1].query.unassigned, true);
    assert.match(open.text, /Zuordnung offen/);
  });

  it('ignores filter values that are not what they should be', async () => {
    backend.on('GET /v1/bookings', { status: 200, body: { total: 0, items: [] } });
    await b.get(`/staff/bookings?view=x&patient_id=${encodeURIComponent("1' or '1'='1")}&doctor_id=../x&offset=-5`);
    assert.deepEqual(backend.called('GET /v1/bookings')[0].query, { limit: 25, offset: 0, patient_id: undefined, doctor_id: undefined, when: 'upcoming' });
  });

  it('searches patients (at least two characters) and sets the main doctor', async () => {
    backend.on('GET /v1/patients', { status: 200, body: { total: 1, items: [patient] } });
    backend.on(`PUT /v1/patients/${UUID(40)}/main-doctor`, { status: 200, body: patient });
    const r = await b.open('/staff/patients?q=Paula');
    assert.deepEqual(backend.called('GET /v1/patients')[0].query, { q: 'Paula', limit: 25, offset: 0 });
    assert.match(r.text, /paula@example.org/);
    assert.match(r.text, new RegExp(`href="/staff/bookings\\?view=all&amp;patient_id=${UUID(40)}&amp;name=Paula%20Patient"`));
    assert.equal((await b.get('/staff/patients?q=P')).status, 422);
    const s = await b.post(`/staff/patients/${UUID(40)}/main-doctor`, { doctor_id: UUID(10), q: 'Paula' });
    assert.match(s.location, /^\/staff\/patients\?q=Paula&m=main_doctor_saved/);
    assert.deepEqual(backend.called(`PUT /v1/patients/${UUID(40)}/main-doctor`)[0].body, { doctor_id: UUID(10) });
    await b.post(`/staff/patients/${UUID(40)}/main-doctor`, { doctor_id: '', q: 'Paula' });
    assert.deepEqual(backend.called(`PUT /v1/patients/${UUID(40)}/main-doctor`)[1].body, { doctor_id: null });
    assert.equal((await b.post(`/staff/patients/${UUID(40)}/main-doctor`, { doctor_id: 'x' })).status, 422);
  });

  it('books for a patient: the times come with the doctor, the booking names the patient and the doctor', async () => {
    backend.on('GET /v1/services', { status: 200, body: { items: [{ id: UUID(30), name: 'Kontrolle', duration_minutes: 30, specialty_name: 'Allgemeinmedizin', is_other: false }] } });
    backend.on('GET /v1/calendar/availability', { status: 200, body: { items: [{ starts_at: '2030-01-07T09:00:00+01:00', ends_at: '2030-01-07T09:30:00+01:00', doctor_id: UUID(10) }] } });
    backend.on('POST /v1/bookings', { status: 201, body: booking() });
    const page = await b.get(`/staff/book/${UUID(40)}/${UUID(30)}?from=2030-01-07&name=Paula%20Patient`);
    const q = backend.called('GET /v1/calendar/availability')[0].query;
    assert.equal(q.patient_id, UUID(40), 'the main doctor rule is applied for this patient');
    assert.match(page.text, /Dora Doktor/);
    assert.match(page.text, /für Paula Patient/);
    assert.match(page.text, new RegExp(`name="slot" value="2030-01-07T09:00:00\\+01:00\\|${UUID(10)}"`));
    b.token = page.text.match(/name="_csrf" value="([^"]+)"/)[1];
    const r = await b.post(`/staff/book/${UUID(40)}/${UUID(30)}`, { slot: `2030-01-07T09:00:00+01:00|${UUID(10)}`, reason: 'Kontrolle', name: 'Paula Patient' });
    assert.equal(r.location, '/staff/bookings?m=booked');
    assert.deepEqual(backend.called('POST /v1/bookings')[0].body, { patient_id: UUID(40), service_id: UUID(30), starts_at: '2030-01-07T09:00:00+01:00', doctor_id: UUID(10), reason: 'Kontrolle' });
  });

  it('shows the booking with the reception tools; assigning a doctor, editing, rescheduling', async () => {
    const id = UUID(100);
    backend.on(`GET /v1/bookings/${id}`, { status: 200, body: booking({ id, doctor: null, patient, info: 'Notiz' }) });
    const page = await b.open(`/bookings/${id}`);
    assert.match(page.text, /noch nicht zugeordnet/);
    assert.match(page.text, /paula@example.org/);
    assert.match(page.text, /Ärztin \/ Arzt zuordnen/);
    assert.match(page.text, /Dirk Doc/);

    backend.on(`PUT /v1/bookings/${id}/doctor`, { status: 200, body: booking({ id }) });
    assert.equal((await b.post(`/bookings/${id}/assign`, { doctor_id: UUID(10) })).location, `/bookings/${id}?m=assigned`);
    assert.deepEqual(backend.called(`PUT /v1/bookings/${id}/doctor`)[0].body, { doctor_id: UUID(10) });
    assert.equal((await b.post(`/bookings/${id}/assign`, { doctor_id: '' })).status, 422);

    backend.on(`PUT /v1/bookings/${id}/doctor`, { status: 409, body: { code: 'slot_taken' } });
    const busy = await b.post(`/bookings/${id}/assign`, { doctor_id: UUID(11) });
    assert.equal(busy.status, 409);
    assert.match(busy.text, /Dieser Termin wurde gerade vergeben/);

    backend.on(`PATCH /v1/bookings/${id}`, { status: 200, body: booking({ id }) });
    assert.equal((await b.post(`/bookings/${id}/edit`, { reason: 'Neu', info: '' })).location, `/bookings/${id}?m=saved`);
    assert.deepEqual(backend.called(`PATCH /v1/bookings/${id}`)[0].body, { reason: 'Neu', info: '' });
  });

  it('reschedules: only the times of the booked doctor are offered', async () => {
    const id = UUID(100);
    backend.on(`GET /v1/bookings/${id}`, { status: 200, body: booking({ id, patient }) });
    backend.on('GET /v1/calendar/availability', { status: 200, body: { items: [
      { starts_at: '2030-01-07T09:00:00+01:00', ends_at: '2030-01-07T09:30:00+01:00', doctor_id: UUID(10) },
      { starts_at: '2030-01-07T10:00:00+01:00', ends_at: '2030-01-07T10:30:00+01:00', doctor_id: UUID(11) },
    ] } });
    const page = await b.open(`/bookings/${id}/reschedule?from=2030-01-07`);
    assert.match(page.text, /09:00 Uhr/);
    assert.doesNotMatch(page.text, /10:00 Uhr/);
    backend.on(`PATCH /v1/bookings/${id}`, { status: 200, body: booking({ id }) });
    assert.equal((await b.post(`/bookings/${id}/reschedule`, { starts_at: '2030-01-07T09:00:00+01:00' })).location, `/bookings/${id}?m=rescheduled`);
    assert.deepEqual(backend.called(`PATCH /v1/bookings/${id}`)[0].body, { starts_at: '2030-01-07T09:00:00+01:00' });
    backend.on(`PATCH /v1/bookings/${id}`, { status: 409, body: { code: 'outside_working_time' } });
    const r = await b.post(`/bookings/${id}/reschedule`, { starts_at: '2030-01-07T18:00:00+01:00' });
    assert.equal(r.status, 409);
    assert.match(r.text, /arbeitet zu dieser Zeit nicht/);
  });

  it('reception does not get the practice or the administration pages', async () => {
    for (const path of ['/practice', '/practice/doctors/' + UUID(10), '/admin/users', '/book', '/appointments', '/calendar']) {
      assert.equal((await b.get(path)).status, 403, path);
    }
  });
});

describe('organisation: practice setup (UF-09)', () => {
  let backend, app, b;
  beforeEach(async () => {
    backend = new FakeBackend();
    app = await startApp(backend);
    b = await sessionBrowser(app, loginAs(backend, { roles: ['organisator'] }));
    backend.on('GET /v1/specialties', { status: 200, body: { items: [{ id: UUID(20), name: 'Allgemeinmedizin' }] } });
    backend.on('GET /v1/services', { status: 200, body: { items: [{ id: UUID(30), name: 'Kontrolle', duration_minutes: 30, specialty_id: UUID(20), specialty_name: 'Allgemeinmedizin', is_other: false }, { id: UUID(31), name: 'Sonstiges', duration_minutes: 60, is_other: true }] } });
    backend.on('GET /v1/doctors', { status: 200, body: { items: doctors } });
  });
  afterEach(() => app.close());

  it('creates and deletes a specialty and shows conflicts', async () => {
    backend.on('POST /v1/specialties', { status: 201, body: {} });
    await b.open('/practice');
    assert.equal((await b.post('/practice/specialties', { name: 'Orthopädie' }, { page: '/practice' })).location, '/practice?m=created');
    backend.on('POST /v1/specialties', { status: 409, body: { code: 'duplicate' } });
    const dup = await b.post('/practice/specialties', { name: 'Orthopädie' });
    assert.equal(dup.status, 409);
    assert.match(dup.text, /existiert bereits/);
    backend.on(`DELETE /v1/specialties/${UUID(20)}`, { status: 409, body: { code: 'in_use' } });
    const del = await b.post(`/practice/specialties/${UUID(20)}/delete`, {});
    assert.equal(del.status, 409);
    assert.match(del.text, /noch verwendet/);
  });

  it('creates, edits and deletes services; "Sonstiges" cannot be deleted from the page', async () => {
    const page = await b.open('/practice');
    assert.doesNotMatch(page.text, new RegExp(`/practice/services/${UUID(31)}/delete`));
    assert.match(page.text, new RegExp(`/practice/services/${UUID(30)}/delete`));
    backend.on('POST /v1/services', { status: 201, body: {} });
    await b.post('/practice/services', { name: 'Impfung', duration_minutes: '15', specialty_id: UUID(20) });
    assert.deepEqual(backend.called('POST /v1/services')[0].body, { name: 'Impfung', duration_minutes: 15, specialty_id: UUID(20) });
    backend.on(`PUT /v1/services/${UUID(30)}`, { status: 200, body: {} });
    const edit = await b.open(`/practice/services/${UUID(30)}`);
    assert.match(edit.text, /value="Kontrolle"/);
    assert.equal((await b.post(`/practice/services/${UUID(30)}`, { name: 'Kontrolle', duration_minutes: '45', specialty_id: UUID(20) })).location, '/practice?m=saved');
    backend.on('POST /v1/services', { status: 422, body: { code: 'validation_failed', errors: [{ field: 'duration_minutes', code: 'minimum' }] } });
    const bad = await b.post('/practice/services', { name: 'x', duration_minutes: '1', specialty_id: UUID(20) });
    assert.equal(bad.status, 422);
    assert.match(bad.text, /Die Dauer ist zu klein/);
  });

  it('imports a JSON template: preview first, syntax errors are caught', async () => {
    backend.on('POST /v1/services/import', { status: 200, body: { dry_run: true, specialties_created: 1, services_created: 2, services_updated: 0, warnings: [] } });
    await b.open('/practice');
    const text = JSON.stringify({ specialties: [{ name: 'Neu' }], services: [{ name: 'A', duration_minutes: 20, specialty: 'Neu' }] });
    const r = await b.post('/practice/import', { json: text, dry_run: 'yes' });
    assert.equal(r.status, 200);
    assert.match(r.text, /Vorschau: Es wurde nichts gespeichert/);
    assert.equal(backend.called('POST /v1/services/import')[0].query.dry_run, 'true');
    assert.deepEqual(backend.called('POST /v1/services/import')[0].body, JSON.parse(text));
    await b.post('/practice/import', { json: text });
    assert.equal(backend.called('POST /v1/services/import')[1].query.dry_run, 'false');
    const broken = await b.post('/practice/import', { json: '{"specialties": [' });
    assert.equal(broken.status, 422);
    assert.match(broken.text, /kein gültiges JSON/);
    assert.equal(backend.called('POST /v1/services/import').length, 2);
  });

  it('edits the working times of a doctor and warns about bookings outside them', async () => {
    backend.on(`GET /v1/doctors/${UUID(10)}/schedule`, { status: 200, body: { specialty_ids: [UUID(20)], working_days: [{ weekday: 1, start_time: '08:00', end_time: '12:00' }, { weekday: 1, start_time: '13:00', end_time: '16:00' }] } });
    const page = await b.open(`/practice/doctors/${UUID(10)}`);
    assert.match(page.text, /name="mon_from1" value="08:00"/);
    assert.match(page.text, /name="mon_to2" value="16:00"/);
    assert.match(page.text, new RegExp(`value="${UUID(20)}" checked`));
    backend.on(`PUT /v1/doctors/${UUID(10)}/schedule`, { status: 200, body: { schedule: {}, bookings_outside_hours: [UUID(1), UUID(2)] } });
    const r = await b.post(`/practice/doctors/${UUID(10)}`, { specialty: [UUID(20)], mon_from1: '08:00', mon_to1: '12:00', tue_from1: '09:00', tue_to1: '17:30' });
    assert.match(r.text, /2 bereits gebuchte Termine liegen nun außerhalb/);
    assert.deepEqual(backend.called(`PUT /v1/doctors/${UUID(10)}/schedule`)[0].body, {
      specialty_ids: [UUID(20)], working_days: [{ weekday: 1, start_time: '08:00', end_time: '12:00' }, { weekday: 2, start_time: '09:00', end_time: '17:30' }],
    });
    const half = await b.post(`/practice/doctors/${UUID(10)}`, { specialty: [UUID(20)], mon_from1: '08:00' });
    assert.equal(half.status, 422);
    assert.match(half.text, /Montag: Bitte Beginn und Ende angeben/);
    backend.on(`PUT /v1/doctors/${UUID(10)}/schedule`, { status: 422, body: { code: 'working_days_overlap' } });
    const overlap = await b.post(`/practice/doctors/${UUID(10)}`, { mon_from1: '08:00', mon_to1: '12:00', mon_from2: '11:00', mon_to2: '14:00' });
    assert.equal(overlap.status, 422);
    assert.match(overlap.text, /dürfen sich nicht überschneiden/);
    assert.match(overlap.text, /name="mon_from2" value="11:00"/, 'the input is kept');
  });
});

describe('system administration (UF-10, UF-13)', () => {
  let backend, app, b;
  const u = { id: UUID(50), first_name: 'Udo', last_name: 'User', email: 'udo@example.org', roles: ['desk'], status: 'active', permanently_locked: false, mfa_enabled: true, created_at: '2030-01-01T00:00:00Z', locked_until: null };
  beforeEach(async () => {
    backend = new FakeBackend();
    app = await startApp(backend);
    b = await sessionBrowser(app, loginAs(backend, { roles: ['systemadmin'], me: { id: UUID(1) } }));
  });
  afterEach(() => app.close());

  it('lists users with filters and creates a user', async () => {
    backend.on('GET /v1/admin/users', { status: 200, body: { items: [u], total: 1 } });
    const r = await b.open('/admin/users?q=udo&role=desk&status=active');
    assert.deepEqual(backend.called('GET /v1/admin/users')[0].query, { q: 'udo', role: 'desk', status: 'active', limit: 25, offset: 0 });
    assert.match(r.text, /Udo User/);
    backend.on('POST /v1/admin/users', { status: 201, body: { ...u, id: UUID(51) } });
    const c = await b.post('/admin/users', { first_name: 'Neu', last_name: 'Nutzer', email: 'neu@example.org', roles: ['doctor', 'patient', 'hacker'] });
    assert.equal(c.location, `/admin/users/${UUID(51)}?m=user_created`);
    assert.deepEqual(backend.called('POST /v1/admin/users')[0].body, { first_name: 'Neu', last_name: 'Nutzer', email: 'neu@example.org', roles: ['doctor', 'patient'] });
    backend.on('POST /v1/admin/users', { status: 409, body: { code: 'email_taken' } });
    const dup = await b.post('/admin/users', { first_name: 'A', last_name: 'B', email: 'udo@example.org', roles: ['doctor'] });
    assert.equal(dup.status, 409);
    assert.match(dup.text, /bereits verwendet/);
  });

  it('changes roles, resets the password, unlocks, deletes', async () => {
    backend.on(`GET /v1/admin/users/${u.id}`, { status: 200, body: { ...u, permanently_locked: true, status: 'locked' } });
    const page = await b.open(`/admin/users/${u.id}`);
    assert.match(page.text, /dauerhaft gesperrt/);
    assert.match(page.text, /Konto freischalten/);
    backend.on(`PUT /v1/admin/users/${u.id}/roles`, { status: 200, body: u });
    assert.equal((await b.post(`/admin/users/${u.id}/roles`, { roles: ['doctor'] })).location, `/admin/users/${u.id}?m=roles_saved`);
    assert.deepEqual(backend.called(`PUT /v1/admin/users/${u.id}/roles`)[0].body, { roles: ['doctor'] });
    backend.on(`PUT /v1/admin/users/${u.id}/roles`, { status: 422, body: { code: 'systemadmin_exclusive' } });
    const ex = await b.post(`/admin/users/${u.id}/roles`, { roles: ['systemadmin', 'doctor'] });
    assert.equal(ex.status, 422);
    assert.match(ex.text, /kann nicht mit anderen Rollen kombiniert/);

    backend.on(`POST /v1/admin/users/${u.id}/password-reset`, { status: 202 });
    assert.equal((await b.post(`/admin/users/${u.id}/password-reset`, {})).location, `/admin/users/${u.id}?m=reset_requested`);
    backend.on(`POST /v1/admin/users/${u.id}/unlock`, { status: 204 });
    assert.equal((await b.post(`/admin/users/${u.id}/unlock`, {})).location, `/admin/users/${u.id}?m=unlocked`);

    assert.equal((await b.post(`/admin/users/${u.id}/delete`, {})).status, 422);
    backend.on(`DELETE /v1/admin/users/${u.id}`, { status: 204 });
    assert.equal((await b.post(`/admin/users/${u.id}/delete`, { confirm: 'yes' })).location, '/admin/users?m=deleted_entry');
  });

  it('shows no role or delete controls for the own account', async () => {
    backend.on(`GET /v1/admin/users/${UUID(1)}`, { status: 200, body: { ...u, id: UUID(1) } });
    const r = await b.get(`/admin/users/${UUID(1)}`);
    assert.match(r.text, /Ihre eigenen Rollen können Sie nicht ändern/);
    assert.doesNotMatch(r.text, /Konto löschen/);
  });

  it('shows the page links when there are more results than one page', async () => {
    backend.on('GET /v1/admin/users', { status: 200, body: { items: [u], total: 60 } });
    const first = await b.open('/admin/users');
    assert.equal(first.status, 200);
    assert.match(first.text, /1–25 von 60/);
    assert.match(first.text, /offset=25/);
    const last = await b.open('/admin/users?offset=50');
    assert.match(last.text, /51–60 von 60/);
    assert.match(last.text, /offset=25/);
  });

  it('reads the audit log and edits the retention periods', async () => {
    backend.on('GET /v1/admin/audit-log', { status: 200, body: { total: 1, items: [{ id: 1, occurred_at: '2030-01-01T10:00:00Z', action: 'purge', actor_user_id: null, target_user_id: UUID(50), data_category: 'bookings_cancelled', affected_count: 3 }] } });
    const audit = await b.get('/admin/audit?action=purge');
    assert.equal(backend.called('GET /v1/admin/audit-log')[0].query.action, 'purge');
    assert.match(audit.text, /bookings_cancelled/);
    assert.match(audit.text, /System/);
    const v = { retention_a_days: 30, retention_b_days: 365, retention_c_days: 730, retention_d_days: 30, retention_e_days: 90 };
    backend.on('GET /v1/admin/retention', { status: 200, body: v });
    const page = await b.open('/admin/retention');
    assert.match(page.text, /name="retention_c_days"[^>]*value="730"|value="730"[^>]*name="retention_c_days"/);
    backend.on('PUT /v1/admin/retention', { status: 200, body: v });
    assert.equal((await b.post('/admin/retention', Object.fromEntries(Object.entries(v).map(([k, x]) => [k, String(x)])))).location, '/admin/retention?m=saved');
    assert.deepEqual(backend.called('PUT /v1/admin/retention')[0].body, v);
    backend.on('PUT /v1/admin/retention', { status: 422, body: { code: 'validation_failed', errors: [{ field: 'retention_c_days', code: 'c_before_b' }] } });
    const bad = await b.post('/admin/retention', { ...Object.fromEntries(Object.entries(v).map(([k, x]) => [k, String(x)])), retention_c_days: '10' });
    assert.equal(bad.status, 422);
    assert.match(bad.text, /Frist C darf nicht kürzer sein als Frist B|darf nicht kürzer sein als Frist B/);
  });

  it('the system administrator gets no appointment pages', async () => {
    for (const path of ['/staff/bookings', '/staff/patients', '/book', '/appointments', '/calendar', '/practice']) {
      assert.equal((await b.get(path)).status, 403, path);
    }
  });
});
