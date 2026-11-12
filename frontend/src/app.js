// Builds the Express application. Everything the application needs is passed in, so the tests can run it in process
// against fake backends.
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import express from 'express';
import helmet from 'helmet';
import nunjucks from 'nunjucks';
import { Backend, BackendUnavailable } from './api.js';
import { csrf } from './csrf.js';
import { FLASH, HttpError, problemMessage } from './errors.js';
import { makeFormatters, ROLE_LABELS, STATUS_LABELS, WEEKDAYS } from './format.js';
import { attachApi, clearSession, safeNext, session } from './middleware.js';
import authRoutes from './routes/auth.js';
import setupRoutes from './routes/setup.js';
import accountRoutes from './routes/account.js';
import patientRoutes from './routes/patient.js';
import bookingRoutes from './routes/bookings.js';
import staffRoutes from './routes/staff.js';
import practiceRoutes from './routes/practice.js';
import adminRoutes from './routes/admin.js';

const here = path.dirname(fileURLToPath(import.meta.url));

export function createApp(config, { backend = new Backend(config), log = defaultLog } = {}) {
  const app = express();
  app.set('trust proxy', config.trustProxy); // Traefik is the one proxy in front
  app.disable('x-powered-by');
  app.set('etag', false); // pages hold personal data and are never cached
  app.set('view engine', 'njk');

  const fmt = makeFormatters(config.timezone);
  const env = nunjucks.configure(path.join(here, 'views'), { express: app, autoescape: true, noCache: process.env.NODE_ENV !== 'production' });
  env.addFilter('dateTime', fmt.dateTime);
  env.addFilter('date', fmt.date);
  env.addFilter('time', fmt.time);
  env.addFilter('ymd', fmt.ymd);
  env.addFilter('min', (list) => Math.min(...list));
  env.addFilter('max', (list) => Math.max(...list));
  env.addGlobal('practiceName', config.practiceName);
  env.addGlobal('roleLabels', ROLE_LABELS);
  env.addGlobal('statusLabels', STATUS_LABELS);
  env.addGlobal('weekdays', WEEKDAYS);

  // Security headers. There is no JavaScript in this application, so scripts are not allowed at all.
  app.use(
    helmet({
      contentSecurityPolicy: {
        useDefaults: false,
        directives: {
          'default-src': ["'none'"],
          'style-src': ["'self'"],
          'img-src': ["'self'"],
          'form-action': ["'self'"],
          'frame-ancestors': ["'none'"],
          'base-uri': ["'none'"],
        },
      },
      strictTransportSecurity: { maxAge: 31536000, includeSubDomains: true },
      referrerPolicy: { policy: 'no-referrer' },
      crossOriginOpenerPolicy: { policy: 'same-origin' },
      crossOriginResourcePolicy: { policy: 'same-origin' },
      xFrameOptions: { action: 'deny' },
    }),
  );
  app.use((req, res, next) => {
    res.set('Permissions-Policy', 'camera=(), microphone=(), geolocation=(), payment=(), usb=()');
    res.set('Cache-Control', 'no-store'); // pages contain personal data
    next();
  });
  app.use(accessLog(log));
  app.get('/healthz', (req, res) => res.type('text/plain').send('ok'));
  // In the cluster (HTTPS) static assets are cached for an hour; locally over plain HTTP they are not,
  // so CSS changes show up on a normal reload while reworking the layout.
  const staticMaxAge = config.cookieSecure ? 3600 : 0;
  app.use('/static', express.static(path.join(here, 'public'), { maxAge: staticMaxAge, index: false, dotfiles: 'ignore', setHeaders: (res) => res.set('Cache-Control', `public, max-age=${staticMaxAge}`) }));

  // Form bodies only, no JSON. The service import is the one large form.
  const small = express.urlencoded({ extended: false, limit: '64kb' });
  const large = express.urlencoded({ extended: false, limit: '300kb' });
  app.use((req, res, next) => {
    return (req.path === '/practice/import' ? large : small)(req, res, next);
  });
  app.use(csrf(config, log));
  app.use(attachApi(backend));
  app.use(session(config, backend));
  app.use((req, res, next) => {
    res.locals.flash = FLASH[req.query.m] ?? null;
    res.locals.path = req.path;
    next();
  });

  const ctx = { config, backend, fmt };
  authRoutes(app, ctx);
  setupRoutes(app, ctx);
  accountRoutes(app, ctx);
  patientRoutes(app, ctx);
  bookingRoutes(app, ctx);
  staffRoutes(app, ctx);
  practiceRoutes(app, ctx);
  adminRoutes(app, ctx);

  app.use((req, res) => res.status(404).render('error.njk', { title: 'Nicht gefunden', message: 'Diese Seite gibt es nicht.' }));
  app.use(errorHandler(config, log));
  return app;
}

function errorHandler(config, log) {
  return (err, req, res, next) => {
    if (res.headersSent) return next(err);
    if (err instanceof HttpError) {
      if (err.status === 401) {
        clearSession(config, res);
        const target = req.method === 'GET' ? safeNext(req.originalUrl, '/') : '/';
        return res.redirect(`/login?m=expired&next=${encodeURIComponent(target)}`);
      }
      const page = {
        403: ['Keine Berechtigung', 'Für diese Seite oder Aktion haben Sie keine Berechtigung.'],
        404: ['Nicht gefunden', 'Der Eintrag wurde nicht gefunden.'],
        409: ['Nicht möglich', problemMessage(err.problem, 'Die Aktion ist im aktuellen Zustand nicht möglich.')],
        422: ['Eingabe ungültig', problemMessage(err.problem, 'Die Eingabe ist ungültig.')],
        429: ['Zu viele Anfragen', problemMessage(err.problem, 'Bitte warten Sie einen Moment.')],
      }[err.status];
      if (page) return res.status(err.status).render('error.njk', { title: page[0], message: page[1] });
      log({ level: 'error', msg: 'backend answered unexpectedly', status: err.status, path: req.path });
      return res.status(502).render('error.njk', { title: 'Dienst nicht verfügbar', message: 'Ein Dienst hat unerwartet geantwortet. Bitte versuchen Sie es später noch einmal.' });
    }
    if (err instanceof BackendUnavailable) {
      log({ level: 'error', msg: 'backend not reachable', service: err.service, path: req.path });
      return res.status(503).render('error.njk', { title: 'Dienst nicht erreichbar', message: 'Die Anwendung ist vorübergehend nicht erreichbar. Bitte versuchen Sie es später noch einmal.' });
    }
    if (err?.type === 'entity.too.large') {
      return res.status(413).render('error.njk', { title: 'Eingabe zu groß', message: 'Die Eingabe ist zu groß.' });
    }
    log({ level: 'error', msg: 'unhandled error', path: req.path, error: String(err?.message ?? err) });
    res.status(500).render('error.njk', { title: 'Fehler', message: 'Es ist ein unerwarteter Fehler aufgetreten.' });
  };
}

// One JSON line per request: method, path (never the query: it can carry a one-time token), status, duration.
function accessLog(log) {
  return (req, res, next) => {
    const start = Date.now();
    res.on('finish', () => {
      if (req.path !== '/healthz' && !req.path.startsWith('/static/')) {
        log({ level: 'info', msg: 'request', method: req.method, path: req.path, status: res.statusCode, ms: Date.now() - start });
      }
    });
    next();
  };
}

function defaultLog(entry) {
  console.log(JSON.stringify({ time: new Date().toISOString(), service: 'frontend_provider', ...entry }));
}
