<script setup lang="ts">
import type { CurrencyInfo } from '@/lib/money'
import type { PlazaModel } from '@/lib/types'
import { computed } from 'vue'
import CopyButton from '@/components/CopyButton.vue'
import { Badge } from '@/components/ui/badge'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { priceCaption as unitCaption, priceSummary } from '@/lib/billingMode'
import { isNonUnitMultiplier } from '@/lib/groups'
import { displayNameOf, formatTokenCount, isMyPlazaModel, PROTOCOL_DESCRIPTIONS, PROTOCOL_LABELS, sortProtocols } from '@/lib/plaza'
import CapabilityIcons from './CapabilityIcons.vue'
import MyModelBadges from './MyModelBadges.vue'
import PlazaPrice from './PlazaPrice.vue'
import ScheduleNote from './ScheduleNote.vue'
import VendorMark from './VendorMark.vue'

/**
 * One plaza model. `card` (grid on wide screens) or `row` (compact list on phones).
 * The title button is stretched over the whole item (opens the detail sheet); the copy
 * button and tooltips sit above it.
 */
const props = withDefaults(defineProps<{
  model: PlazaModel
  currency: CurrencyInfo | null
  layout?: 'card' | 'row'
  /** Render without the click target (admin preview). */
  static?: boolean
}>(), { layout: 'card', static: false })
defineEmits<{ open: [] }>()

const name = computed(() => displayNameOf(props.model))
const showModel = computed(() => name.value !== props.model.model)
const protocols = computed(() => sortProtocols(props.model.protocols))
const mine = computed(() => (isMyPlazaModel(props.model) ? props.model : null))
const specs = computed(() => {
  const out: string[] = []
  if (props.model.contextWindow)
    out.push(`${formatTokenCount(props.model.contextWindow)} 上下文`)
  if (props.model.maxOutput)
    out.push(`${formatTokenCount(props.model.maxOutput)} 输出`)
  return out
})
/**
 * Price caption: for free models (own / shared first) the platform price only applies
 * on fallback; with no platform channel at all there is no price to show.
 */
const priceCaption = computed(() => {
  const m = mine.value
  const unit = unitCaption(priceSummary(props.model.price))
  if (!m || m.billing !== 'free')
    return `价格 · ${unit}`
  return m.sources.platform > 0 ? `回退到平台渠道时的价格 · ${unit}` : null
})
/** phase8 §1.1: list price struck through when the group multiplier changes the price. */
const basePrice = computed(() => (mine.value && isNonUnitMultiplier(mine.value.priceMultiplier) ? mine.value.basePrice ?? null : null))
</script>

