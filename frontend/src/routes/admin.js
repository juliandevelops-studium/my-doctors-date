// Technical administration: users, roles, locks, audit log, retention periods (UF-10, UF-13). The system administrator
// has no access to appointments; user_service and calendar_service enforce that.
import { fieldErrors, HttpError, problemMessage } from '../errors.js';
import { idParam, intParam, list, oneOf, ROLES, text } from '../form.js';
import { requireFullSession, requireLogin, showOnlyFor } from '../middleware.js';

const PAGE = 25;
const STATUSES = ['pending', 'active', 'locked', 'disabled'];

export default function adminRoutes(app) {
  const guard = [requireLogin, requireFullSession, showOnlyFor('systemadmin')];

  async function users(req, res, status = 200, extra = {}) {
    const q = text(req.query, 'q', 100);
    const role = oneOf(req.query.role, ROLES);
    const st = oneOf(req.query.status, STATUSES);
    const offset = intParam(req.query.offset);
    const { body } = await req.api.need('user', 'GET', '/v1/admin/users', { query: { q, role, status: st, limit: PAGE, offset } });
    const keep = new URLSearchParams({ ...(q && { q }), ...(role && { role }), ...(st && { status: st }) });
    res.status(status).render('admin_users.njk', {
      title: 'Benutzer', items: body.items, total: body.total, offset, limit: PAGE, q, role, statusFilter: st, roles: ROLES, statuses: STATUSES,
      pagerBase: `/admin/users?${keep}${keep.size ? '&' : ''}`, ...extra,
    });
  }

  app.get('/admin/users', guard, (req, res) => users(req, res));

  app.post('/admin/users', guard, async (req, res) => {
    const v = { first_name: text(req.body, 'first_name', 100), last_name: text(req.body, 'last_name', 100), email: text(req.body, 'email', 254), roles: list(req.body, 'roles').filter((r) => ROLES.includes(r)) };
    const r = await req.api.raw('user', 'POST', '/v1/admin/users', { body: v });
    if (r.status === 201) return res.redirect(`/admin/users/${r.body.id}?m=user_created`);
    if ([409, 422].includes(r.status)) return users(req, res, r.status, { error: problemMessage(r.body), errors: fieldErrors(r.body), v });
    throw new HttpError(r.status, r.body);
  });

  async function user(req, res, status = 200, extra = {}) {
    const id = idParam(req.params.id);
    const { body } = await req.api.need('user', 'GET', `/v1/admin/users/${id}`);
    res.status(status).render('admin_user.njk', { title: `${body.first_name} ${body.last_name}`, u: body, roles: ROLES, isSelf: id === req.session.me.id, ...extra });
  }

  app.get('/admin/users/:id', guard, (req, res) => user(req, res));

  app.post('/admin/users/:id/roles', guard, async (req, res) => {
    const id = idParam(req.params.id);
    const roles = list(req.body, 'roles').filter((r) => ROLES.includes(r));
    const r = await req.api.raw('user', 'PUT', `/v1/admin/users/${id}/roles`, { body: { roles } });
    if (r.status === 200) return res.redirect(`/admin/users/${id}?m=roles_saved`);
    if ([409, 422].includes(r.status)) return user(req, res, r.status, { error: problemMessage(r.body) });
    throw new HttpError(r.status, r.body);
  });

  app.post('/admin/users/:id/password-reset', guard, async (req, res) => {
    const id = idParam(req.params.id);
    await req.api.need('user', 'POST', `/v1/admin/users/${id}/password-reset`);
    res.redirect(`/admin/users/${id}?m=reset_requested`);
  });

  app.post('/admin/users/:id/unlock', guard, async (req, res) => {
    const id = idParam(req.params.id);
    await req.api.need('user', 'POST', `/v1/admin/users/${id}/unlock`);
    res.redirect(`/admin/users/${id}?m=unlocked`);
  });

  app.post('/admin/users/:id/delete', guard, async (req, res) => {
    const id = idParam(req.params.id);
    if (text(req.body, 'confirm', 5) !== 'yes') return user(req, res, 422, { error: 'Bitte bestätigen Sie das Löschen.' });
    const r = await req.api.raw('user', 'DELETE', `/v1/admin/users/${id}`);
    if (r.status === 204) return res.redirect('/admin/users?m=deleted_entry');
    if ([404, 409, 422].includes(r.status)) return user(req, res, r.status, { error: problemMessage(r.body) });
    throw new HttpError(r.status, r.body);
  });

  app.get('/admin/audit', guard, async (req, res) => {
    const action = text(req.query, 'action', 100);
    const offset = intParam(req.query.offset);
    const { body } = await req.api.need('user', 'GET', '/v1/admin/audit-log', { query: { action, limit: PAGE, offset } });
    res.render('admin_audit.njk', { title: 'Protokoll', items: body.items, total: body.total, offset, limit: PAGE, action, pagerBase: `/admin/audit?action=${encodeURIComponent(action)}&` });
  });

  const KEYS = ['retention_a_days', 'retention_b_days', 'retention_c_days', 'retention_d_days', 'retention_e_days'];

  app.get('/admin/retention', guard, async (req, res) => {
    const { body } = await req.api.need('user', 'GET', '/v1/admin/retention');
    res.render('admin_retention.njk', { title: 'Aufbewahrungsfristen', v: body, keys: KEYS });
  });

  app.post('/admin/retention', guard, async (req, res) => {
    const v = Object.fromEntries(KEYS.map((k) => [k, intParam(req.body?.[k], { min: 0, max: 100000, fallback: 0 })]));
    const r = await req.api.raw('user', 'PUT', '/v1/admin/retention', { body: v });
    if (r.status === 200) return res.redirect('/admin/retention?m=saved');
    if (r.status === 422) return res.status(422).render('admin_retention.njk', { title: 'Aufbewahrungsfristen', v, keys: KEYS, error: problemMessage(r.body, 'Bitte prüfen Sie die Fristen.'), errors: fieldErrors(r.body) });
    throw new HttpError(r.status, r.body);
  });
}
