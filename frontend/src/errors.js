// German texts for the problem answers of the backends (application/problem+json, api/CONVENTIONS.md).
// Only the stable `code` and the field names are used, never a message from the backend, and never a rejected value.

export class HttpError extends Error {
  constructor(status, problem) {
    super(problem?.code || `HTTP ${status}`);
    this.status = status;
    this.problem = problem;
  }
}

const CODES = {
  invalid_credentials: 'E-Mail-Adresse oder Passwort stimmen nicht, oder das Konto ist vorübergehend gesperrt.',
  invalid_current_password: 'Das aktuelle Passwort ist nicht richtig.',
  temporarily_locked: 'Zu viele Fehlversuche. Bitte warten Sie einen Moment.',
  rate_limited: 'Zu viele Anfragen. Bitte warten Sie einen Moment und versuchen Sie es noch einmal.',
  invalid_token: 'Der Link ist ungültig, abgelaufen oder wurde schon verwendet.',
  invalid_code: 'Der Code ist nicht richtig.',
  mfa_active: 'Die Zwei-Faktor-Anmeldung ist bereits eingerichtet. Entfernen Sie sie zuerst, um sie neu einzurichten.',
  mfa_not_active: 'Die Zwei-Faktor-Anmeldung ist nicht eingerichtet.',
  no_setup: 'Starten Sie zuerst die Einrichtung.',
  email_taken: 'Diese E-Mail-Adresse wird bereits verwendet.',
  slot_taken: 'Dieser Termin wurde gerade vergeben. Bitte wählen Sie einen anderen.',
  doctor_unavailable: 'Die gewählte Ärztin / der gewählte Arzt ist zu dieser Zeit nicht verfügbar.',
  outside_working_time: 'Die Ärztin / der Arzt arbeitet zu dieser Zeit nicht.',
  doctor_not_offering: 'Diese Ärztin / dieser Arzt bietet die Leistung nicht an.',
  starts_in_past: 'Der Termin muss in der Zukunft liegen.',
  booking_in_past: 'Vergangene Termine können nicht mehr geändert werden.',
  not_a_doctor: 'Das ist keine Ärztin / kein Arzt.',
  not_a_patient: 'Adressdaten gibt es nur für Patient:innen.',
  duplicate: 'Dieser Eintrag existiert bereits.',
  in_use: 'Der Eintrag wird noch verwendet und kann nicht gelöscht werden.',
  is_other: 'Die Leistung „Sonstiges“ kann nicht gelöscht werden.',
  specialty_required: 'Eine Leistung braucht eine Fachrichtung.',
  unknown_specialty: 'Eine Fachrichtung existiert nicht.',
  unknown_reference: 'Ein verknüpfter Eintrag existiert nicht oder wird noch verwendet.',
  working_days_overlap: 'Arbeitszeiten am selben Tag dürfen sich nicht überschneiden.',
  end_before_start: 'Ein Arbeitsblock muss nach seinem Beginn enden.',
  systemadmin_exclusive: 'Die Rolle Systemadministration kann nicht mit anderen Rollen kombiniert werden.',
  own_roles: 'Administrator:innen können ihre eigenen Rollen nicht ändern.',
  own_account: 'Das eigene Konto löschen Sie unter „Mein Konto“.',
  last_admin: 'Die letzte Systemadministration kann nicht gelöscht oder herabgestuft werden.',
  c_before_b: 'Frist C darf nicht kürzer sein als Frist B.',
  whole_minutes: 'Die Uhrzeit muss auf die Minute genau sein.',
  password_unchanged: 'Das neue Passwort muss sich vom alten unterscheiden.',
  payload_too_large: 'Die Eingabe ist zu groß.',
  forbidden: 'Dafür haben Sie keine Berechtigung.',
  not_found: 'Der Eintrag wurde nicht gefunden.',
};

