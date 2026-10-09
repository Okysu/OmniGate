import type { CustomMeterInfo } from '@/lib/customMeters'
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { isApiError } from '@/lib/api'
import { collectMeterOptions, optionsFromMeterList } from '@/lib/customMeters'
import { billingMetersApi, pluginsApi } from '@/lib/endpoints'
import { useAuthStore } from './auth'

/**
 * Plan rule meters provided by billing plugins (phase9 §3), from
 * `GET /api/admin/billing/meters` (billing.manage). Backends without that
 * endpoint (404 / 405): derived from the plugin list and the latest approved
 * versions' manifests (needs `plugins.read`). Without access the list stays
 * empty and summaries fall back to the bare meter name. Loaded on demand and cached.
 */
export const useCustomMetersStore = defineStore('customMeters', () => {
  const items = ref<CustomMeterInfo[] | null>(null)
  const loading = ref(false)
  const error = ref<unknown>(null)
  let inflight: Promise<void> | null = null

  const byId = computed(() => new Map((items.value ?? []).map(m => [m.id, m])))
  /** Grouped by plugin for the meter select. */
  const groups = computed(() => {
    const map = new Map<string, { pluginKey: string, pluginName: string, meters: CustomMeterInfo[] }>()
    for (const m of items.value ?? []) {
      let g = map.get(m.pluginKey)
      if (!g) {
        g = { pluginKey: m.pluginKey, pluginName: m.pluginName || m.pluginKey, meters: [] }
        map.set(m.pluginKey, g)
      }
      g.meters.push(m)
    }
    return [...map.values()].sort((a, b) => a.pluginName.localeCompare(b.pluginName, 'zh-CN'))
  })

  async function fromPlugins(): Promise<void> {
    if (!useAuthStore().can('plugins.read')) {
      items.value = []
      return
    }
    const list = (await pluginsApi.list()).items
    items.value = await collectMeterOptions(list, async (id, vid) => (await pluginsApi.version(id, vid)).manifest)
  }

  async function fetch(): Promise<void> {
    const auth = useAuthStore()
    if (!auth.can('billing.manage') && !auth.can('plugins.read')) {
      items.value = []
      return
    }
    loading.value = true
    try {
      if (auth.can('billing.manage')) {
        try {
          items.value = optionsFromMeterList(await billingMetersApi.list())
        }
        catch (err) {
          if (!(isApiError(err) && (err.status === 404 || err.status === 405 || err.status === 403)))
            throw err
          await fromPlugins()
        }
      }
      else {
        await fromPlugins()
      }
      error.value = null
    }
    catch (err) {
      error.value = err
      items.value ??= []
    }
    finally {
      loading.value = false
    }
  }

  function load(force = false): Promise<void> {
    if (items.value && !force)
      return Promise.resolve()
    inflight ??= fetch().finally(() => {
      inflight = null
    })
    return inflight
  }

  return { items, loading, error, byId, groups, load }
})