<template>
  <!-- Card -->
  <article
    v-if="layout === 'card'"
    class="group bg-card text-card-foreground relative flex min-w-0 flex-col gap-3 rounded-xl border p-4 transition-colors"
    :class="static ? '' : 'hover:border-foreground/20 focus-within:border-ring has-[button.plaza-open:focus-visible]:ring-3 has-[button.plaza-open:focus-visible]:ring-ring/50'"
    data-testid="plaza-card"
    :data-model="model.model"
  >
    <header class="flex min-w-0 items-start gap-3">
      <VendorMark :vendor="model.vendor" :model="model.model" />
      <div class="min-w-0 flex-1">
        <h3 class="truncate leading-tight font-semibold">
          <button v-if="!static" type="button" class="plaza-open text-left outline-none after:absolute after:inset-0 after:rounded-xl after:content-['']" @click="$emit('open')">
            {{ name }}
          </button>
          <template v-else>
            {{ name }}
          </template>
        </h3>
        <div class="text-muted-foreground mt-0.5 flex h-6 min-w-0 items-center gap-1">
          <code class="truncate font-mono text-xs" :title="model.model" data-testid="plaza-model-name">{{ model.model }}</code>
          <CopyButton :value="model.model" label="复制模型名" class="relative z-10 shrink-0" />
        </div>
      </div>
      <CapabilityIcons :capabilities="model.capabilities" />
    </header>

    <p class="text-muted-foreground flex min-h-4 flex-wrap items-center gap-x-1.5 text-xs">
      <span v-if="model.vendor" class="text-foreground/80">{{ model.vendor }}</span>
      <template v-for="(s, i) in specs" :key="s">
        <span v-if="model.vendor || i > 0" aria-hidden="true">·</span>
        <span class="tabular-nums">{{ s }}</span>
      </template>
      <span v-if="!model.vendor && !specs.length && !showModel">暂无资料</span>
    </p>

    <div v-if="model.tags.length" class="flex flex-wrap items-center gap-1">
      <Badge v-for="t in model.tags" :key="`t-${t}`" variant="secondary" class="font-normal">
        {{ t }}
      </Badge>
    </div>
    <div v-if="protocols.length" class="flex flex-wrap items-center gap-1" aria-label="可用协议">
      <Tooltip v-for="p in protocols" :key="`p-${p}`">
        <TooltipTrigger as-child>
          <Badge variant="outline" class="text-muted-foreground relative z-10 font-mono text-[10px] font-normal" tabindex="0" :data-protocol="p">
            {{ PROTOCOL_LABELS[p] ?? p }}
          </Badge>
        </TooltipTrigger>
        <TooltipContent>{{ PROTOCOL_DESCRIPTIONS[p] ?? p }}</TooltipContent>
      </Tooltip>
    </div>

    <div class="mt-auto space-y-2.5 border-t pt-3">
      <template v-if="priceCaption">
        <p class="text-muted-foreground text-[11px]">
          {{ priceCaption }}
        </p>
        <PlazaPrice :price="model.price" :currency="currency" :base-price="basePrice" />
        <ScheduleNote :price="model.price" />
      </template>
      <p v-else class="text-muted-foreground text-[11px]">
        仅由自有 / 共享渠道提供，不经过平台渠道，不计费
      </p>
      <MyModelBadges v-if="mine" :model="mine" />
      <div v-if="model.plans.length && !mine" class="flex flex-wrap items-center gap-1" data-testid="plaza-plans">
        <span class="text-muted-foreground text-[11px]">套餐</span>
        <Badge v-for="p in model.plans" :key="p.id" variant="outline" class="border-violet-500/40 font-normal text-violet-700 dark:text-violet-300">
          {{ p.name }}
        </Badge>
      </div>
    </div>
  </article>

  <!-- Compact row (phones) -->
  <article
    v-else
    class="relative flex min-w-0 items-start gap-3 px-1 py-3"
    data-testid="plaza-row"
    :data-model="model.model"
  >
    <VendorMark :vendor="model.vendor" :model="model.model" size="sm" />
    <div class="min-w-0 flex-1 space-y-1">
      <div class="flex min-w-0 items-start justify-between gap-2">
        <h3 class="min-w-0 truncate text-sm leading-tight font-semibold">
          <button type="button" class="plaza-open text-left outline-none after:absolute after:inset-0 after:content-[''] focus-visible:underline" @click="$emit('open')">
            {{ name }}
          </button>
        </h3>
        <div v-if="priceCaption" class="flex shrink-0 items-center gap-1">
          <ScheduleNote :price="model.price" variant="badge" />
          <PlazaPrice :price="model.price" :currency="currency" variant="compact" :base-price="basePrice" />
        </div>
        <span v-else class="shrink-0 text-xs text-emerald-700 dark:text-emerald-400">不计费</span>
      </div>
      <div class="text-muted-foreground -mt-1 flex h-6 min-w-0 items-center gap-1">
        <code class="truncate font-mono text-xs">{{ model.model }}</code>
        <CopyButton :value="model.model" label="复制模型名" class="relative z-10 shrink-0" />
      </div>
      <div class="text-muted-foreground flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1 text-xs">
        <span v-if="model.vendor" class="text-foreground/80">{{ model.vendor }}</span>
        <span v-for="s in specs" :key="s" class="tabular-nums">{{ s }}</span>
        <CapabilityIcons :capabilities="model.capabilities" class="ml-auto" />
      </div>
      <MyModelBadges v-if="mine" :model="mine" class="pt-0.5" />
      <div v-else-if="model.plans.length" class="flex flex-wrap gap-1 pt-0.5">
        <Badge v-for="p in model.plans" :key="p.id" variant="outline" class="border-violet-500/40 font-normal text-violet-700 dark:text-violet-300">
          {{ p.name }}
        </Badge>
      </div>
    </div>
  </article>
</template>
