import { useDocumentVisibility, useIntervalFn } from '@vueuse/core'
import { computed, onMounted, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { UNREAD_POLL_MS, useNotificationsStore } from '@/stores/notifications'

/**
 * Keeps the unread notification count fresh: fetch on mount, then every 60 s while the
 * tab is visible and the user is signed in. Hidden tabs do not poll; becoming visible
 * again fetches immediately. Mount once (the console sidebar does).
 */
export function useUnreadPolling() {
  const store = useNotificationsStore()
  const auth = useAuthStore()
  const visibility = useDocumentVisibility()
  const active = computed(() => auth.isAuthenticated && store.available && visibility.value === 'visible')

  const { pause, resume } = useIntervalFn(() => {
    void store.refresh()
  }, UNREAD_POLL_MS, { immediate: false })

  watch(active, (on, was) => {
    if (on) {
      if (was === false)
        void store.refresh()
      resume()
    }
    else {
      pause()
    }
  })

  onMounted(() => {
    if (auth.isAuthenticated)
      void store.refresh()
    if (active.value)
      resume()
  })

  return { unread: computed(() => store.unread) }
}
