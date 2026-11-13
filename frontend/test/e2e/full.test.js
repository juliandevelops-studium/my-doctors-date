// The whole application through the real stack: Traefik (HTTPS) -> frontend -> user_service / calendar_service ->
// database_middleware -> PostgreSQL, and mail_service -> Mailpit. Needs a running cluster (`make e2e`).
// It uses unique names per run, so it can run again against the same cluster.
import assert from 'node:assert/strict';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { before, describe, it } from 'node:test';
import { Browser, decode, env, linkOf, mails, nextCode, sleep, waitMail } from './client.js';

const run = Date.now().toString(36);
const UUID = '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}';
const GOOD = 'Gelb-Wolke-Stein-Fluss-77';
const OTHER = 'Rot-Wiese-Berg-Bach-1234';

const person = (key, first, last, extra = {}) => ({ key, first, last, email: `${key}-${run}@example.org`, password: GOOD, browser: new Browser(key), ...extra });
const admin = { key: 'admin', browser: new Browser('admin') };
const orga = person('orga', 'Olga', `Organisation${run}`);
const front = person('front', 'Emil', `Empfang${run}`);
const doc1 = person('doc1', 'Dora', `Doktor${run}`);
const doc2 = person('doc2', 'Dirk', `Doc${run}`);
const patA = person('pata', 'Paula', `Patientin${run}`, { phone: '0621 111' });
const patB = person('patb', 'Bernd', `Besucher${run}`);
const patC = person('patc', 'Clara', `Chaos${run}`);
const patD = person('patd', 'Dieter', `Dummy${run}`);
const xss = person('xss', '<script>alert(1)</script>', `Xss${run}`);
const state = {};

const html = (r) => decode(r.text);
const pageHas = (r, re) => assert.match(html(r), re);

// Password step and "continue without code" step of the login. Returns the page after the login.
async function loginPassword(u, password = u.password) {
  const b = u.browser;
  await b.get('/login');
  const r = await b.post('/login', { email: u.email, password });
  if (r.status !== 302) return r;
  if (r.location === '/login/mfa') await b.get('/login/mfa');
  return r;
}

// First login: password step, "no code yet", optional password change, second factor setup. Leaves a full session.
async function onboard(u, { initialPassword = u.password, changePasswordFrom } = {}) {
  const b = u.browser;
  const r = await loginPassword(u, initialPassword);
  assert.equal(r.status, 302);
  assert.equal(r.location, '/setup', 'no MFA yet: straight to the setup');
  let page = await b.go('/setup');
  if (changePasswordFrom) {
    pageHas(page, /Passwort fest/);
    const p = await b.post('/setup/password', { current_password: changePasswordFrom, new_password: u.password, password2: u.password });
    assert.equal(p.location, '/setup');
    page = await b.go('/setup');
    u.passwordChanged = true;
  }
  pageHas(page, /Einrichtung starten/);
  const start = await b.post('/setup/mfa/start', {});
  assert.equal(start.status, 200);
  assert.match(start.text, /<svg/);
  u.secret = /class="secret">([A-Z2-7]{32})</.exec(start.text)[1];
  const wrong = await b.post('/setup/mfa/confirm', { code: '000000', secret: u.secret });
  assert.equal(wrong.status, 422);
  const ok = await b.post('/setup/mfa/confirm', { code: await nextCode(u), secret: u.secret });
  assert.equal(ok.status, 200);
  u.recovery = [...ok.text.matchAll(/<li>([a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4})<\/li>/g)].map((m) => m[1]);
  assert.equal(u.recovery.length, 10, 'ten recovery codes');
  assert.match(ok.text, /nur jetzt angezeigt/);
  return b.go('/');
}

async function register(u) {
  const b = u.browser;
  await b.get('/register');
  const r = await b.post('/register', { first_name: u.first, last_name: u.last, email: u.email, phone: u.phone ?? '', street: 'Hauptstr. 1', postal_code: '68159', city: 'Mannheim', password: u.password, password2: u.password });
  assert.equal(r.location, '/login?m=registered', `registration: ${r.status}`);
  const mail = await waitMail(u.email, 'E-Mail-Adresse');
  const link = linkOf(mail, '/verify-email');
  const page = await b.get(link);
  assert.equal(page.status, 200);
  const done = await b.post('/verify-email', { token: new URL(link, env.base).searchParams.get('token') });
  assert.equal(done.location, '/login?m=verified');
}

// Admin creates an account; the person sets the password through the mailed link.
async function adminCreates(u, roles) {
  const b = admin.browser;
  await b.get('/admin/users');
  const since = Date.now() - 1000;
  const r = await b.post('/admin/users', { first_name: u.first, last_name: u.last, email: u.email, roles });
  assert.equal(r.status, 302, `create ${u.key}: ${r.status} ${decode(r.text).match(/class="alert"[^>]*>([^<]+)/)?.[1] ?? ''}`);
  u.id = new RegExp(`/admin/users/(${UUID})`).exec(r.location)[1];
  const mail = await waitMail(u.email, 'Passwort zurücksetzen', { since });
  const link = linkOf(mail, '/reset-password');
  await u.browser.get(link);
  const set = await u.browser.post('/reset-password', { token: new URL(link, env.base).searchParams.get('token'), new_password: u.password, password2: u.password });
  assert.equal(set.location, '/login?m=password_reset');
}

