import type { ApiError } from '@/lib/api'
import type { SystemInfo } from '@/lib/types'
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { isApiError } from '@/lib/api'
import { systemApi } from '@/lib/endpoints'

export type SystemStatus = 'unknown' | 'ok' | 'error'

export const useSystemStore = defineStore('system', () => {
  const info = ref<SystemInfo | null>(null)
  const status = ref<SystemStatus>('unknown')
  const error = ref<ApiError | null>(null)
  let inflight: Promise<void> | null = null

  async function load(): Promise<void> {
    try {
      info.value = await systemApi.info()
      status.value = 'ok'
      error.value = null
    }
    catch (err) {
      status.value = 'error'
      error.value = isApiError(err) ? err : null
    }
  }

  function ensureLoaded(): Promise<void> {
    if (info.value)
      return Promise.resolve()
    inflight ??= load().finally(() => {
      inflight = null
    })
    return inflight
  }

  return { info, status, error, load, ensureLoaded }
})
