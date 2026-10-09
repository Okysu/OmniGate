<script setup lang="ts">
import type { PriceFieldKey } from '@/lib/billingMode'
import type { CurrencyInfo } from '@/lib/money'
import type { PlazaPrice } from '@/lib/types'
import { computed } from 'vue'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { priceCaption, priceSummary, showsTokenPrices } from '@/lib/billingMode'
import { formatMoney } from '@/lib/money'
import { hasAudioUnitPrice, isChargedAmount } from '@/lib/plaza'

/**
 * Price block. Token prices per 1M (输入 / 输出 / 缓存读 / 缓存写; image models: 输入 / 输出 /
 * 每张图片 / 图片输入; audio models with per-minute / per-character prices: 输入 / 输出 /
 * 每分钟音频 / 每 1M 字符; plus 每次请求 when charged). Prices not billed by tokens
 * (按次 / 按张 / 按时长) show only their own parts ("$0.04 / 次"), never "$0".
 * Amounts show 3 decimals (`formatMoney`); tooltips explain a price, never its exact value.
 * `null` → "未定价" (not billed).
 */
const props = withDefaults(defineProps<{
  price: PlazaPrice | null
  currency: CurrencyInfo | null
  /** `compact`: main prices only (mobile list). */
  variant?: 'full' | 'compact'
  /**
   * phase8 §1.1 ("我的模型"): list price before the group multiplier; shown struck through
   * next to `price` when it differs.
   */
  basePrice?: PlazaPrice | null
}>(), { variant: 'full', basePrice: null })

function money(v: string | null) {
  return formatMoney(v, props.currency)
}

const summary = computed(() => priceSummary(props.price))
/** Token layout (input / output first) or the parts of a per-call / per-image / audio price. */
const tokenLayout = computed(() => showsTokenPrices(summary.value))

interface Cell {
  key: string
  label: string
  value: string | null
  base?: string | null
  /** Unit after the amount ("次", "张" …); token cells rely on the caption. */
  unit?: string
  title?: string
}

function baseOf(key: PriceFieldKey): string | null | undefined {
  return props.basePrice ? props.basePrice[key] ?? null : undefined
}

const cells = computed<Cell[]>(() => {
  const p = props.price
  const s = summary.value
  if (!p || !s)
    return []
  if (!tokenLayout.value) {
    return s.main.map(part => ({
      key: part.key,
      label: part.label,
      value: part.amount,
      base: baseOf(part.key),
      unit: part.unit,
    }))
  }
  const b = props.basePrice
  const all: Cell[] = [
    { key: 'input', label: '输入', value: p.inputPerM, base: b?.inputPerM },
    { key: 'output', label: '输出', value: p.outputPerM, base: b?.outputPerM },
  ]
  if (isChargedAmount(p.perImage) || p.imageInputPerM != null) {
    // Image models (phase7 §1.1): per-image and image-input prices replace the cache cells.
    const perImage = isChargedAmount(p.perImage) ? p.perImage : null
    all.push(
      { key: 'perImage', label: '每张图片', value: perImage, base: b?.perImage, unit: '张', title: perImage == null ? '不按张计费' : '每张输出图片' },
      { key: 'imageInput', label: '图片输入', value: p.imageInputPerM ?? p.inputPerM, base: b ? (b.imageInputPerM ?? b.inputPerM) : null, title: p.imageInputPerM == null ? '未单独设置，按输入单价计' : undefined },
    )
  }
  else if (hasAudioUnitPrice(p)) {
    // Audio models (phase9 §1.1): per-minute / per-1M-characters prices replace the cache cells.
    const perMinute = isChargedAmount(p.perMinute) ? p.perMinute : null
    const perMChars = isChargedAmount(p.perMCharacters) ? p.perMCharacters : null
    all.push(
      { key: 'perMinute', label: '每分钟音频', value: perMinute, base: b?.perMinute, title: perMinute == null ? '不按时长计费' : '转写 / 翻译每分钟输入音频（按秒计）' },
      { key: 'perMCharacters', label: '每 1M 字符', value: perMChars, base: b?.perMCharacters, title: perMChars == null ? '不按字符计费' : '语音合成每 1M 输入字符' },
    )
  }
  else {
    all.push(
      { key: 'cacheRead', label: '缓存读', value: p.cacheReadPerM, base: b?.cacheReadPerM },
      { key: 'cacheWrite', label: '缓存写', value: p.cacheWritePerM, base: b?.cacheWritePerM },
    )
  }
  if (isChargedAmount(p.perRequest))
    all.push({ key: 'perRequest', label: '每次请求', value: p.perRequest, base: b?.perRequest, unit: '次', title: '除 token 费用外每次请求另收' })
  return all
})