const FIELD_CODES = {
  min_length: 'ist zu kurz',
  max_length: 'ist zu lang',
  pattern: 'hat ein ungültiges Format',
  format: 'hat ein ungültiges Format',
  minimum: 'ist zu klein',
  maximum: 'ist zu groß',
  enum: 'ist ungültig',
  required: 'fehlt',
  additional_properties: 'ist nicht erlaubt',
  password_too_short: 'ist zu kurz (mindestens 15 Zeichen)',
  password_too_long: 'ist zu lang (höchstens 512 Zeichen)',
  password_blocklisted: 'steht auf der Liste häufig verwendeter oder schwacher Passwörter',
  password_contains_account_name: 'darf Ihren Kontonamen nicht enthalten',
  password_unchanged: 'muss sich vom alten Passwort unterscheiden',
  c_before_b: 'darf nicht kürzer sein als Frist B',
  email_unchanged: 'ist die aktuelle Adresse',
  hash_mismatch: 'wurde beim Speichern verändert',
};

const FIELD_LABELS = {
  password: 'Passwort', new_password: 'Das neue Passwort', current_password: 'Das aktuelle Passwort', email: 'Die E-Mail-Adresse',
  new_email: 'Die neue E-Mail-Adresse', first_name: 'Der Vorname', last_name: 'Der Nachname', phone: 'Die Telefonnummer',
  name: 'Der Name', duration_minutes: 'Die Dauer', reason: 'Der Grund', info: 'Die Anmerkung', code: 'Der Code',
};

// A message for the top of the page.
export function problemMessage(problem, fallback = 'Die Anfrage war nicht erfolgreich.') {
  if (!problem) return fallback;
  if (CODES[problem.code]) return CODES[problem.code];
  const first = problem.errors?.[0];
  if (first) return fieldMessage(first);
  return fallback;
}

function fieldMessage({ field, code }) {
  const name = String(field ?? '').split('.').pop();
  const label = FIELD_LABELS[name] ?? 'Die Eingabe';
  return `${label} ${FIELD_CODES[code] ?? 'ist ungültig'}.`;
}

// Messages per field name, for the forms.
export function fieldErrors(problem) {
  const out = {};
  for (const e of problem?.errors ?? []) {
    const name = String(e.field ?? '').split('.').pop();
    (out[name] ??= []).push(fieldMessage(e));
  }
  return out;
}

export const FLASH = {
  registered: 'Wir haben Ihnen eine E-Mail mit einem Bestätigungslink geschickt. Falls die Adresse neu ist, ist sie in wenigen Minuten da.',
  verified: 'Ihre E-Mail-Adresse ist bestätigt. Sie können sich jetzt anmelden.',
  reset_sent: 'Wenn zu dieser Adresse ein Konto existiert, haben wir einen Link zum Zurücksetzen geschickt.',
  password_reset: 'Das Passwort wurde geändert. Alle bisherigen Sitzungen wurden beendet. Bitte melden Sie sich neu an.',
  expired: 'Ihre Sitzung ist abgelaufen. Bitte melden Sie sich neu an.',
  logged_out: 'Sie wurden abgemeldet.',
  deleted: 'Ihr Konto wurde gelöscht.',
  mfa_removed: 'Die Zwei-Faktor-Anmeldung wurde entfernt. Alle Sitzungen wurden beendet. Bitte melden Sie sich neu an.',
  email_changed: 'Die E-Mail-Adresse wurde geändert. Bitte melden Sie sich mit der neuen Adresse an.',
  email_requested: 'Wir haben einen Bestätigungslink an die neue Adresse geschickt.',
  profile_saved: 'Ihre Daten wurden gespeichert.',
  password_saved: 'Das Passwort wurde geändert.',
  booked: 'Der Termin ist gebucht. Eine Bestätigung ist per E-Mail unterwegs.',
  cancelled: 'Der Termin wurde storniert.',
  rescheduled: 'Der Termin wurde verschoben.',
  assigned: 'Die Ärztin / der Arzt wurde zugeordnet.',
  saved: 'Gespeichert.',
  created: 'Angelegt.',
  deleted_entry: 'Gelöscht.',
  user_created: 'Das Konto wurde angelegt. Die Person erhält einen Link zum Festlegen des Passworts.',
  roles_saved: 'Die Rollen wurden gespeichert. Die Sitzungen der Person wurden beendet.',
  reset_requested: 'Die Person erhält einen Link zum Zurücksetzen des Passworts. Ihre Sitzungen wurden beendet.',
  unlocked: 'Das Konto wurde freigeschaltet.',
  main_doctor_saved: 'Der Hauptarzt wurde gespeichert.',
};
