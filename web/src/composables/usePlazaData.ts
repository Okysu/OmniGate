import type { PlazaModel, PlazaCurrency, PlazaResponse } from '@/lib/types'
import type { Ref } from 'vue'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { plazaCurrency } from '@/lib/plaza'
import { isAbortError } from '@/lib/query'
import { useSystemStore } from '@/stores/system'

/** Loads a plaza list (`plazaApi.models` / `plazaApi.mine`) on mount, with abort, error and currency. */
export function usePlazaData<T extends PlazaModel>(fetcher: (signal: AbortSignal) => Promise<PlazaResponse<T>>) {
  const system = useSystemStore()
  const items = ref([]) as Ref<T[]>
  const resCurrency = ref<PlazaCurrency | null>(null)
  const loading = ref(true)
  const error = ref<unknown>(null)
  let controller: AbortController | null = null

  async function load() {
    controller?.abort()
    const ctrl = new AbortController()
    controller = ctrl
    loading.value = true
    error.value = null
    try {
      const res = await fetcher(ctrl.signal)
      items.value = res.items
      resCurrency.value = res.currency
    }
    catch (err) {
      if (!isAbortError(err))
        error.value = err
    }
    finally {
      if (controller === ctrl)
        loading.value = false
    }
  }

  onMounted(() => {
    void system.ensureLoaded()
    void load()
  })
  onBeforeUnmount(() => controller?.abort())

  const currency = computed(() => plazaCurrency(resCurrency.value, system.info?.currency ?? null))
  return { items, loading, error, currency, load }
}
