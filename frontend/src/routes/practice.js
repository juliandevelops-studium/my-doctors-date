// Practice organisation: specialties, the Leistungsangebot, JSON import, working times of the doctors (UF-09).
import { fieldErrors, HttpError, problemMessage } from '../errors.js';
import { idParam, intParam, isUuid, list, text } from '../form.js';
import { WEEKDAYS } from '../format.js';
import { requireFullSession, requireLogin, showOnlyFor } from '../middleware.js';

const HHMM = /^([01]\d|2[0-3]):[0-5]\d$/;
const BLOCKS = 3; // working blocks per weekday in the editor

// "Mo–Fr 08:00–12:00 · Sa 09:00–13:00" from working_days [{weekday: 1..7, start_time, end_time}]
function summarizeDays(days) {
  const byTime = new Map();
  for (const d of days) {
    const key = `${d.start_time}–${d.end_time}`;
    if (!byTime.has(key)) byTime.set(key, []);
    byTime.get(key).push(d.weekday);
  }
  const short = (n) => WEEKDAYS[n - 1].label.slice(0, 2);
  const parts = [];
  for (const [time, wds] of byTime) {
    wds.sort((a, b) => a - b);
    const runs = [];
    let start = wds[0], prev = wds[0];
    for (let i = 1; i <= wds.length; i++) {
      if (wds[i] === prev + 1) { prev = wds[i]; continue; }
      runs.push(start === prev ? short(start) : `${short(start)}–${short(prev)}`);
      start = wds[i]; prev = wds[i];
    }
    parts.push(`${runs.join(', ')} ${time}`);
  }
  return parts.join(' · ') || '–';
}

