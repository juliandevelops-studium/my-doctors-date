// Details of one booking and the doctor's week (UF-07, UF-08, UF-11). What a user may see or do with a booking is
// decided by calendar_service; a booking of somebody else simply answers 404.
import { groupByDay, INSTANT, weekRange } from '../calendar.js';
import { HttpError, problemMessage } from '../errors.js';
import { idParam, optionalUuid, text } from '../form.js';
import { has, requireFullSession, requireLogin, showOnlyFor } from '../middleware.js';

export default function bookingRoutes(app, { fmt }) {
  const guard = [requireLogin, requireFullSession];

  // The doctor's own calendar for one week.
  app.get('/calendar', guard, showOnlyFor('doctor'), async (req, res) => {
    const range = weekRange(fmt, req.query.from, { allowPast: true });
    const { body } = await req.api.need('calendar', 'GET', '/v1/bookings', {
      query: { from: range.fromInstant.toISOString(), to: range.toInstant.toISOString(), limit: 200 },
    });
    res.render('calendar.njk', { title: 'Mein Kalender', range, days: groupByDay(body.items, fmt), total: body.total });
  });

  const back = (req) => (has(req, 'desk', 'organisator') ? '/staff/bookings' : has(req, 'doctor') && !has(req, 'patient') ? '/calendar' : '/appointments');

  async function detail(req, res, status = 200, extra = {}) {
    const id = idParam(req.params.id);
    const booking = (await req.api.need('calendar', 'GET', `/v1/bookings/${id}`)).body;
    const staff = has(req, 'desk', 'organisator');
    const doctors = staff ? (await req.api.need('calendar', 'GET', '/v1/doctors')).body.items : [];
    const upcoming = new Date(booking.ends_at) > new Date();
    res.status(status).render('booking.njk', { title: 'Termin', booking, staff, doctors, upcoming, back: back(req), ...extra });
  }

  app.get('/bookings/:id', guard, (req, res) => detail(req, res));

  app.post('/bookings/:id/cancel', guard, async (req, res) => {
    await req.api.need('calendar', 'POST', `/v1/bookings/${idParam(req.params.id)}/cancel`);
    res.redirect(`${back(req)}?m=cancelled`);
  });

  app.post('/bookings/:id/edit', guard, showOnlyFor('desk', 'organisator'), async (req, res) => {
    const id = idParam(req.params.id);
    const r = await req.api.raw('calendar', 'PATCH', `/v1/bookings/${id}`, { body: { reason: text(req.body, 'reason', 200), info: text(req.body, 'info', 2000) } });
    if (r.status === 200) return res.redirect(`/bookings/${id}?m=saved`);
    if ([409, 422].includes(r.status)) return detail(req, res, r.status, { error: problemMessage(r.body) });
    throw new HttpError(r.status, r.body);
  });

  app.post('/bookings/:id/assign', guard, showOnlyFor('desk', 'organisator'), async (req, res) => {
    const id = idParam(req.params.id);
    const doctorId = optionalUuid(text(req.body, 'doctor_id', 40));
    if (!doctorId) return detail(req, res, 422, { error: 'Bitte wählen Sie eine Ärztin oder einen Arzt.' });
    const r = await req.api.raw('calendar', 'PUT', `/v1/bookings/${id}/doctor`, { body: { doctor_id: doctorId } });
    if (r.status === 200) return res.redirect(`/bookings/${id}?m=assigned`);
    if ([409, 422].includes(r.status)) return detail(req, res, r.status, { error: problemMessage(r.body) });
    throw new HttpError(r.status, r.body);
  });

  async function reschedulePage(req, res, status = 200, error = undefined) {
    const id = idParam(req.params.id);
    const booking = (await req.api.need('calendar', 'GET', `/v1/bookings/${id}`)).body;
    const range = weekRange(fmt, req.query.from ?? req.body?.from);
    const { body } = await req.api.need('calendar', 'GET', '/v1/calendar/availability', {
      query: { service_id: booking.service.id, from: range.fromInstant.toISOString(), to: range.toInstant.toISOString() },
    });
    // The booking keeps its doctor; show only the times of that doctor (reception can assign another doctor first).
    const items = booking.doctor ? body.items.filter((s) => s.doctor_id === booking.doctor.id) : body.items;
    res.status(status).render('reschedule.njk', { title: 'Termin verschieben', booking, range, days: groupByDay(items, fmt), error, action: `/bookings/${id}/reschedule` });
  }

  app.get('/bookings/:id/reschedule', guard, showOnlyFor('desk', 'organisator'), (req, res) => reschedulePage(req, res));

  app.post('/bookings/:id/reschedule', guard, showOnlyFor('desk', 'organisator'), async (req, res) => {
    const id = idParam(req.params.id);
    const startsAt = text(req.body, 'starts_at', 40);
    if (!INSTANT.test(startsAt)) return reschedulePage(req, res, 422, 'Bitte wählen Sie einen Termin aus.');
    const r = await req.api.raw('calendar', 'PATCH', `/v1/bookings/${id}`, { body: { starts_at: startsAt } });
    if (r.status === 200) return res.redirect(`/bookings/${id}?m=rescheduled`);
    if ([409, 422].includes(r.status)) return reschedulePage(req, res, r.status, problemMessage(r.body));
    throw new HttpError(r.status, r.body);
  });
}
