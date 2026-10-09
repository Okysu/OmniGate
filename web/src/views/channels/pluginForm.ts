import type { ConfigValues, SecretValues } from '@/lib/configSchema'
import type { PluginManifest } from '@/lib/types'
import { buildPluginConfig, buildSecrets, validatePluginConfig } from '@/lib/configSchema'

/** Plugin part of the channel form (owned by ChannelFormSheet, edited by PluginSection). */
export interface PluginFormState {
  /** Plugin row id. */
  pluginId: string | null
  /** Selected version (create) / pinned or target version (edit). */
  versionId: string | null
  /** Version the channel is pinned to when the sheet opened (edit). */
  originalVersionId: string | null
  manifest: PluginManifest | null
  loading: boolean
  error: string | null
  config: ConfigValues
  secrets: SecretValues
  /** The user edited a config field (only then is pluginConfig sent). */
  configTouched: boolean
}

export function emptyPluginForm(): PluginFormState {
  return { pluginId: null, versionId: null, originalVersionId: null, manifest: null, loading: false, error: null, config: {}, secrets: {}, configTouched: false }
}

export function pluginVersionChanged(s: PluginFormState): boolean {
  return s.originalVersionId !== null && s.versionId !== null && s.versionId !== s.originalVersionId
}

export function validatePluginForm(s: PluginFormState, secretsSet?: Record<string, { set: boolean }> | null): Record<string, string> {
  if (!s.manifest)
    return {}
  const errors = validatePluginConfig(s.manifest.configSchema, s.config, s.secrets, { secretsSet })
  // While switching versions without touching the config, the server migrates
  // the stored config; only secrets are checked client-side then.
  if (pluginVersionChanged(s) && !s.configTouched) {
    for (const k of Object.keys(errors)) {
      if (k.startsWith('pluginConfig.'))
        delete errors[k]
    }
  }
  return errors
}

/** Plugin fields of the create / update payload. */
export function pluginPayload(s: PluginFormState, create: boolean): { pluginVersionId?: string, pluginConfig?: Record<string, unknown>, secrets?: Record<string, string> } {
  const out: { pluginVersionId?: string, pluginConfig?: Record<string, unknown>, secrets?: Record<string, string> } = {}
  if (!s.versionId || !s.manifest)
    return out
  if (create || pluginVersionChanged(s))
    out.pluginVersionId = s.versionId
  if (create ? Object.keys(s.config).length > 0 : s.configTouched)
    out.pluginConfig = buildPluginConfig(s.manifest.configSchema, s.config)
  const secrets = buildSecrets(s.secrets)
  if (secrets)
    out.secrets = secrets
  return out
}
