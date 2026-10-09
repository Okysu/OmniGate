<script setup lang="ts">
import type { EventMeta, NotificationCategory } from '@/lib/notifications'
import type { EventChannels } from '@/lib/types'
import { computed } from 'vue'
import { BellRing, Lock, Mail, Webhook, Zap } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { CATEGORIES, CATEGORY_LABELS, isLocked } from '@/lib/notifications'

/** Per-event channel switches (rows grouped by category; email / webhook / in-app columns). */
const props = defineProps<{
  /** Events the user may receive (the only rows shown). */
  events: EventMeta[]
  emailEnabled: boolean
  webhookEnabled: boolean
  digestDaily: boolean
  errors: Record<string, string>
}>()
const model = defineModel<Record<string, EventChannels>>({ required: true })

const groups = computed(() => CATEGORIES
  .map(c => ({ category: c as NotificationCategory, label: CATEGORY_LABELS[c], items: props.events.filter(e => e.category === c) }))
  .filter(g => g.items.length > 0))

type Column = keyof EventChannels
const COLUMNS: { key: Column, label: string, icon: typeof Mail }[] = [
  { key: 'email', label: '邮件', icon: Mail },
  { key: 'webhook', label: 'Webhook', icon: Webhook },
  { key: 'inApp', label: '站内', icon: BellRing },
]

function value(type: string, col: Column): boolean {
  return isLocked(type, col) || !!model.value[type]?.[col]
}
function set(type: string, col: Column, v: boolean) {
  if (isLocked(type, col))
    return
  const cur = model.value[type] ?? { email: false, webhook: false, inApp: false }
  model.value = { ...model.value, [type]: { ...cur, [col]: v } }
}
function columnOff(col: Column): boolean {
  return (col === 'email' && !props.emailEnabled) || (col === 'webhook' && !props.webhookEnabled)
}
function rowError(type: string): string | null {
  for (const [k, v] of Object.entries(props.errors)) {
    if (k === `events.${type}` || k.startsWith(`events.${type}.`))
      return v
  }
  return null
}
const GRID = 'grid grid-cols-[minmax(0,1fr)_repeat(3,3rem)] items-center gap-x-1 sm:grid-cols-[minmax(0,1fr)_repeat(3,5.5rem)] sm:gap-x-2'
</script>

<template>
  <div class="overflow-hidden rounded-lg border" data-testid="event-matrix">
    <div :class="GRID" class="bg-muted/50 text-muted-foreground border-b px-3 py-2 text-xs font-medium" role="row">
      <span role="columnheader">事件</span>
      <span v-for="c in COLUMNS" :key="c.key" role="columnheader" class="flex flex-col items-center gap-0.5 text-center sm:flex-row sm:justify-center sm:gap-1">
        <component :is="c.icon" class="size-3.5 shrink-0" />
        <span class="truncate" :class="c.key === 'webhook' ? 'max-sm:text-[10px]' : ''">{{ c.label }}</span>
      </span>
    </div>
    <div v-for="g in groups" :key="g.category" role="rowgroup" :data-category="g.category">
      <div class="bg-muted/20 text-muted-foreground border-b px-3 py-1.5 text-xs font-semibold">
        {{ g.label }}
      </div>
      <div v-for="e in g.items" :key="e.type" :class="GRID" class="border-b px-3 py-2.5 last:border-b-0" role="row" :data-event="e.type">
        <div class="min-w-0 space-y-0.5 pr-1" role="rowheader">
          <p class="flex flex-wrap items-center gap-1.5 text-sm font-medium">
            {{ e.label }}
            <Tooltip v-if="e.alert && digestDaily">
              <TooltipTrigger as-child>
                <Badge variant="outline" class="h-4 gap-0.5 px-1 text-[10px] font-normal text-amber-700 dark:text-amber-400" tabindex="0">
                  <Zap />即时
                </Badge>
              </TooltipTrigger>
              <TooltipContent class="max-w-60">
                告警类事件不合并进每日摘要，总是立即发送。
              </TooltipContent>
            </Tooltip>
          </p>
          <p class="text-muted-foreground text-xs leading-relaxed">
            {{ e.description }}
          </p>
          <p v-if="rowError(e.type)" class="text-destructive text-xs" role="alert">
            {{ rowError(e.type) }}
          </p>
        </div>
        <div v-for="c in COLUMNS" :key="c.key" class="flex justify-center" role="cell">
          <Tooltip v-if="isLocked(e.type, c.key)">
            <TooltipTrigger as-child>
              <span class="relative inline-flex" tabindex="0" :aria-label="`${e.label}：${c.label}（始终开启，不可关闭）`" :data-testid="`ev-${e.type}-${c.key}-locked`">
                <Switch :model-value="true" disabled aria-hidden="true" tabindex="-1" :data-testid="`ev-${e.type}-${c.key}`" />
                <Lock class="text-muted-foreground bg-background absolute -top-1.5 -right-2 size-3 rounded-full" aria-hidden="true" />
              </span>
            </TooltipTrigger>
            <TooltipContent class="max-w-60">
              站内通知不可关闭：账号状态变更总会出现在通知中心。
            </TooltipContent>
          </Tooltip>
          <Switch
            v-else
            :model-value="value(e.type, c.key)"
            :aria-label="`${e.label}：${c.label}`"
            :class="columnOff(c.key) && value(e.type, c.key) ? 'opacity-60' : ''"
            :data-testid="`ev-${e.type}-${c.key}`"
            @update:model-value="(v: boolean) => set(e.type, c.key, v)"
          />
        </div>
      </div>
    </div>
  </div>
</template>
