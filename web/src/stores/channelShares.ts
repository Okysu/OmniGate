import type { IncomingShare } from '@/lib/types'
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { isApiError } from '@/lib/api'
import { channelSharesApi } from '@/lib/endpoints'
import { pendingCount, updateIncoming } from '@/lib/shares'

/**
 * Channels shared with the current user (phase5-api.md §5.3): the "共享给我的" tab and its
 * pending-invitation counter. Accept / decline / leave update the list in place.
 */
export const useChannelSharesStore = defineStore('channelShares', () => {
  const items = ref<IncomingShare[] | null>(null)
  const loading = ref(false)
  const error = ref<unknown>(null)
  /** False once the backend answered 404 (no share acceptance yet): the tab is hidden. */
  const available = ref(true)
  let inflight: Promise<void> | null = null

  const pending = computed(() => pendingCount(items.value))

  async function fetchList(): Promise<void> {
    loading.value = true
    error.value = null
    try {
      items.value = (await channelSharesApi.list()).items
      available.value = true
    }
    catch (err) {
      if (isApiError(err) && err.status === 404) {
        available.value = false
        items.value = []
      }
      else {
        error.value = err
      }
    }
    finally {
      loading.value = false
    }
  }

  function load(): Promise<void> {
    inflight ??= fetchList().finally(() => {
      inflight = null
    })
    return inflight
  }

  async function accept(channelId: string): Promise<IncomingShare> {
    const next = await channelSharesApi.accept(channelId)
    items.value = updateIncoming(items.value ?? [], channelId, next)
    return next
  }

  async function decline(channelId: string): Promise<void> {
    await channelSharesApi.decline(channelId)
    items.value = updateIncoming(items.value ?? [], channelId, null)
  }

  async function leave(channelId: string): Promise<void> {
    await channelSharesApi.leave(channelId)
    items.value = updateIncoming(items.value ?? [], channelId, null)
  }

  return { items, loading, error, available, pending, load, accept, decline, leave }
})
