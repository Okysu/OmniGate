import { defineStore } from 'pinia'
import { ref } from 'vue'
import { isApiError } from '@/lib/api'
import { notificationsApi } from '@/lib/endpoints'

/** Poll interval of the sidebar unread badge (phase6-api.md §4). */
export const UNREAD_POLL_MS = 60_000

/**
 * Unread in-app notification count for the sidebar badge. Polled every 60 s while the
 * tab is visible (see `useUnreadPolling`); pages that change read state call `refresh()`
 * or `set()` so the badge updates immediately.
 */
export const useNotificationsStore = defineStore('notifications', () => {
  const unread = ref(0)
  /** False once the backend answered 404 / 501 (notifications not available): polling stops. */
  const available = ref(true)
  let inflight: Promise<void> | null = null

  async function fetchCount(): Promise<void> {
    try {
      const res = await notificationsApi.unreadCount()
      unread.value = typeof res?.count === 'number' && res.count > 0 ? res.count : 0
      available.value = true
    }
    catch (err) {
      if (isApiError(err) && (err.status === 404 || err.status === 501))
        available.value = false
      // Other failures keep the last known count; the next poll retries.
    }
  }

  function refresh(): Promise<void> {
    inflight ??= fetchCount().finally(() => {
      inflight = null
    })
    return inflight
  }

  function set(count: number): void {
    unread.value = Math.max(0, count)
  }

  /** Optimistic local decrement after marking items read. */
  function decrement(by = 1): void {
    unread.value = Math.max(0, unread.value - by)
  }

  return { unread, available, refresh, set, decrement }
})
