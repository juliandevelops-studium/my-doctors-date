// Dates and labels for the German user interface. All times are shown in the time zone of the practice.

export function makeFormatters(timezone) {
  const dateTime = new Intl.DateTimeFormat('de-DE', { timeZone: timezone, weekday: 'short', day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit' });
  const date = new Intl.DateTimeFormat('de-DE', { timeZone: timezone, weekday: 'long', day: '2-digit', month: '2-digit', year: 'numeric' });
  const dayShort = new Intl.DateTimeFormat('de-DE', { timeZone: timezone, weekday: 'short', day: '2-digit', month: '2-digit' });
  const time = new Intl.DateTimeFormat('de-DE', { timeZone: timezone, hour: '2-digit', minute: '2-digit' });
  const ymd = new Intl.DateTimeFormat('en-CA', { timeZone: timezone, year: 'numeric', month: '2-digit', day: '2-digit' });
  const parts = new Intl.DateTimeFormat('en-US', { timeZone: timezone, hourCycle: 'h23', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' });
  const toDate = (v) => (v instanceof Date ? v : new Date(v));
  const valid = (d) => !Number.isNaN(d.getTime());

  // Offset of the time zone at an instant, in milliseconds (positive east of UTC).
  function offsetAt(instant) {
    const p = Object.fromEntries(parts.formatToParts(instant).map((x) => [x.type, x.value]));
    const asUtc = Date.UTC(+p.year, +p.month - 1, +p.day, +p.hour, +p.minute, +p.second);
    return asUtc - Math.floor(instant.getTime() / 1000) * 1000;
  }

  return {
    dateTime: (v) => (v ? (valid(toDate(v)) ? dateTime.format(toDate(v)).replaceAll(',', '') + ' Uhr' : '') : ''),
    date: (v) => (v && valid(toDate(v)) ? date.format(toDate(v)) : ''),
    dayShort: (v) => (v && valid(toDate(v)) ? dayShort.format(toDate(v)).replaceAll(',', '') : ''),
    time: (v) => (v && valid(toDate(v)) ? time.format(toDate(v)) + ' Uhr' : ''),
    // 2030-01-02 in the time zone of the practice
    ymd: (v) => ymd.format(toDate(v)),
    // The instant of 00:00 (practice time) on a YYYY-MM-DD date. Null for anything that is not a real date.
    startOfDay(str) {
      if (!/^\d{4}-\d{2}-\d{2}$/.test(str ?? '')) return null;
      const guess = new Date(`${str}T00:00:00Z`);
      if (!valid(guess) || guess.toISOString().slice(0, 10) !== str) return null;
      let instant = new Date(guess.getTime() - offsetAt(guess));
      instant = new Date(guess.getTime() - offsetAt(instant)); // second pass at DST changes
      return instant;
    },
    addDays(str, n) {
      const d = new Date(`${str}T00:00:00Z`);
      d.setUTCDate(d.getUTCDate() + n);
      return d.toISOString().slice(0, 10);
    },
  };
}

export const ROLE_LABELS = {
  patient: 'Patient:in',
  doctor: 'Ärztin/Arzt',
  desk: 'Empfang',
  organisator: 'Organisation',
  systemadmin: 'Systemadministration',
};

export const STATUS_LABELS = {
  bestaetigt: 'bestätigt',
  angefragt: 'angefragt',
  storniert: 'storniert',
  abgeschlossen: 'abgeschlossen',
  pending: 'E-Mail nicht bestätigt',
  active: 'aktiv',
  locked: 'gesperrt',
  disabled: 'deaktiviert',
};

export const WEEKDAYS = [
  { n: 1, key: 'mon', label: 'Montag' },
  { n: 2, key: 'tue', label: 'Dienstag' },
  { n: 3, key: 'wed', label: 'Mittwoch' },
  { n: 4, key: 'thu', label: 'Donnerstag' },
  { n: 5, key: 'fri', label: 'Freitag' },
  { n: 6, key: 'sat', label: 'Samstag' },
  { n: 7, key: 'sun', label: 'Sonntag' },
];
