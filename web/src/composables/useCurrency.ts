import type { FormatMoneyOptions } from '@/lib/money'
import { computed, onMounted } from 'vue'
import { formatMoney } from '@/lib/money'
import { useSystemStore } from '@/stores/system'

/** Formatting options for `money()`. */
export type MoneyOptions = FormatMoneyOptions

/**
 * Currency from `/api/system/info` plus a bound money formatter: `money()`
 * shows every displayed amount with `DISPLAY_DECIMALS` (3) fraction digits.
 * Editable inputs never go through it — they hold the exact decimal string.
 */
export function useCurrency() {
  const system = useSystemStore()
  onMounted(() => {
    void system.ensureLoaded()
  })
  const currency = computed(() => system.info?.currency ?? null)
  function money(value: string | null | undefined, opts?: MoneyOptions): string {
    return formatMoney(value, currency.value, opts)
  }
  return { currency, money }
}
