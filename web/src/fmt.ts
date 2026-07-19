// fmt.ts — small formatting helpers.

/** duration renders whole seconds as a compact human duration. */
export function duration(seconds: number): string {
  if (seconds < 1) return '0s';
  const d = Math.floor(seconds / 86400);
  const hh = Math.floor((seconds % 86400) / 3600);
  const mm = Math.floor((seconds % 3600) / 60);
  if (d > 0) return hh > 0 ? `${d}d ${hh}h` : `${d}d`;
  if (hh > 0) return mm > 0 ? `${hh}h ${mm}m` : `${hh}h`;
  if (mm > 0) return `${mm}m`;
  return `${Math.floor(seconds)}s`;
}

/** num renders a score-ish float compactly (2 decimals, trimmed). */
export function num(x: number): string {
  if (Number.isInteger(x)) return String(x);
  return x.toFixed(2).replace(/\.?0+$/, '');
}

/** ts renders an event timestamp for display (local time). */
export function ts(s: string): string {
  const d = new Date(s);
  if (isNaN(d.getTime())) return s;
  return d.toLocaleString(undefined, {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
  });
}
