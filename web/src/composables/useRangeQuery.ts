import type { RangePreset, TimeRange } from '@/lib/timeRange'
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { queryStr } from '@/lib/query'
import { presetRange, toLocalInput } from '@/lib/timeRange'

/**
 * Time range kept in the URL (`range`, plus `from`/`to` for custom ranges) so
 * reload and back/forward keep the view.
 */
export function useRangeQuery(defaultPreset: Exclude<RangePreset, 'custom'>, presets: RangePreset[]) {
  const route = useRoute()
  const router = useRouter()

  const preset = computed<RangePreset>({
    get() {
      const v = queryStr(route.query.range) as RangePreset
      if (!presets.includes(v))
        return defaultPreset
      if (v === 'custom' && !(queryStr(route.query.from) && queryStr(route.query.to)))
        return 'custom'
      return v
    },
    set(v) {
      if (v === 'custom') {
        // Seed the custom inputs with the range currently shown.
        const cur = resolve()
        void router.replace({ query: { ...route.query, range: 'custom', from: cur.from, to: cur.to, page: undefined } })
      }
      else {
        void router.replace({ query: { ...route.query, range: v === defaultPreset ? undefined : v, from: undefined, to: undefined, page: undefined } })
      }
    },
  })

  const customFrom = computed(() => queryStr(route.query.from))
  const customTo = computed(() => queryStr(route.query.to))
  const customFromLocal = computed(() => (customFrom.value ? toLocalInput(customFrom.value) : ''))
  const customToLocal = computed(() => (customTo.value ? toLocalInput(customTo.value) : ''))

  /** Concrete ISO range; presets are evaluated against "now" on every call. */
  function resolve(): TimeRange {
    const p = preset.value
    if (p === 'custom' && customFrom.value && customTo.value)
      return { from: customFrom.value, to: customTo.value }
    return presetRange(p === 'custom' ? defaultPreset : p)
  }

  function applyCustom(range: { from: string, to: string }) {
    void router.replace({ query: { ...route.query, range: 'custom', from: range.from, to: range.to, page: undefined } })
  }

  return { preset, customFrom, customTo, customFromLocal, customToLocal, resolve, applyCustom }
}
