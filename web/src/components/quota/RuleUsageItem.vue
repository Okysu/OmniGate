<script setup lang="ts">
import type { QuotaOverflow, RuleUsage } from '@/lib/types'
import { computed } from 'vue'
import { CircleAlert } from '@lucide/vue'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useCurrency } from '@/composables/useCurrency'
import { isCustomMeter } from '@/lib/customMeters'
import { exceededNote, formatMeterAmount, meterLabel, meterUnit, modelsSummary, resetInfo, ruleTitle, usageLevel, usageRatio, windowDescription } from '@/lib/quota'
import QuotaBar from './QuotaBar.vue'

const props = withDefaults(defineProps<{
  rule: RuleUsage
  /** Current time (ms) so a parent can tick relative times. */
  now?: number
  /** Only show the title, numbers and bar (tables / tiles). */
  compact?: boolean
  /** Whether the subscription is live (exceeded / reset notes only make sense then). */
  live?: boolean
  /** What happens once quota is used up (the owner's billing preference); null = unknown. */
  overflow?: QuotaOverflow | null
}>(), { now: undefined, compact: false, live: true, overflow: null })

const { money } = useCurrency()

const isCharge = computed(() => props.rule.meter === 'charge')
const used = computed(() => formatMeterAmount(props.rule.meter, props.rule.used, money))
const limit = computed(() => formatMeterAmount(props.rule.meter, props.rule.limit, money))
const unit = computed(() => meterUnit(props.rule.meter, props.rule))
const usageTitle = computed(() => isCharge.value
  ? `已用 ${money(props.rule.used)} / 上限 ${money(props.rule.limit)}，剩余 ${money(props.rule.remaining)}`
  : `已用 ${props.rule.used} / 上限 ${props.rule.limit} ${unit.value}，剩余 ${props.rule.remaining}`)
const ratio = computed(() => usageRatio(props.rule.used, props.rule.limit))
const level = computed(() => (props.live ? usageLevel(props.rule) : 'ok'))
const reset = computed(() => resetInfo(props.rule, props.now ?? Date.now()))
const detail = computed(() => {
  const parts: string[] = []
  if (isCustomMeter(props.rule.meter))
    parts.push(`插件计量「${meterLabel(props.rule.meter, props.rule)}」`)
  if (props.rule.models.length)
    parts.push(`仅计量 ${modelsSummary(props.rule.models, 3)}`)
  const weights = Object.entries(props.rule.modelWeights)
  if (weights.length)
    parts.push(`倍率 ${weights.map(([m, w]) => `${m} ×${w}`).join('、')}`)
  return parts.join(' · ')
})
</script>

<template>
  <div class="min-w-0 space-y-1.5" :data-exceeded="live && rule.exceeded ? '' : undefined">
    <div class="flex items-baseline justify-between gap-2" :class="compact ? 'text-xs' : 'text-sm'">
      <span class="flex min-w-0 items-center gap-1 truncate font-medium">
        <CircleAlert v-if="live && rule.exceeded" class="text-destructive size-3.5 shrink-0" aria-hidden="true" />
        <span class="truncate">{{ ruleTitle(rule) }}</span>
      </span>
      <span class="shrink-0 tabular-nums" :class="live && rule.exceeded ? 'text-destructive font-medium' : ''" :title="usageTitle">
        {{ used }} / {{ limit }}<span v-if="unit" class="text-muted-foreground ml-1">{{ unit }}</span>
      </span>
    </div>
    <QuotaBar :ratio="ratio" :level="level" :label="ruleTitle(rule)" :size="compact ? 'sm' : 'md'" />
    <div v-if="!compact" class="text-muted-foreground flex flex-wrap justify-between gap-x-3 gap-y-0.5 text-xs">
      <span>{{ windowDescription(rule.window) }}</span>
      <template v-if="live">
        <Tooltip v-if="reset.absolute">
          <TooltipTrigger as-child>
            <span tabindex="0" class="decoration-muted-foreground/50 cursor-help underline decoration-dotted underline-offset-2">{{ reset.text }}</span>
          </TooltipTrigger>
          <TooltipContent>重置时间：{{ reset.absolute }}</TooltipContent>
        </Tooltip>
        <span v-else>{{ reset.text }}</span>
      </template>
    </div>
    <template v-if="!compact">
      <p v-if="live && rule.exceeded" class="text-destructive text-xs" role="status">
        {{ exceededNote(overflow) }}
      </p>
      <p v-if="detail" class="text-muted-foreground text-xs">
        {{ detail }}
      </p>
    </template>
  </div>
</template>
