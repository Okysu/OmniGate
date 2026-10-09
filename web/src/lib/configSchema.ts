// Client side of the plugin configSchema subset (phase2-api.md §2.1): field
// list, form state, validation mirroring server/internal/plugin/manifest.go
// (ValidateConfig / SchemaProperty.check) and request payloads.
// Error keys use the server's 422 detail keys: `pluginConfig.<name>` / `secrets.<name>`.
import type { ConfigSchema, SchemaProperty } from './types'

export interface ConfigField {
  name: string
  prop: SchemaProperty
  required: boolean
  secret: boolean
  group: string
}

/** Fields in schema order (the server sorts keys), secrets included. */
export function schemaFields(schema: ConfigSchema | null | undefined): ConfigField[] {
  if (!schema?.properties)
    return []
  const required = new Set(schema.required ?? [])
  return Object.entries(schema.properties).map(([name, prop]) => ({
    name,
    prop,
    required: required.has(name),
    secret: prop['x-secret'] === true,
    group: prop['x-group'] ?? '',
  }))
}

/** Groups non-secret fields by `x-group` ("" first), keeping order. */
export function groupFields(fields: ConfigField[]): Array<{ group: string, fields: ConfigField[] }> {
  const out: Array<{ group: string, fields: ConfigField[] }> = []
  for (const f of fields) {
    let g = out.find(x => x.group === f.group)
    if (!g) {
      g = { group: f.group, fields: [] }
      out.push(g)
    }
    g.fields.push(f)
  }
  return out.sort((a, b) => (a.group === '' ? -1 : b.group === '' ? 1 : 0))
}

/** Form values: booleans as booleans, everything else as the input's string. */
export type ConfigValues = Record<string, string | boolean>

export interface SecretInput {
  value: string
  /** On edit, a set secret is kept unless the user turns on "更换". */
  replace: boolean
}

export type SecretValues = Record<string, SecretInput>

function valueToInput(prop: SchemaProperty, v: unknown): string | boolean {
  if (prop.type === 'boolean')
    return v === true
  if (v === null || v === undefined)
    return ''
  return String(v)
}

/** Initial values: existing config, else schema default, else empty. */
export function initConfigValues(schema: ConfigSchema | null | undefined, existing?: Record<string, unknown> | null): ConfigValues {
  const out: ConfigValues = {}
  for (const f of schemaFields(schema)) {
    if (f.secret)
      continue
    const has = existing != null && Object.prototype.hasOwnProperty.call(existing, f.name)
    out[f.name] = valueToInput(f.prop, has ? existing[f.name] : f.prop.default)
  }
  return out
}

export function initSecretValues(schema: ConfigSchema | null | undefined, set?: Record<string, { set: boolean }> | null): SecretValues {
  const out: SecretValues = {}
  for (const f of schemaFields(schema)) {
    if (f.secret)
      out[f.name] = { value: '', replace: !set?.[f.name]?.set }
  }
  return out
}

function runeLength(s: string): number {
  return Array.from(s).length
}

function isEmpty(v: string | boolean | undefined): boolean {
  return v === undefined || (typeof v === 'string' && v.trim() === '')
}

/** Converts an input to its typed value; null when it cannot be parsed. */
function parseValue(prop: SchemaProperty, raw: string | boolean): unknown {
  switch (prop.type) {
    case 'boolean':
      return raw === true
    case 'number':
    case 'integer': {
      const s = String(raw).trim()
      if (!/^-?(?:\d+\.?\d*|\.\d+)(?:e[+-]?\d+)?$/i.test(s))
        return null
      const n = Number(s)
      return Number.isFinite(n) ? n : null
    }
    default:
      return String(raw)
  }
}

/** The server validated the pattern with Go's RE2; patterns JS cannot parse are skipped. */
function compilePattern(pattern: string): RegExp | null {
  try {
    return new RegExp(pattern)
  }
  catch {
    return null
  }
}

