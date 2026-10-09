#!/usr/bin/env node
// Writes .br and .gz siblings for compressible files of a built web app, so the
// Go server can serve them with Content-Encoding instead of compressing at
// request time (see server/internal/webui). Variants that do not save at least
// 10% are skipped. Usage: node scripts/precompress.mjs <dir>
import { readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { brotliCompressSync, constants, gzipSync } from 'node:zlib'

const dir = process.argv[2]
if (!dir) {
  console.error('usage: precompress.mjs <dir>')
  process.exit(2)
}
const exts = /\.(?:js|mjs|css|html|svg|json|map|txt|xml|wasm|ttf|otf)$/i
let before = 0
let after = 0
let count = 0

function walk(d) {
  for (const name of readdirSync(d)) {
    const p = join(d, name)
    if (statSync(p).isDirectory()) {
      walk(p)
      continue
    }
    // index.html is served from memory with a per-version CSP; keep it plain.
    if (!exts.test(name) || name === 'index.html' || statSync(p).size < 1024)
      continue
    const raw = readFileSync(p)
    const variants = [
      ['.br', brotliCompressSync(raw, { params: { [constants.BROTLI_PARAM_QUALITY]: 11, [constants.BROTLI_PARAM_SIZE_HINT]: raw.length } })],
      ['.gz', gzipSync(raw, { level: 9 })],
    ]
    for (const [ext, data] of variants) {
      if (data.length > raw.length * 0.9)
        continue
      writeFileSync(p + ext, data)
      after += data.length
      count++
    }
    before += raw.length
  }
}

walk(dir)
console.log(`precompress: ${count} variants, ${(before / 1048576).toFixed(1)} MiB source -> ${(after / 1048576).toFixed(1)} MiB br+gz`)
