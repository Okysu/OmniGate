import type { ClientInfo } from '@/lib/types'
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { groupClients } from '@/lib/clients'
import { logsApi } from '@/lib/endpoints'

/**
 * Known clients (phase13-api.md §1) from `GET /api/clients` — the backend's detection list,
 * so the console never hardcodes it. Loaded on demand and cached; on failure the list stays
 * empty (filters then only offer "全部客户端", badges show the name the log carries).
 */
export const useClientsStore = defineStore('clients', () => {
  const items = ref<ClientInfo[] | null>(null)
  let inflight: Promise<void> | null = null

  const list = computed(() => items.value ?? [])
  const byId = computed(() => new Map(list.value.map(c => [c.id, c])))
  const groups = computed(() => groupClients(list.value))

  async function load(): Promise<void> {
    try {
      items.value = (await logsApi.clients()).items
    }
    catch {
      items.value = null
    }
  }

  function ensureLoaded(): Promise<void> {
    if (items.value)
      return Promise.resolve()
    inflight ??= load().finally(() => {
      inflight = null
    })
    return inflight
  }

  return { items, list, byId, groups, ensureLoaded }
})
