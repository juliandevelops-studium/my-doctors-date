// Reception and organisation work with all appointments and patients (UF-06, UF-08). calendar_service answers 403 to
// everybody else; showOnlyFor() just avoids showing the pages to people who cannot use them.
import { INSTANT, weekCalendar, weekRange } from '../calendar.js';
import { HttpError, problemMessage } from '../errors.js';
import { idParam, intParam, isUuid, oneOf, optionalUuid, text } from '../form.js';
import { requireFullSession, requireLogin, showOnlyFor } from '../middleware.js';

const PAGE = 25;

export default function staffRoutes(app, { fmt }) {
  const guard = [requireLogin, requireFullSession, showOnlyFor('desk', 'organisator')];
  const doctorsOf = async (req) => (await req.api.need('calendar', 'GET', '/v1/doctors')).body.items;
  const nameOf = (d) => `${d.first_name} ${d.last_name}`;

  const VIEWS = [
    { id: 'upcoming', label: 'Kommende' },
    { id: 'open', label: 'Zuordnung offen' },
    { id: 'past', label: 'Vergangene' },
    { id: 'all', label: 'Alle' },
  ];

  // All bookings. "open" = bookings without a doctor (Zuordnung offen, UF-06).
  app.get('/staff/bookings', guard, async (req, res) => {
    const view = oneOf(req.query.view, VIEWS.map((v) => v.id)) ?? 'upcoming';
    const offset = intParam(req.query.offset);
    const patientId = optionalUuid(req.query.patient_id);
    const patientName = text(req.query, 'name', 100);
    const doctorId = optionalUuid(req.query.doctor_id);
    const query = { limit: PAGE, offset, patient_id: patientId, doctor_id: doctorId };
    if (view === 'open') query.unassigned = true;
    if (view === 'upcoming' || view === 'open') query.when = 'upcoming';
    if (view === 'past') query.when = 'past';
    const [bookings, doctors] = await Promise.all([req.api.need('calendar', 'GET', '/v1/bookings', { query }), doctorsOf(req)]);
    const keep = new URLSearchParams({ view, ...(patientId && { patient_id: patientId }), ...(patientName && { name: patientName }), ...(doctorId && { doctor_id: doctorId }) });
    res.render('staff_bookings.njk', {
      title: 'Termine', view, views: VIEWS, doctors, patientId, patientName, doctorId, items: bookings.body.items, total: bookings.body.total, offset, limit: PAGE,
      pagerBase: `/staff/bookings?${keep}&`,
    });
  });

  app.get('/staff/patients', guard, async (req, res) => {
    const q = text(req.query, 'q', 100);
    const offset = intParam(req.query.offset);
    const edit = optionalUuid(req.query.edit);
    let patients = { items: [], total: 0 };
    let error;
    if (q.length === 1) error = 'Bitte geben Sie mindestens zwei Zeichen ein.';
    else patients = (await req.api.need('calendar', 'GET', '/v1/patients', { query: { q, limit: PAGE, offset } })).body;
    const doctors = await doctorsOf(req);
    res.status(error ? 422 : 200).render('staff_patients.njk', {
      title: 'Patient:innen', q, doctors, edit, doctorNames: Object.fromEntries(doctors.map((d) => [d.id, nameOf(d)])),
      items: patients.items, total: patients.total, offset, limit: PAGE, error,
      pagerBase: `/staff/patients?q=${encodeURIComponent(q)}&`,
    });
  });

  app.post('/staff/patients/:id/main-doctor', guard, async (req, res) => {
    const doctorId = text(req.body, 'doctor_id', 40);
    if (doctorId !== '' && !isUuid(doctorId)) throw new HttpError(422, { code: 'not_a_doctor' });
    await req.api.need('calendar', 'PUT', `/v1/patients/${idParam(req.params.id)}/main-doctor`, { body: { doctor_id: doctorId || null } });
    res.redirect(`/staff/patients?q=${encodeURIComponent(text(req.body, 'q', 100))}&m=main_doctor_saved`);
  });

  // Book for a patient: choose the service, then the time (with the doctor).
  app.get('/staff/book/:patientId', guard, async (req, res) => {
    const patientId = idParam(req.params.patientId);
    const name = text(req.query, 'name', 100);
    const { body } = await req.api.need('calendar', 'GET', '/v1/services');
    res.render('book_services.njk', { title: 'Termin für Patient:in buchen', services: body.items, baseUrl: `/staff/book/${patientId}`, suffix: name ? `?name=${encodeURIComponent(name)}` : '' });
  });

  async function staffSlots(req, res, status, { patientId, serviceId, error }) {
    const range = weekRange(fmt, req.query.from ?? req.body?.from);
    const name = text(req.query.name !== undefined ? req.query : req.body, 'name', 100);
    const [services, doctors] = await Promise.all([req.api.need('calendar', 'GET', '/v1/services'), doctorsOf(req)]);
    const service = services.body.items.find((s) => s.id === serviceId);
    if (!service) throw new HttpError(404, { code: 'not_found' });
    const { body } = await req.api.need('calendar', 'GET', '/v1/calendar/availability', {
      query: { service_id: serviceId, patient_id: patientId, from: range.fromInstant.toISOString(), to: range.toInstant.toISOString() },
    });
    res.status(status).render('book_slots.njk', {
      title: `Termin buchen: ${service.name}`, service, range, week: weekCalendar(range, body.items, fmt), empty: body.items.length === 0, error, patientName: name,
      action: `/staff/book/${patientId}/${serviceId}`, pagerBase: `/staff/book/${patientId}/${serviceId}?name=${encodeURIComponent(name)}&`,
      slotField: 'slot', hidden: { name }, doctorNames: Object.fromEntries(doctors.map((d) => [d.id, nameOf(d)])),
      reason: text(req.body, 'reason', 200), info: text(req.body, 'info', 2000),
    });
  }

  app.get('/staff/book/:patientId/:serviceId', guard, (req, res) =>
    staffSlots(req, res, 200, { patientId: idParam(req.params.patientId), serviceId: idParam(req.params.serviceId) }));

  app.post('/staff/book/:patientId/:serviceId', guard, async (req, res) => {
    const patientId = idParam(req.params.patientId);
    const serviceId = idParam(req.params.serviceId);
    const [startsAt, doctorId] = text(req.body, 'slot', 80).split('|');
    if (!INSTANT.test(startsAt ?? '')) return staffSlots(req, res, 422, { patientId, serviceId, error: 'Bitte wählen Sie einen Termin aus.' });
    const body = { patient_id: patientId, service_id: serviceId, starts_at: startsAt };
    if (isUuid(doctorId)) body.doctor_id = doctorId;
    const reason = text(req.body, 'reason', 200);
    const info = text(req.body, 'info', 2000);
    if (reason) body.reason = reason;
    if (info) body.info = info;
    const r = await req.api.raw('calendar', 'POST', '/v1/bookings', { body });
    if (r.status === 201) return res.redirect('/staff/bookings?m=booked');
    if ([409, 422, 429].includes(r.status)) return staffSlots(req, res, r.status, { patientId, serviceId, error: problemMessage(r.body) });
    throw new HttpError(r.status, r.body);
  });
}
