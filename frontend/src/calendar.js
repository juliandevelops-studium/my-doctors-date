// Helpers for week views and slot lists.
import { WEEKDAYS } from './format.js';

// The week that starts on the given day (default: today in the practice time zone). Past weeks are only allowed when
// requested (the doctor's calendar); booking never offers the past anyway.
export function weekRange(fmt, fromParam, { allowPast = false } = {}) {
  const today = fmt.ymd(new Date());
  let from = fmt.startOfDay(fromParam) ? fromParam : today;
  if (!allowPast && from < today) from = today;
  const next = fmt.addDays(from, 7);
  return {
    from,
    to: next,
    fromInstant: fmt.startOfDay(from),
    toInstant: fmt.startOfDay(next),
    prev: !allowPast && fmt.addDays(from, -7) < today ? null : fmt.addDays(from, -7),
    next,
    today,
  };
}

// Groups items with a starts_at by day in the practice time zone: [{ day, label, items }]
export function groupByDay(items, fmt) {
  const days = new Map();
  for (const item of items) {
    const day = fmt.ymd(item.starts_at);
    if (!days.has(day)) days.set(day, { day, label: fmt.date(item.starts_at), items: [] });
    days.get(day).items.push(item);
  }
  return [...days.values()];
}

// The day as a timeline of blocks: free slots (bookable) and the busy gaps between them, each with its length in
// minutes so the view can size them proportionally: [{ type: 'free'|'busy', starts_at, ends_at, minutes, doctor_id? }]
function dayBlocks(items) {
  const sorted = [...items].sort((a, b) => new Date(a.starts_at) - new Date(b.starts_at));
  const blocks = [];
  let cursor = null;
  for (const s of sorted) {
    const start = new Date(s.starts_at).getTime();
    const end = new Date(s.ends_at).getTime();
    if (cursor !== null && start > cursor) {
      blocks.push({ type: 'busy', starts_at: new Date(cursor).toISOString(), ends_at: s.starts_at, minutes: (start - cursor) / 60000 });
    }
    blocks.push({ type: 'free', starts_at: s.starts_at, ends_at: s.ends_at, minutes: (end - start) / 60000, doctor_id: s.doctor_id });
    cursor = Math.max(cursor ?? 0, end);
  }
  return blocks;
}

// All seven days of the week (Mon-Sun) as calendar columns, each with its timeline of free/busy blocks. Sundays and
// past days are marked `off` (greyed out, not bookable): [{ day, label, weekday, blocks, off }]
export function weekCalendar(range, items, fmt) {
  const byDay = new Map(groupByDay(items, fmt).map((d) => [d.day, d.items]));
  const days = [];
  for (let i = 0; i < 7; i++) {
    const day = fmt.addDays(range.from, i);
    const wd = new Date(`${day}T00:00:00Z`).getUTCDay(); // 0 = Sunday
    const iso = wd === 0 ? 7 : wd; // Monday = 1 ... Sunday = 7
    const dayItems = byDay.get(day) ?? [];
    days.push({
      day,
      label: fmt.dayShort(day),
      weekday: WEEKDAYS[iso - 1].label,
      blocks: dayBlocks(dayItems),
      off: iso === 7 || day < range.today,
    });
  }
  return days;
}

// RFC 3339 date-time as sent by the backends (offset or Z). Anything else is refused before it reaches a backend.
export const INSTANT = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;
