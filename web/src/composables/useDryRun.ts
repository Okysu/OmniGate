import type { Ref } from 'vue'
import type { BulkSubscriptionResult } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { watchDebounced } from '@vueuse/core'
import { errorMessage } from '@/lib/api'

/**
 * Dry-run preview for a bulk subscription action (phase7-api.md §3.3): re-runs `run` with
 * `?dryRun=true` (debounced) whenever the request key changes while `active`, and exposes
 * whether the preview matches the current request (`ready`) — the confirm button stays
 * disabled until it does.
 */
export function useDryRun<B>(
  body: Ref<B | null>,
  /** Key of the parts of the body that change the affected set (not the note). */
  keyOf: (b: B) => string,
  run: (b: B) => Promise<BulkSubscriptionResult>,
  active: Ref<boolean>,
) {
  const preview = ref<{ key: string, affected: number } | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)
  let seq = 0
  /** Key of the newest request sent (skips the debounced duplicate of an immediate run). */
  let lastKey: string | null = null

  const currentKey = computed(() => (body.value ? keyOf(body.value) : null))

  async function refresh() {
    const b = body.value
    const mine = ++seq
    if (!b || !active.value) {
      loading.value = false
      error.value = null
      return
    }
    const key = keyOf(b)
    lastKey = key
    loading.value = true
    error.value = null
    try {
      const res = await run(b)
      if (mine === seq)
        preview.value = { key, affected: res.affected }
    }
    catch (err) {
      if (mine === seq) {
        preview.value = null
        error.value = errorMessage(err)
      }
    }
    finally {
      if (mine === seq)
        loading.value = false
    }
  }

  watch(active, (v) => {
    preview.value = null
    error.value = null
    lastKey = null
    if (v)
      void refresh()
  }, { immediate: true })
  watchDebounced(currentKey, (k, old) => {
    if (active.value && k !== old && k !== lastKey)
      void refresh()
  }, { debounce: 350 })

  /** The preview belongs to the current request and finished. */
  const ready = computed(() => !loading.value && !!preview.value && preview.value.key === currentKey.value)
  /** Shown while the key changed but the debounced run has not finished yet. */
  const stale = computed(() => !!preview.value && preview.value.key !== currentKey.value)

  return { preview, loading, error, ready, stale, refresh }
}