/** Mirrors SchemaProperty.check (manifest.go); returns "" when valid. */
export function checkValue(prop: SchemaProperty, raw: string | boolean): string {
  const v = parseValue(prop, raw)
  switch (prop.type) {
    case 'string': {
      const s = v as string
      if ((prop.minLength != null && runeLength(s) < prop.minLength) || (prop.maxLength != null && runeLength(s) > prop.maxLength)) {
        if (prop.minLength != null && prop.maxLength != null)
          return `长度应为 ${prop.minLength}–${prop.maxLength} 个字符`
        return prop.minLength != null ? `至少 ${prop.minLength} 个字符` : `最多 ${prop.maxLength} 个字符`
      }
      if (prop.pattern) {
        const re = compilePattern(prop.pattern)
        if (re && !re.test(s))
          return '格式不符合要求'
      }
      break
    }
    case 'number':
    case 'integer': {
      if (v === null)
        return '必须是数字'
      const n = v as number
      if (prop.type === 'integer' && !Number.isInteger(n))
        return '必须是整数'
      if ((prop.minimum != null && n < prop.minimum) || (prop.maximum != null && n > prop.maximum)) {
        if (prop.minimum != null && prop.maximum != null)
          return `范围为 ${prop.minimum}–${prop.maximum}`
        return prop.minimum != null ? `不能小于 ${prop.minimum}` : `不能大于 ${prop.maximum}`
      }
      break
    }
  }
  if (prop.enum?.length && !prop.enum.some(e => String(e) === String(v)))
    return '不在允许的取值范围内'
  return ''
}

export interface ValidateOptions {
  /** Secrets already stored on the server (edit). */
  secretsSet?: Record<string, { set: boolean }> | null
}

/** Validates config + secrets; keys are `pluginConfig.<name>` / `secrets.<name>`. */
export function validatePluginConfig(schema: ConfigSchema | null | undefined, values: ConfigValues, secrets: SecretValues, opts: ValidateOptions = {}): Record<string, string> {
  const errors: Record<string, string> = {}
  for (const f of schemaFields(schema)) {
    if (f.secret) {
      const s = secrets[f.name]
      const typed = s !== undefined && s.replace && s.value !== ''
      if (f.required && !typed && !opts.secretsSet?.[f.name]?.set)
        errors[`secrets.${f.name}`] = '必填'
      else if (typed && /[\r\n]/.test(s.value))
        errors[`secrets.${f.name}`] = '不能包含换行'
      continue
    }
    const raw = values[f.name]
    if (f.prop.type !== 'boolean' && isEmpty(raw)) {
      // Omitted values get the schema default on the server.
      if (f.required && f.prop.default === undefined)
        errors[`pluginConfig.${f.name}`] = '必填'
      continue
    }
    const msg = checkValue(f.prop, raw ?? '')
    if (msg)
      errors[`pluginConfig.${f.name}`] = msg
  }
  return errors
}

/**
 * Builds `pluginConfig` (whole-object replace). Empty optional inputs are
 * omitted so the server applies the schema default; enum values keep the type
 * of the matching enum entry.
 */
export function buildPluginConfig(schema: ConfigSchema | null | undefined, values: ConfigValues): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const f of schemaFields(schema)) {
    if (f.secret)
      continue
    const raw = values[f.name]
    if (raw === undefined || (f.prop.type !== 'boolean' && isEmpty(raw)))
      continue
    let v = parseValue(f.prop, typeof raw === 'string' ? raw.trim() : raw)
    if (v === null)
      continue
    if (f.prop.enum?.length) {
      const match = f.prop.enum.find(e => String(e) === String(v))
      if (match !== undefined)
        v = match
    }
    out[f.name] = v
  }
  return out
}

/** Secrets to send: only fields with a newly typed value (empty = keep). */
export function buildSecrets(secrets: SecretValues): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  for (const [name, s] of Object.entries(secrets)) {
    if (s.replace && s.value !== '')
      out[name] = s.value
  }
  return Object.keys(out).length > 0 ? out : undefined
}
