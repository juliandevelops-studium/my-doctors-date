// A patient books and cancels appointments (UF-05, UF-06, UF-07). The times on offer come from calendar_service,
// which applies the main doctor rule, the specialty and the working time. The frontend only shows them.
import { INSTANT, weekCalendar, weekRange } from '../calendar.js';
import { HttpError, problemMessage } from '../errors.js';
import { idParam, text } from '../form.js';
import { requireFullSession, requireLogin, showOnlyFor } from '../middleware.js';

export default function patientRoutes(app, { fmt }) {
  const guard = [requireLogin, requireFullSession, showOnlyFor('patient')];

  app.get('/book', guard, async (req, res) => {
    const { body } = await req.api.need('calendar', 'GET', '/v1/services');
    res.render('book_services.njk', { title: 'Termin buchen', services: body.items, baseUrl: '/book' });
  });

  async function slotsPage(req, res, status, { serviceId, error } = {}) {
    const range = weekRange(fmt, req.query.from ?? req.body?.from);
    const services = (await req.api.need('calendar', 'GET', '/v1/services')).body.items;
    const service = services.find((s) => s.id === serviceId);
    if (!service) throw new HttpError(404, { code: 'not_found' });
    const { body } = await req.api.need('calendar', 'GET', '/v1/calendar/availability', {
      query: { service_id: serviceId, from: range.fromInstant.toISOString(), to: range.toInstant.toISOString() },
    });
    res.status(status).render('book_slots.njk', {
      title: `Termin buchen: ${service.name}`, service, range, week: weekCalendar(range, body.items, fmt), empty: body.items.length === 0, error,
      action: `/book/${serviceId}`, pagerBase: `/book/${serviceId}?`, reason: text(req.body, 'reason', 200), info: text(req.body, 'info', 2000),
    });
  }

  app.get('/book/:serviceId', guard, (req, res) => slotsPage(req, res, 200, { serviceId: idParam(req.params.serviceId) }));

  app.post('/book/:serviceId', guard, async (req, res) => {
    const serviceId = idParam(req.params.serviceId);
    const startsAt = text(req.body, 'starts_at', 40);
    if (!INSTANT.test(startsAt)) return slotsPage(req, res, 422, { serviceId, error: 'Bitte wählen Sie einen Termin aus.' });
    const body = { service_id: serviceId, starts_at: startsAt };
    const reason = text(req.body, 'reason', 200);
    const info = text(req.body, 'info', 2000);
    if (reason) body.reason = reason;
    if (info) body.info = info;
    const r = await req.api.raw('calendar', 'POST', '/v1/bookings', { body });
    if (r.status === 201) return res.redirect('/appointments?m=booked');
    if ([409, 422, 429].includes(r.status)) return slotsPage(req, res, r.status, { serviceId, error: problemMessage(r.body) });
    throw new HttpError(r.status, r.body);
  });

  app.get('/appointments', guard, async (req, res) => {
    const [upcoming, past] = await Promise.all([
      req.api.need('calendar', 'GET', '/v1/bookings', { query: { when: 'upcoming', limit: 100 } }),
      req.api.need('calendar', 'GET', '/v1/bookings', { query: { when: 'past', limit: 50 } }),
    ]);
    res.render('appointments.njk', { title: 'Meine Termine', upcoming: upcoming.body.items, past: past.body.items.slice().reverse() });
  });

  app.post('/appointments/:id/cancel', guard, async (req, res) => {
    await req.api.need('calendar', 'POST', `/v1/bookings/${idParam(req.params.id)}/cancel`);
    res.redirect('/appointments?m=cancelled');
  });
}