/** Compact (phone rows): parts after input / output, e.g. "+ $0.01 / 次". */
const compactExtras = computed(() => summary.value?.main.filter(p => !p.token) ?? [])

/** Base price to strike through (only when it differs from the user's price). */
function struck(c: { value: string | null, base?: string | null }): string | null {
  if (c.base == null || c.value == null || c.base === c.value)
    return null
  return money(c.base)
}

/** Tooltip of a full-layout cell: what the price is (never the exact amount). */
function cellTitle(c: Cell): string | undefined {
  const parts = [c.title, struck(c) ? '划线价为分组倍率前的原价' : undefined].filter(Boolean)
  return parts.length ? parts.join('；') : undefined
}
</script>

<template>
  <div v-if="!price" class="flex items-center gap-2" data-testid="plaza-price">
    <Tooltip>
      <TooltipTrigger as-child>
        <span class="bg-muted text-muted-foreground relative z-10 inline-flex h-6 items-center rounded-md px-2 text-xs font-medium" tabindex="0">
          未定价
        </span>
      </TooltipTrigger>
      <TooltipContent>平台未设置售价，调用不计费</TooltipContent>
    </Tooltip>
  </div>
  <dl
    v-else-if="variant === 'full'"
    class="grid grid-cols-2 gap-x-4 gap-y-1.5 text-xs"
    :class="tokenLayout ? 'sm:grid-cols-4 sm:gap-x-3' : ''"
    data-testid="plaza-price"
    :data-mode="summary?.mode"
  >
    <div v-for="c in cells" :key="c.key" class="min-w-0">
      <dt class="text-muted-foreground">
        {{ c.label }}
      </dt>
      <dd class="truncate text-sm font-medium tabular-nums" :class="c.value == null ? 'text-muted-foreground font-normal' : ''" :title="cellTitle(c)" :data-price="c.key">
        {{ c.value == null ? '—' : money(c.value) }}<span v-if="c.unit && c.value != null" class="text-muted-foreground text-xs font-normal"> / {{ c.unit }}</span>
        <s v-if="struck(c)" class="text-muted-foreground block truncate text-[11px] leading-tight font-normal" data-testid="base-price">{{ struck(c) }}</s>
      </dd>
    </div>
  </dl>
  <p v-else-if="tokenLayout" class="text-right text-xs leading-tight tabular-nums" data-testid="plaza-price" :data-mode="summary?.mode">
    <span class="font-medium">{{ money(price.inputPerM) }}</span>
    <span class="text-muted-foreground"> / </span>
    <span class="font-medium">{{ money(price.outputPerM) }}</span>
    <s v-if="basePrice && (basePrice.inputPerM !== price.inputPerM || basePrice.outputPerM !== price.outputPerM)" class="text-muted-foreground block text-[10px]" data-testid="base-price">{{ money(basePrice.inputPerM) }} / {{ money(basePrice.outputPerM) }}</s>
    <span class="text-muted-foreground block text-[10px]">输入 / 输出 · 每 1M</span>
    <span v-for="x in compactExtras" :key="x.key" class="text-muted-foreground block text-[10px]" :title="x.label">+ {{ money(x.amount) }} / {{ x.unit }}</span>
  </p>
  <p v-else class="text-right text-xs leading-tight tabular-nums" data-testid="plaza-price" :data-mode="summary?.mode">
    <span v-for="c in cells" :key="c.key" class="block" :title="c.title">
      <span class="font-medium">{{ money(c.value) }}</span><span class="text-muted-foreground"> / {{ c.unit }}</span>
      <s v-if="struck(c)" class="text-muted-foreground block text-[10px]" data-testid="base-price">{{ struck(c) }} / {{ c.unit }}</s>
    </span>
    <span class="text-muted-foreground block text-[10px]">{{ priceCaption(summary) }}</span>
  </p>
</template>
