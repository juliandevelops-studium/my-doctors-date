// Client of the two backend services. It forwards the token of the user and the address of the browser, nothing else.
// The frontend never talks to database_middleware or mail_service (NetworkPolicy), and it makes no authorisation
// decisions: 401, 403 and 404 come from the backends.

export class BackendUnavailable extends Error {
  constructor(service, cause) {
    super(`${service} is not reachable`);
    this.service = service;
    this.cause = cause;
  }
}

export class Backend {
  constructor({ userServiceUrl, calendarServiceUrl, backendTimeoutMs = 10000, fetchImpl = fetch }) {
    this.urls = { user: userServiceUrl.replace(/\/$/, ''), calendar: calendarServiceUrl.replace(/\/$/, '') };
    this.timeout = backendTimeoutMs;
    this.fetch = fetchImpl;
  }

  // call('user'|'calendar', method, path, { token, body, query, ip }) -> { status, body, headers }
  async call(service, method, path, { token, body, query, ip } = {}) {
    const url = new URL(this.urls[service] + path);
    for (const [k, v] of Object.entries(query ?? {})) {
      if (v !== undefined && v !== null && v !== '') url.searchParams.set(k, String(v));
    }
    const headers = { Accept: 'application/json' };
    if (token) headers.Authorization = `Bearer ${token}`;
    if (ip) headers['X-Forwarded-For'] = ip;
    const init = { method, headers, signal: AbortSignal.timeout(this.timeout), redirect: 'error' };
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    let res;
    try {
      res = await this.fetch(url, init);
    } catch (err) {
      throw new BackendUnavailable(service, err);
    }
    const text = await res.text();
    let parsed = null;
    if (text) {
      try {
        parsed = JSON.parse(text);
      } catch {
        parsed = null;
      }
    }
    return { status: res.status, body: parsed, headers: res.headers };
  }
}