export default function practiceRoutes(app) {
  const guard = [requireLogin, requireFullSession, showOnlyFor('organisator')];

  async function overview(req, res, status = 200, extra = {}) {
    const [specialties, services, doctors] = await Promise.all([
      req.api.need('calendar', 'GET', '/v1/specialties'),
      req.api.need('calendar', 'GET', '/v1/services'),
      req.api.need('calendar', 'GET', '/v1/doctors'),
    ]);
    const schedules = await Promise.all(doctors.body.items.map((d) =>
      req.api.need('calendar', 'GET', `/v1/doctors/${d.id}/schedule`)
        .then((r) => [d.id, summarizeDays(r.body.working_days)])
        .catch(() => [d.id, '–'])));
    const hours = Object.fromEntries(schedules);
    res.status(status).render('practice.njk', { title: 'Praxis', specialties: specialties.body.items, services: services.body.items, doctors: doctors.body.items, hours, ...extra });
  }

  app.get('/practice', guard, (req, res) => overview(req, res));

  app.post('/practice/specialties', guard, async (req, res) => {
    const r = await req.api.raw('calendar', 'POST', '/v1/specialties', { body: { name: text(req.body, 'name', 100) } });
    if (r.status === 201) return res.redirect('/practice?m=created');
    if ([409, 422].includes(r.status)) return overview(req, res, r.status, { error: problemMessage(r.body), errors: fieldErrors(r.body), section: 'specialty' });
    throw new HttpError(r.status, r.body);
  });

  app.post('/practice/specialties/:id/delete', guard, async (req, res) => {
    const r = await req.api.raw('calendar', 'DELETE', `/v1/specialties/${idParam(req.params.id)}`);
    if (r.status === 204) return res.redirect('/practice?m=deleted_entry');
    if ([404, 409].includes(r.status)) return overview(req, res, r.status, { error: problemMessage(r.body), section: 'specialty' });
    throw new HttpError(r.status, r.body);
  });

  const serviceBody = (b) => {
    const body = { name: text(b, 'name', 100), duration_minutes: intParam(b?.duration_minutes, { min: 0, max: 100000, fallback: 0 }) };
    const sp = text(b, 'specialty_id', 40);
    if (isUuid(sp)) body.specialty_id = sp;
    return body;
  };

  app.post('/practice/services', guard, async (req, res) => {
    const r = await req.api.raw('calendar', 'POST', '/v1/services', { body: serviceBody(req.body) });
    if (r.status === 201) return res.redirect('/practice?m=created');
    if ([409, 422].includes(r.status)) return overview(req, res, r.status, { error: problemMessage(r.body), errors: fieldErrors(r.body), section: 'service', v: req.body });
    throw new HttpError(r.status, r.body);
  });

  async function serviceForm(req, res, status = 200, extra = {}) {
    const id = idParam(req.params.id);
    const [services, specialties] = await Promise.all([req.api.need('calendar', 'GET', '/v1/services'), req.api.need('calendar', 'GET', '/v1/specialties')]);
    const service = services.body.items.find((s) => s.id === id);
    if (!service) throw new HttpError(404, { code: 'not_found' });
    res.status(status).render('service_edit.njk', { title: 'Leistung bearbeiten', service, specialties: specialties.body.items, ...extra });
  }

  app.get('/practice/services/:id', guard, (req, res) => serviceForm(req, res));

  app.post('/practice/services/:id', guard, async (req, res) => {
    const r = await req.api.raw('calendar', 'PUT', `/v1/services/${idParam(req.params.id)}`, { body: serviceBody(req.body) });
    if (r.status === 200) return res.redirect('/practice?m=saved');
    if ([404, 409, 422].includes(r.status)) return serviceForm(req, res, r.status, { error: problemMessage(r.body), errors: fieldErrors(r.body) });
    throw new HttpError(r.status, r.body);
  });

  app.post('/practice/services/:id/delete', guard, async (req, res) => {
    const r = await req.api.raw('calendar', 'DELETE', `/v1/services/${idParam(req.params.id)}`);
    if (r.status === 204) return res.redirect('/practice?m=deleted_entry');
    if ([404, 409].includes(r.status)) return overview(req, res, r.status, { error: problemMessage(r.body), section: 'service' });
    throw new HttpError(r.status, r.body);
  });

  // JSON template import. The syntax is checked here; the structure and every limit by calendar_service.
  app.post('/practice/import', guard, async (req, res) => {
    const raw = typeof req.body?.json === 'string' ? req.body.json : '';
    let parsed;
    try {
      parsed = JSON.parse(raw);
    } catch {
      return overview(req, res, 422, { error: 'Der Text ist kein gültiges JSON.', section: 'import', importText: raw.slice(0, 20000) });
    }
    const dry = req.body?.dry_run === 'yes';
    const r = await req.api.raw('calendar', 'POST', '/v1/services/import', { body: parsed, query: { dry_run: dry ? 'true' : 'false' } });
    if (r.status === 200) return res.render('import_result.njk', { title: 'Import', result: r.body });
    if ([422, 409].includes(r.status)) return overview(req, res, r.status, { error: problemMessage(r.body, 'Die Vorlage ist ungültig.'), section: 'import', importText: raw.slice(0, 20000) });
    throw new HttpError(r.status, r.body);
  });

  // Working times and specialties of one doctor.
  async function scheduleForm(req, res, status = 200, extra = {}) {
    const id = idParam(req.params.id);
    const [doctors, specialties, schedule] = await Promise.all([
      req.api.need('calendar', 'GET', '/v1/doctors'),
      req.api.need('calendar', 'GET', '/v1/specialties'),
      req.api.need('calendar', 'GET', `/v1/doctors/${id}/schedule`),
    ]);
    const doctor = doctors.body.items.find((d) => d.id === id);
    if (!doctor) throw new HttpError(404, { code: 'not_found' });
    const rows = extra.rows ?? WEEKDAYS.map((d) => {
      const blocks = schedule.body.working_days.filter((w) => w.weekday === d.n).slice(0, BLOCKS);
      return { ...d, blocks: Array.from({ length: BLOCKS }, (_, i) => ({ from: blocks[i]?.start_time ?? '', to: blocks[i]?.end_time ?? '' })) };
    });
    const selected = extra.selected ?? schedule.body.specialty_ids;
    res.status(status).render('schedule.njk', { title: 'Arbeitszeiten', doctor, specialties: specialties.body.items, rows, selected, ...extra });
  }

  app.get('/practice/doctors/:id', guard, (req, res) => scheduleForm(req, res));

  app.post('/practice/doctors/:id', guard, async (req, res) => {
    const id = idParam(req.params.id);
    const days = [];
    const problems = [];
    const rows = WEEKDAYS.map((d) => {
      const blocks = [];
      for (let i = 1; i <= BLOCKS; i++) {
        const from = text(req.body, `${d.key}_from${i}`, 5);
        const to = text(req.body, `${d.key}_to${i}`, 5);
        blocks.push({ from, to });
        if (!from && !to) continue;
        if (!HHMM.test(from) || !HHMM.test(to)) problems.push(`${d.label}: Bitte Beginn und Ende angeben (Format HH:MM).`);
        else days.push({ weekday: d.n, start_time: from, end_time: to });
      }
      return { ...d, blocks };
    });
    const selected = list(req.body, 'specialty').filter(isUuid);
    if (problems.length) return scheduleForm(req, res, 422, { rows, selected, error: problems.join(' ') });
    const r = await req.api.raw('calendar', 'PUT', `/v1/doctors/${id}/schedule`, { body: { specialty_ids: selected, working_days: days } });
    if (r.status === 200) {
      const outside = r.body.bookings_outside_hours.length;
      return res.render('schedule_saved.njk', { title: 'Arbeitszeiten gespeichert', doctorId: id, outside });
    }
    if ([404, 409, 422].includes(r.status)) return scheduleForm(req, res, r.status, { rows, selected, error: problemMessage(r.body), errors: fieldErrors(r.body) });
    throw new HttpError(r.status, r.body);
  });
}