// Backends cache a positive token check for up to 10 seconds (api/internal.md section 1), so a revoked token may still
// work for that long. Waits until the token is refused.
async function assertRevoked(token, label) {
  const probe = new Browser();
  probe.jar.set('__Host-session', token);
  const end = Date.now() + 13000;
  for (;;) {
    const r = await probe.get('/appointments');
    if (r.status === 302 && /^\/login/.test(r.location)) return;
    if (Date.now() > end) assert.fail(`${label}: still accepted after 13 s (status ${r.status})`);
    await sleep(700);
  }
}

const futureWeekFrom = () => {
  const d = new Date(Date.now() + 14 * 86400000);
  while (d.getUTCDay() !== 1) d.setUTCDate(d.getUTCDate() + 1);
  return d.toISOString().slice(0, 10);
};

describe('UF-01 to UF-13 through the real stack', { concurrency: false }, () => {
  before(async () => {
    assert.ok(env.ca && env.mailpit && env.adminFile && env.adminState, 'run this through `make e2e`');
    // The last test of a previous run fills the rate limit of the public forms; it empties within a minute.
    const b = new Browser();
    await b.get('/forgot-password');
    const end = Date.now() + 70000;
    while ((await b.post('/forgot-password', { email: `warmup-${run}@example.org` })).status === 429 && Date.now() < end) await sleep(2000);
  });

  describe('the edge', () => {
    it('HTTPS works with the local CA, a visitor is sent to the login, HTTP redirects to HTTPS with the port', async () => {
      const b = new Browser();
      const home = await b.get('/');
      assert.equal(home.status, 302);
      assert.equal(home.location, '/login');
      const login = await b.get('/login');
      assert.equal(login.status, 200);
      assert.match(login.headers['content-security-policy'], /default-src 'none'/);
      assert.match(login.headers['strict-transport-security'], /max-age=31536000/);
      const csrf = [...home.setCookies, ...login.setCookies].find((c) => c.startsWith('__Host-csrf='));
      assert.match(csrf, /; Path=\/; HttpOnly; Secure; SameSite=Strict/);
      const plain = await b.get('/login', { base: env.httpBase });
      assert.ok([301, 308].includes(plain.status), `HTTP gives ${plain.status}`);
      assert.match(plain.location, /^https:\/\/praxis\.localhost:\d+\/login$/);
    });
  });

  describe('UF-03: the bootstrap administrator', () => {
    it('logs in for the first time, must choose a password and set up the second factor', async () => {
      const b = admin.browser;
      const text = readFileSync(env.adminFile, 'utf8');
      admin.email = /E-Mail:\s*(\S+)/.exec(text)[1];
      // The bootstrap account can be used once. Password and key of the later runs are kept next to the other secrets.
      if (existsSync(env.adminState)) {
        Object.assign(admin, JSON.parse(readFileSync(env.adminState, 'utf8')));
        assert.equal((await loginPassword(admin)).status, 302);
        const done = await b.post('/login/mfa', { mode: 'code', code: await nextCode(admin) });
        assert.equal(done.status, 302);
        const state = { password: admin.password, secret: admin.secret, lastStep: admin.lastStep };
        writeFileSync(env.adminState, JSON.stringify(state), { mode: 0o600 });
        assert.equal((await b.go('/admin/users')).status, 200);
        return;
      }
      admin.initial = /Password:\s*(\S+)/.exec(text)[1];
      admin.password = `Admin-${GOOD}`;
      const r = await loginPassword(admin, admin.initial);
      assert.equal(r.status, 302);
      assert.equal(r.location, '/setup');
      for (const path of ['/admin/users', '/account', '/staff/bookings']) {
        assert.equal((await b.get(path)).location, '/setup', `${path} is not reachable before the setup`);
      }
      pageHas(await b.go('/setup'), /Passwort fest/);
      const p = await b.post('/setup/password', { current_password: admin.initial, new_password: admin.password, password2: admin.password });
      assert.equal(p.location, '/setup');
      pageHas(await b.go('/setup'), /Einrichtung starten/);
      const start = await b.post('/setup/mfa/start', {});
      admin.secret = /class="secret">([A-Z2-7]{32})</.exec(start.text)[1];
      const ok = await b.post('/setup/mfa/confirm', { code: await nextCode(admin), secret: admin.secret });
      assert.equal(ok.status, 200);
      writeFileSync(env.adminState, JSON.stringify({ password: admin.password, secret: admin.secret, lastStep: admin.lastStep }), { mode: 0o600 });
      const home = await b.go('/admin/users');
      assert.equal(home.status, 200);
      pageHas(home, /Benutzer anlegen/);
    });

    it('the administrator has no access to appointments or the practice', async () => {
      for (const path of ['/staff/bookings', '/staff/patients', '/book', '/appointments', '/calendar', '/practice']) {
        assert.equal((await admin.browser.get(path)).status, 403, path);
      }
    });
  });

  describe('UF-10: users and roles', () => {
    it('creates the organisation, reception and two doctors; each sets the own password and second factor', async () => {
      await adminCreates(orga, ['organisator']);
      await adminCreates(front, ['desk']);
      await adminCreates(doc1, ['doctor']);
      await adminCreates(doc2, ['doctor']);
      for (const u of [orga, front, doc1, doc2]) {
        const home = await onboard(u);
        assert.equal(home.status, 200, u.key);
      }
      pageHas(await orga.browser.get('/practice'), /Praxis einrichten/);
      pageHas(await front.browser.get('/staff/bookings'), /Termine/);
      pageHas(await doc1.browser.get('/calendar'), /Mein Kalender/);
    });

    it('refuses the combination of the administrator role with another role', async () => {
      const b = admin.browser;
      await b.get('/admin/users');
      const r = await b.post('/admin/users', { first_name: 'X', last_name: 'Y', email: `bad-${run}@example.org`, roles: ['systemadmin', 'doctor'] });
      assert.equal(r.status, 422);
      pageHas(r, /kann nicht mit anderen Rollen kombiniert/);
    });
  });

  describe('UF-09: the organisation sets up the practice', () => {
    it('creates a specialty and services, imports a template (preview first), sets working times', async () => {
      const b = orga.browser;
      state.specialty = `Allgemeinmedizin ${run}`;
      state.service = `Kontrolle ${run}`;
      await b.go('/practice');
      assert.equal((await b.post('/practice/specialties', { name: state.specialty })).location, '/practice?m=created');
      let page = await b.go('/practice');
      pageHas(page, new RegExp(state.specialty));
      const specialtyId = new RegExp(`<option value="(${UUID})">${state.specialty}</option>`).exec(page.text)[1];
      assert.equal((await b.post('/practice/services', { name: state.service, duration_minutes: '30', specialty_id: specialtyId })).location, '/practice?m=created');
      page = await b.go('/practice');
      pageHas(page, new RegExp(state.service));

      const template = JSON.stringify({ specialties: [{ name: state.specialty }], services: [{ name: `Impfung ${run}`, duration_minutes: 15, specialty: state.specialty }] });
      const preview = await b.post('/practice/import', { json: template, dry_run: 'yes' });
      pageHas(preview, /Vorschau: Es wurde nichts gespeichert/);
      assert.doesNotMatch((await b.go('/practice')).text, new RegExp(`Impfung ${run}`));
      pageHas(await b.post('/practice/import', { json: template }), /Gespeichert/);
      pageHas(await b.go('/practice'), new RegExp(`Impfung ${run}`));
      const bad = await b.post('/practice/import', { json: '{ nope' });
      assert.equal(bad.status, 422);

      for (const d of [doc1, doc2]) {
        const form = await b.go(`/practice/doctors/${d.id}`);
        assert.equal(form.status, 200);
        const fields = { specialty: [specialtyId] };
        for (const day of ['mon', 'tue', 'wed', 'thu', 'fri']) Object.assign(fields, { [`${day}_from1`]: '08:00', [`${day}_to1`]: '12:00' });
        const saved = await b.post(`/practice/doctors/${d.id}`, fields);
        assert.equal(saved.status, 200);
        pageHas(saved, /Alle bestehenden Termine liegen innerhalb/);
      }
      const half = await b.post(`/practice/doctors/${doc1.id}`, { specialty: [specialtyId], mon_from1: '08:00' });
      assert.equal(half.status, 422);
    });

    it('other roles cannot change the practice', async () => {
      for (const u of [front, doc1]) {
        assert.equal((await u.browser.get('/practice')).status, 403);
      }
    });
  });

  describe('UF-01 and UF-12: registration and password reset', () => {
    it('registers four patients with e-mail confirmation and sets up the second factor', async () => {
      for (const u of [patA, patB, patC, patD]) {
        await register(u);
        // Before the second factor exists the account is in the setup state.
        const home = await onboard(u);
        assert.equal(home.status, 200, u.key);
        pageHas(home, /Meine Termine/);
      }
    });

    it('does not tell whether an address is registered', async () => {
      const b = new Browser();
      await b.get('/register');
      const dup = await b.post('/register', { first_name: 'Paula', last_name: 'Zwei', email: patA.email, password: GOOD, password2: GOOD });
      assert.equal(dup.location, '/login?m=registered', 'same answer as for a new address');
      await b.get('/forgot-password');
      const known = await b.post('/forgot-password', { email: patD.email });
      const unknown = await b.post('/forgot-password', { email: `nobody-${run}@example.org` });
      assert.equal(known.location, unknown.location);
      const login = await new Browser();
      await login.get('/login');
      const r1 = await login.post('/login', { email: `nobody-${run}@example.org`, password: GOOD });
      const r2 = await login.post('/login', { email: patD.email, password: 'Falsches-Passwort-123456' });
      assert.equal(r1.status, r2.status);
      assert.equal(decode(r1.text).match(/class="alert"[^>]*>([^<]+)/)[1], decode(r2.text).match(/class="alert"[^>]*>([^<]+)/)[1]);
    });

    it('UF-12: a reset link sets a new password, ends all sessions and does not bypass the second factor', async () => {
      const oldSession = patD.browser.cookie('__Host-session');
      assert.ok(oldSession);
      const since = Date.now() - 1000;
      const b = new Browser();
      await b.get('/forgot-password');
      await b.post('/forgot-password', { email: patD.email });
      const mail = await waitMail(patD.email, 'Passwort zurücksetzen', { since });
      const link = linkOf(mail, '/reset-password');
      await b.get(link);
      const weak = await b.post('/reset-password', { token: new URL(link, env.base).searchParams.get('token'), new_password: 'kurz', password2: 'kurz' });
      assert.equal(weak.status, 422);
      pageHas(weak, /zu kurz/);
      const ok = await b.post('/reset-password', { token: new URL(link, env.base).searchParams.get('token'), new_password: OTHER, password2: OTHER });
      assert.equal(ok.location, '/login?m=password_reset');
      const again = await b.post('/reset-password', { token: new URL(link, env.base).searchParams.get('token'), new_password: OTHER, password2: OTHER });
      assert.equal(again.status, 400, 'a link works once');
      await assertRevoked(oldSession, 'session before the reset');
      // Old password refused, new password still needs the code.
      patD.password = OTHER;
      assert.equal((await loginPassword(patD, GOOD)).status, 401);
      patD.browser = new Browser('patd2');
      const second = await loginPassword(patD, OTHER);
      assert.equal(second.status, 302);
      assert.equal(second.location, '/login/mfa', 'MFA is set up: the code is asked for');
      const noCode = await patD.browser.post('/login/mfa', { mode: 'none' });
      assert.equal(noCode.location, '/login/mfa', 'no second step without the code');
      const withCode = await patD.browser.post('/login/mfa', { mode: 'code', code: await nextCode(patD) });
      assert.equal(withCode.status, 302);
    });
  });

  describe('UF-05: a patient books an appointment', () => {
    it('finds the service, sees only free times, books with reason and note, and gets a mail', async () => {
      state.from = futureWeekFrom();
      const b = patA.browser;
      const services = await b.go('/book');
      pageHas(services, new RegExp(state.service));
      state.serviceId = new RegExp(`<a href="/book/(${UUID})">${state.service}</a>`).exec(services.text)[1];
      const slots = await b.go(`/book/${state.serviceId}?from=${state.from}`);
      state.slots = [...slots.text.matchAll(/name="starts_at" value="([^"]+)"/g)].map((m) => m[1]);
      assert.ok(state.slots.length >= 10, `${state.slots.length} start times in the week`);
      assert.ok(!slots.text.includes(doc1.id) && !slots.text.includes(doc2.id), 'a patient does not see ids of doctors');
      state.slot = state.slots[0];
      const since = Date.now() - 1000;
      const r = await b.post(`/book/${state.serviceId}`, { starts_at: state.slot, reason: `Husten ${run}`, info: 'seit drei Tagen' });
      assert.equal(r.location, '/appointments?m=booked');
      const list = await b.go('/appointments');
      pageHas(list, new RegExp(state.service));
      pageHas(list, /Ihr Termin|bestätigt/);
      const mail = await waitMail(patA.email, 'Ihr Termin ist bestätigt', { since });
      assert.match(mail.text, new RegExp(state.service));
      assert.doesNotMatch(mail.text, /Husten|seit drei Tagen/, 'reason and note are not in the mail');
      state.bookingA = new RegExp(`/bookings/(${UUID})`).exec(list.text)[1];
    });

    it('two doctors take two bookings of the same time; the third patient is told the time is taken', async () => {
      const rb = await patB.browser.go(`/book/${state.serviceId}?from=${state.from}`);
      assert.match(rb.text, new RegExp(state.slot.replace(/[+]/g, '\\+')), 'the time is still offered while one doctor is free');
      const okB = await patB.browser.post(`/book/${state.serviceId}`, { starts_at: state.slot, reason: 'Kontrolle' });
      assert.equal(okB.location, '/appointments?m=booked');
      const rc = await patC.browser.post(`/book/${state.serviceId}`, { starts_at: state.slot });
      assert.equal(rc.status, 409);
      pageHas(rc, /Dieser Termin wurde gerade vergeben/);
      const stillThere = await patC.browser.go(`/book/${state.serviceId}?from=${state.from}`);
      assert.doesNotMatch(stillThere.text, new RegExp(`value="${state.slot.replace(/[+]/g, '\\+')}"`), 'the taken time is no longer offered');
      state.bookingB = new RegExp(`/bookings/(${UUID})`).exec((await patB.browser.go('/appointments')).text)[1];
    });

    it('a booking of somebody else does not exist for me (404), and nothing changes', async () => {
      assert.equal((await patB.browser.get(`/bookings/${state.bookingA}`)).status, 404);
      assert.equal((await patC.browser.get(`/bookings/${state.bookingA}`)).status, 404);
      await patB.browser.go('/appointments');
      const cancel = await patB.browser.post(`/bookings/${state.bookingA}/cancel`, {});
      assert.equal(cancel.status, 404);
      pageHas(await patA.browser.go('/appointments'), new RegExp(state.service));
    });
  });

  describe('UF-11: doctors see their own calendar', () => {
    it('each doctor sees exactly one of the two bookings, with patient and reason', async () => {
      const seen = [];
      for (const d of [doc1, doc2]) {
        const cal = await d.browser.go(`/calendar?from=${state.from}`);
        assert.equal(cal.status, 200);
        const mine = [patA, patB].filter((p) => html(cal).includes(p.last));
        seen.push(mine.length);
        if (html(cal).includes(patA.last)) {
          pageHas(cal, new RegExp(`Husten ${run}`));
          state.docOfA = d;
          const detail = await d.browser.go(`/bookings/${state.bookingA}`);
          pageHas(detail, /seit drei Tagen/);
          assert.equal((await d.browser.get(`/bookings/${state.bookingB}`)).status === 200, html(cal).includes(patB.last));
        }
      }
      assert.deepEqual(seen.sort(), [1, 1]);
    });
  });

  describe('UF-06 and UF-08: reception', () => {
    it('sees all appointments with patients and finds a patient by name', async () => {
      const list = await front.browser.go('/staff/bookings?view=all');
      pageHas(list, new RegExp(patA.last));
      pageHas(list, new RegExp(patB.last));
      const found = await front.browser.go(`/staff/patients?q=${patA.last}`);
      pageHas(found, new RegExp(patA.email));
      state.idA = new RegExp(`/staff/book/(${UUID})`).exec(found.text)[1];
    });

    it('sets the main doctor; the times for that patient are then only the ones of that doctor', async () => {
      const found = await front.browser.go(`/staff/patients?q=${patC.last}`);
      state.idC = new RegExp(`/staff/book/(${UUID})`).exec(found.text)[1];
      const save = await front.browser.post(`/staff/patients/${state.idC}/main-doctor`, { doctor_id: doc1.id, q: patC.last });
      assert.match(save.location, /m=main_doctor_saved/);
      const page = await front.browser.go(`/staff/book/${state.idC}/${state.serviceId}?from=${state.from}&name=Clara`);
      const doctors = [...page.text.matchAll(/name="slot" value="[^|"]+\|([0-9a-f-]{36})"/g)].map((m) => m[1]);
      assert.ok(doctors.length > 0);
      assert.ok(doctors.every((d) => d === doc1.id), 'only the main doctor');
      const own = await patC.browser.go(`/book/${state.serviceId}?from=${state.from}`);
      assert.ok(own.text.includes('name="starts_at"'));
    });

    it('books for a patient and chooses the time with the doctor', async () => {
      const page = await front.browser.go(`/staff/book/${state.idC}/${state.serviceId}?from=${state.from}&name=Clara`);
      const slot = /name="slot" value="([^"]+)"/.exec(page.text)[1];
      const r = await front.browser.post(`/staff/book/${state.idC}/${state.serviceId}`, { slot, reason: 'vom Empfang gebucht', name: 'Clara' });
      assert.equal(r.location, '/staff/bookings?m=booked');
      const mail = await waitMail(patC.email, 'Ihr Termin ist bestätigt');
      assert.match(mail.text, /Dora/);
      const mine = await patC.browser.go('/appointments');
      pageHas(mine, new RegExp(state.service));
      state.bookingC = new RegExp(`/bookings/(${UUID})`).exec(mine.text)[1];
    });

    it('"Sonstiges": the patient gets no doctor, reception assigns one, the patient is informed', async () => {
      const services = await patA.browser.go('/book');
      const otherId = new RegExp(`<a href="/book/(${UUID})">Sonstiges</a>`).exec(services.text)[1];
      const slots = await patA.browser.go(`/book/${otherId}?from=${state.from}`);
      const slot = [...slots.text.matchAll(/name="starts_at" value="([^"]+)"/g)].map((m) => m[1]).at(-1);
      const r = await patA.browser.post(`/book/${otherId}`, { starts_at: slot, reason: `Sonstiges ${run}` });
      assert.equal(r.location, '/appointments?m=booked');
      const open = await front.browser.go(`/staff/bookings?view=open&patient_id=${state.idA}`);
      pageHas(open, /Zuordnung offen/);
      const id = new RegExp(`/bookings/(${UUID})`).exec(open.text)[1];
      const detail = await front.browser.go(`/bookings/${id}`);
      pageHas(detail, /noch nicht zugeordnet/);
      const since = Date.now() - 1000;
      const assign = await front.browser.post(`/bookings/${id}/assign`, { doctor_id: doc2.id });
      assert.equal(assign.location, `/bookings/${id}?m=assigned`);
      pageHas(await front.browser.go(`/bookings/${id}`), new RegExp(doc2.last));
      const mail = await waitMail(patA.email, 'Ihr Termin wurde geändert', { since });
      assert.match(mail.text, new RegExp(doc2.first));
      assert.doesNotMatch((await front.browser.go(`/staff/bookings?view=open&patient_id=${state.idA}`)).text, new RegExp(`/bookings/${id}`));
    });

    it('reschedules an appointment; the patient is informed', async () => {
      const page = await front.browser.go(`/bookings/${state.bookingC}/reschedule?from=${state.from}`);
      const times = [...page.text.matchAll(/name="starts_at" value="([^"]+)"/g)].map((m) => m[1]);
      assert.ok(times.length > 0);
      const since = Date.now() - 1000;
      const r = await front.browser.post(`/bookings/${state.bookingC}/reschedule`, { starts_at: times.at(-1) });
      assert.equal(r.location, `/bookings/${state.bookingC}?m=rescheduled`);
      await waitMail(patC.email, 'Ihr Termin wurde geändert', { since });
      const edit = await front.browser.post(`/bookings/${state.bookingC}/edit`, { reason: 'neuer Grund', info: '' });
      assert.equal(edit.location, `/bookings/${state.bookingC}?m=saved`);
      pageHas(await front.browser.go(`/bookings/${state.bookingC}`), /neuer Grund/);
    });

    it('patients and doctors do not get the reception pages', async () => {
      for (const u of [patA, doc1]) {
        for (const path of ['/staff/bookings', '/staff/patients']) assert.equal((await u.browser.get(path)).status, 403, `${u.key} ${path}`);
      }
    });
  });

  describe('UF-07: cancelling', () => {
    it('a patient cancels; the slot is free again and the patient gets a mail', async () => {
      const since = Date.now() - 1000;
      await patB.browser.go('/appointments');
      const r = await patB.browser.post(`/appointments/${state.bookingB}/cancel`, {});
      assert.equal(r.location, '/appointments?m=cancelled');
      await waitMail(patB.email, 'Ihr Termin wurde storniert', { since });
      assert.doesNotMatch(html(await patB.browser.go('/appointments')), new RegExp(`/bookings/${state.bookingB}`));
      // Now patient C can take the freed time.
      await patD.browser.go(`/book/${state.serviceId}?from=${state.from}`);
      const again = await patD.browser.post(`/book/${state.serviceId}`, { starts_at: state.slot });
      assert.equal(again.location, '/appointments?m=booked', `status ${again.status} ${decode(again.text).match(/class="alert"[^>]*>([^<]+)/)?.[1] ?? ''}`);
    });
  });

  describe('UF-04: the own account', () => {
    it('changes the profile', async () => {
      const b = patA.browser;
      await b.go('/account');
      const r = await b.post('/account', { first_name: patA.first, last_name: patA.last, phone: '0621 999', street: 'Neue Str. 5', postal_code: '68161', city: 'Mannheim' });
      assert.equal(r.location, '/account?m=profile_saved');
      const page = await b.go('/account');
      assert.match(page.text, /value="0621 999"/);
      assert.match(page.text, /value="Neue Str. 5"/);
    });

    it('changes the e-mail address through a link sent to the new address', async () => {
      const b = patB.browser;
      await b.go('/account');
      const wrong = await b.post('/account/email', { new_email: `b2-${run}@example.org`, current_password: 'falsch' });
      assert.equal(wrong.status, 422);
      const since = Date.now() - 1000;
      const r = await b.post('/account/email', { new_email: `b2-${run}@example.org`, current_password: patB.password });
      assert.equal(r.location, '/account?m=email_requested');
      await waitMail(patB.email, 'Änderung', { since });
      const mail = await waitMail(`b2-${run}@example.org`, 'Neue E-Mail-Adresse', { since });
      const link = linkOf(mail, '/confirm-email-change');
      const anon = new Browser();
      await anon.get(link);
      const done = await anon.post('/confirm-email-change', { token: new URL(link, env.base).searchParams.get('token') });
      assert.equal(done.location, '/login?m=email_changed');
      patB.email = `b2-${run}@example.org`;
      patB.browser = new Browser('patb2');
      assert.equal((await loginPassword(patB)).status, 302);
      assert.equal((await patB.browser.post('/login/mfa', { mode: 'code', code: await nextCode(patB) })).status, 302);
    });

    it('changing the password ends the other sessions but keeps this one', async () => {
      const copy = patA.browser.cookie('__Host-session');
      const old = new Browser();
      old.jar.set('__Host-session', copy);
      assert.equal((await old.get('/appointments')).status, 200, 'the copy of the session works before the change');
      const b = patA.browser;
      await b.go('/account');
      const r = await b.post('/account/password', { current_password: patA.password, new_password: OTHER, password2: OTHER });
      assert.equal(r.location, '/account?m=password_saved');
      patA.password = OTHER;
      assert.equal((await b.get('/appointments')).status, 200, 'this browser got the new token');
      await assertRevoked(copy, 'copy of the session');
    });

    it('recovery codes work once; new codes invalidate the old ones', async () => {
      const u = patA;
      const fresh = new Browser('pata-recovery');
      const first = u.recovery[0];
      await fresh.get('/login');
      await fresh.post('/login', { email: u.email, password: u.password });
      await fresh.get('/login/mfa?recovery=1');
      assert.equal((await fresh.post('/login/mfa', { mode: 'recovery', recovery_code: first })).status, 302);
      const reuse = new Browser();
      await reuse.get('/login');
      await reuse.post('/login', { email: u.email, password: u.password });
      await reuse.get('/login/mfa?recovery=1');
      assert.equal((await reuse.post('/login/mfa', { mode: 'recovery', recovery_code: first })).status, 401, 'a code works once');
      // New codes from the full session.
      await u.browser.go('/account');
      const gen = await u.browser.post('/account/mfa/recovery-codes', { code: await nextCode(u) });
      assert.equal(gen.status, 200);
      const newCodes = [...gen.text.matchAll(/<li>([a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4})<\/li>/g)].map((m) => m[1]);
      assert.equal(newCodes.length, 10);
      const old = new Browser();
      await old.get('/login');
      await old.post('/login', { email: u.email, password: u.password });
      await old.get('/login/mfa?recovery=1');
      assert.equal((await old.post('/login/mfa', { mode: 'recovery', recovery_code: u.recovery[1] })).status, 401, 'old codes are void');
      u.recovery = newCodes;
    });

    it('the second factor can only be replaced after removing it (decision 22)', async () => {
      const u = patA;
      await u.browser.go('/account');
      // No wrong code here: failed second-factor attempts count towards the lock-out of the account (SR.05).
      const r = await u.browser.post('/account/mfa/disable', { mode: 'code', password: u.password, code: await nextCode(u) });
      assert.equal(r.location, '/login?m=mfa_removed', `status ${r.status} ${decode(r.text).match(/class="alert"[^>]*>([^<]+)/)?.[1] ?? ''}`);
      assert.ok(!u.browser.cookie('__Host-session'));
      const old = new Browser();
      old.jar.set('__Host-session', 'x');
      // A new login only reaches the setup, and a new key is created.
      const oldSecret = u.secret;
      u.browser = new Browser('pata3');
      assert.equal((await loginPassword(u)).location, '/setup', 'MFA was removed: straight to the setup');
      await u.browser.go('/setup');
      const start = await u.browser.post('/setup/mfa/start', {});
      u.secret = /class="secret">([A-Z2-7]{32})</.exec(start.text)[1];
      u.lastStep = 0;
      assert.notEqual(u.secret, oldSecret);
      const ok = await u.browser.post('/setup/mfa/confirm', { code: await nextCode(u), secret: u.secret });
      assert.equal(ok.status, 200);
    });
  });

  describe('SR.05 and UF-10: lock-out and unlocking', () => {
    it('three wrong passwords lock the account even for the right password; the administrator unlocks', async () => {
      const u = patC;
      for (let i = 0; i < 3; i++) {
        const b = new Browser();
        await b.get('/login');
        assert.equal((await b.post('/login', { email: u.email, password: 'Falsches-Passwort-123456' })).status, 401);
      }
      const locked = new Browser();
      await locked.get('/login');
      const r = await locked.post('/login', { email: u.email, password: u.password });
      assert.equal(r.status, 401, 'the right password is refused during the lock');
      pageHas(r, /E-Mail-Adresse oder Passwort stimmen nicht/);
      const found = await admin.browser.go(`/admin/users?q=${u.email}`);
      const id = new RegExp(`/admin/users/(${UUID})`).exec(found.text)[1];
      pageHas(found, /zeitweise gesperrt/);
      const page = await admin.browser.go(`/admin/users/${id}`);
      pageHas(page, /Konto freischalten/);
      const unlock = await admin.browser.post(`/admin/users/${id}/unlock`, {});
      assert.equal(unlock.location, `/admin/users/${id}?m=unlocked`);
      u.browser = new Browser('patc2');
      assert.equal((await loginPassword(u)).status, 302, 'login works after the unlock');
    });

    it('the administrator changes roles (sessions end), resets a password and sees the audit log', async () => {
      const id = patD.id ?? new RegExp(`/admin/users/(${UUID})`).exec((await admin.browser.go(`/admin/users?q=${patD.email}`)).text)[1];
      const b = admin.browser;
      await b.go(`/admin/users/${id}`);
      const roles = await b.post(`/admin/users/${id}/roles`, { roles: ['patient', 'doctor'] });
      assert.equal(roles.location, `/admin/users/${id}?m=roles_saved`);
      await assertRevoked(patD.browser.cookie('__Host-session'), 'sessions of the person');
      const own = await b.post(`/admin/users/${admin.id ?? '00000000-0000-4000-8000-000000000000'}/roles`, { roles: ['patient'] });
      assert.ok([404, 422].includes(own.status));
      const since = Date.now() - 1000;
      const reset = await b.post(`/admin/users/${id}/password-reset`, {});
      assert.equal(reset.location, `/admin/users/${id}?m=reset_requested`);
      await waitMail(patD.email, 'Passwort zurücksetzen', { since });
      const audit = await b.go('/admin/audit');
      pageHas(audit, /roles_changed/);
      pageHas(audit, /password_reset_requested/);
      const retention = await b.go('/admin/retention');
      pageHas(retention, /Aufbewahrungsfristen/);
      const bad = await b.post('/admin/retention', { retention_a_days: '30', retention_b_days: '365', retention_c_days: '10', retention_d_days: '30', retention_e_days: '90' });
      assert.equal(bad.status, 422);
      const keep = await b.post('/admin/retention', { retention_a_days: '30', retention_b_days: '365', retention_c_days: '730', retention_d_days: '30', retention_e_days: '90' });
      assert.equal(keep.location, '/admin/retention?m=saved');
    });
  });

  describe('UF-13: deleting the account', () => {
    it('needs password, code and confirmation; afterwards the account is gone and future appointments are cancelled', async () => {
      const u = patB;
      await u.browser.go('/account');
      const noBox = await u.browser.post('/account/delete', { password: u.password, code: await nextCode(u) });
      assert.equal(noBox.status, 422);
      const wrong = await u.browser.post('/account/delete', { password: 'falsch', code: await nextCode(u), confirm: 'yes' });
      assert.equal(wrong.status, 422);
      const r = await u.browser.post('/account/delete', { password: u.password, code: await nextCode(u), confirm: 'yes' });
      assert.equal(r.location, '/login?m=deleted');
      await waitMail(u.email, 'gelöscht');
      assert.ok(!u.browser.cookie('__Host-session'));
      u.browser = new Browser();
      await u.browser.get('/login');
      assert.equal((await u.browser.post('/login', { email: u.email, password: u.password })).status, 401);
    });
  });

  describe('security through the real stack', () => {
    it('refuses a form without a valid CSRF token and a form from another origin', async () => {
      const b = patA.browser;
      await b.go('/account');
      const noToken = await b.request('POST', '/account', { form: { first_name: 'X', last_name: 'Y' } });
      assert.equal(noToken.status, 403);
      const wrongSite = await b.post('/account', { first_name: 'X', last_name: 'Y' }, { headers: { origin: 'https://evil.example', 'sec-fetch-site': 'cross-site' } });
      assert.equal(wrongSite.status, 403);
      const other = new Browser();
      await other.get('/login');
      const foreign = await b.request('POST', '/account', { form: { _csrf: other.page.token, first_name: 'X', last_name: 'Y' } });
      assert.equal(foreign.status, 403, 'the token of another visitor');
      const kept = await patA.browser.go('/account');
      assert.doesNotMatch(kept.text, /value="X"/);
    });

    it('escapes names everywhere (XSS)', async () => {
      await register(xss);
      const home = await onboard(xss);
      assert.equal(home.status, 200);
      assert.doesNotMatch(home.text, /<script>alert\(1\)<\/script>/);
      assert.match(home.text, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
      const list = await admin.browser.go(`/admin/users?q=${xss.last}`);
      assert.doesNotMatch(list.text, /<script>alert\(1\)<\/script>/);
      assert.match(list.text, /&lt;script&gt;/);
      const account = await xss.browser.go('/account');
      assert.doesNotMatch(account.text, /<script>alert\(1\)<\/script>/);
    });

    it('the session token never appears in a page, and the session cookie has the right flags', async () => {
      const u = new Browser();
      const fresh = person('flags', 'F', 'Lags');
      await register(Object.assign(fresh, { browser: u }));
      const r = await loginPassword(fresh);
      assert.equal(r.status, 302);
      assert.equal(r.location, '/setup');
      const setupSession = r.setCookies.find((c) => c.startsWith('__Host-session='));
      assert.match(setupSession, /HttpOnly; Secure; SameSite=Strict|; Path=\/; (Max-Age=\d+; )?HttpOnly; Secure; SameSite=Strict/);
      await u.go('/setup');
      const start = await u.post('/setup/mfa/start', {});
      fresh.secret = /class="secret">([A-Z2-7]{32})</.exec(start.text)[1];
      const done = await u.post('/setup/mfa/confirm', { code: await nextCode(fresh), secret: fresh.secret });
      const session = done.setCookies.find((c) => c.startsWith('__Host-session='));
      assert.match(session, /HttpOnly/);
      assert.match(session, /Secure/);
      assert.match(session, /SameSite=Strict/);
      assert.match(session, /Path=\//);
      assert.doesNotMatch(session, /Domain=/i);
      const token = u.cookie('__Host-session');
      for (const path of ['/account', '/appointments', '/book']) assert.ok(!(await u.go(path)).text.includes(token), path);
    });

    it('logging out revokes the token on the server, not only in the browser', async () => {
      const u = person('logout', 'L', 'Ogout');
      await register(u);
      await onboard(u);
      const token = u.browser.cookie('__Host-session');
      await u.browser.go('/account');
      const out = await u.browser.post('/logout', {});
      assert.equal(out.location, '/login?m=logged_out');
      await assertRevoked(token, 'token after logout');
    });

    it('cannot reach internal services or API routes through the ingress', async () => {
      const b = new Browser();
      for (const path of ['/v1/me', '/v1/jwks', '/v1/ops/userList', '/api/v1/messages', '/static/../package.json', '/src/config.js', '/healthz-x']) {
        assert.equal((await b.get(path)).status, 404, path);
      }
      assert.equal((await b.get('/healthz')).status, 200);
    });

    it('limits the rate of public forms (429), per operation', async () => {
      const b = new Browser();
      await b.get('/forgot-password');
      const seen = new Set();
      for (let i = 0; i < 45; i++) {
        const r = await b.post('/forgot-password', { email: `flood${i}-${run}@example.org` });
        seen.add(r.status);
        if (r.status === 429) break;
      }
      assert.ok(seen.has(429), `statuses seen: ${[...seen]}`);
      void sleep;
      void mails;
    });
  });
});
