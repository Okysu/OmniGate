// Tiny CSV builder + client-side download (no data leaves the browser).

function escapeCell(value: string | number | null | undefined): string {
  const s = value == null ? '' : String(value)
  // Neutralise spreadsheet formula injection, then quote when needed.
  const safe = typeof value === 'string' && /^[=+\-@\t\r]/.test(s) ? `'${s}` : s
  return /[",\r\n]/.test(safe) ? `"${safe.replace(/"/g, '""')}"` : safe
}

/** RFC 4180 CSV (CRLF line endings). */
export function toCsv(rows: Array<Array<string | number | null | undefined>>): string {
  return rows.map(r => r.map(escapeCell).join(',')).join('\r\n')
}

/** Triggers a download of `content` via a temporary Blob URL. */
export function downloadText(filename: string, content: string, mime = 'text/csv;charset=utf-8'): void {
  // BOM so Excel opens UTF-8 (Chinese notes) correctly.
  const blob = new Blob(['﻿', content], { type: mime })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.rel = 'noopener'
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
