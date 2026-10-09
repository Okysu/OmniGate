<script setup lang="ts">
import type { ScheduleForm } from '@/lib/priceSchedule'
import { computed, ref } from 'vue'
import { CircleAlert, Clock, Plus, Sparkles, Trash2 } from '@lucide/vue'
import FormField from '@/components/FormField.vue'
import ScheduleTimeline from '@/components/ScheduleTimeline.vue'
import TimezoneSelect from '@/components/TimezoneSelect.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  buildSchedule,
  DAY_CHARS,
  DAY_NAMES,
  emptyRow,
  everyDay,
  isEveryDay,
  MAX_SCHEDULE_ROWS,
  parseTime,
  rowErrors,
  SCHEDULE_PRESETS,
  toggleDay,
  WEEKDAYS,
} from '@/lib/priceSchedule'

/** "分时价格" editor of the price dialog (phase8 §3). */
const props = defineProps<{
  /** Merged client + server errors (`schedule.<i>.<field>`, `schedule`, `scheduleTimezone`). */
  errors: Record<string, string>
}>()
const form = defineModel<ScheduleForm>({ required: true })

const MAX_ROWS = MAX_SCHEDULE_ROWS
const focused = ref<number | null>(null)
const periods = computed(() => buildSchedule(form.value.rows))

function addRow() {
  if (form.value.rows.length < MAX_ROWS)
    form.value = { ...form.value, rows: [...form.value.rows, emptyRow()] }
}
function removeRow(i: number) {
  form.value = { ...form.value, rows: form.value.rows.filter((_, j) => j !== i) }
}
function setDays(i: number, days: number[]) {
  form.value.rows[i]!.days = days
}
function applyPreset(id: string) {
  const p = SCHEDULE_PRESETS.find(x => x.id === id)
  if (p)
    form.value = { rows: p.rows.map(r => ({ ...r, days: [...r.days] })), timezone: p.timezone }
}
function crossesMidnight(i: number): boolean {
  const r = form.value.rows[i]!
  const s = parseTime(r.start)
  const e = parseTime(r.end)
  return s !== null && e !== null && s > e
}
function nextDayName(i: number): string {
  const days = form.value.rows[i]!.days
  if (isEveryDay(days))
    return '次日'
  return days.length === 1 ? DAY_NAMES[(days[0]! + 1) % 7]! : '次日'
}
const errs = computed(() => form.value.rows.map((_, i) => rowErrors(props.errors, i)))
</script>

<template>
  <div class="space-y-3 rounded-lg border p-3" role="group" aria-labelledby="ps-title" data-testid="schedule-editor">
    <div class="flex flex-wrap items-start justify-between gap-2">
      <div class="min-w-0">
        <p id="ps-title" class="flex items-center gap-1.5 text-sm font-medium">
          <Clock class="size-4" aria-hidden="true" />
          分时价格（可选）
        </p>
        <p class="text-muted-foreground text-xs">
          在指定时段把该版本的全部单价乘以倍率；按请求开始时间匹配第一个命中的时段，未命中按原价。时段之间不能重叠。
        </p>
      </div>
      <Button
        v-for="p in SCHEDULE_PRESETS"
        :key="p.id"
        type="button"
        variant="outline"
        size="sm"
        :title="p.description"
        :data-testid="`preset-${p.id}`"
        @click="applyPreset(p.id)"
      >
        <Sparkles />
        {{ p.label }}
      </Button>
    </div>

    <p v-if="errors.schedule" class="text-destructive flex items-start gap-1.5 text-xs" role="alert">
      <CircleAlert class="mt-0.5 size-3.5 shrink-0" />{{ errors.schedule }}
    </p>

    <ol v-if="form.rows.length" class="space-y-2">
      <li
        v-for="(r, i) in form.rows"
        :key="i"
        class="bg-muted/30 space-y-2 rounded-md border p-2.5"
        :class="errs[i]?.[''] ? 'border-destructive/50' : ''"
        :data-testid="`schedule-row-${i}`"
        @focusin="focused = i"
        @focusout="focused = null"
      >
        <div class="flex flex-wrap items-center gap-1.5">
          <span class="text-muted-foreground mr-1 text-xs font-medium tabular-nums">时段 {{ i + 1 }}</span>
          <button
            type="button"
            class="h-7 rounded-md border px-2 text-xs transition-colors"
            :class="isEveryDay(r.days) ? 'border-primary bg-primary text-primary-foreground' : 'hover:bg-muted text-muted-foreground'"
            :aria-pressed="isEveryDay(r.days)"
            data-testid="day-every"
            @click="setDays(i, everyDay())"
          >
            每天
          </button>
          <div class="flex gap-1" role="group" :aria-label="`时段 ${i + 1} 的星期`">
            <button
              v-for="d in WEEKDAYS"
              :key="d"
              type="button"
              class="size-7 rounded-md border text-xs transition-colors"
              :class="r.days.includes(d) ? 'border-primary bg-primary/10 text-foreground font-medium' : 'hover:bg-muted text-muted-foreground'"
              :aria-pressed="r.days.includes(d)"
              :aria-label="DAY_NAMES[d]"
              :data-day="d"
              @click="setDays(i, toggleDay(r.days, d))"
            >
              {{ DAY_CHARS[d] }}
            </button>
          </div>
          <Button type="button" variant="ghost" size="icon-sm" class="ml-auto" :aria-label="`删除时段 ${i + 1}`" @click="removeRow(i)">
            <Trash2 />
          </Button>
        </div>
        <p v-if="errs[i]?.days" class="text-destructive text-xs" role="alert">
          {{ errs[i]?.days }}
        </p>
        <div class="grid grid-cols-2 gap-2 sm:grid-cols-3" :data-testid="`schedule-fields-${i}`">
          <div class="space-y-1">
            <Label :for="`ps-start-${i}`" class="text-xs">开始</Label>
            <Input :id="`ps-start-${i}`" v-model="r.start" type="time" step="60" class="tabular-nums" :aria-invalid="!!errs[i]?.start" />
          </div>
          <div class="space-y-1">
            <Label :for="`ps-end-${i}`" class="text-xs">结束（不含）</Label>
            <Input :id="`ps-end-${i}`" v-model="r.end" type="time" step="60" class="tabular-nums" :aria-invalid="!!errs[i]?.end" />
          </div>
          <div class="col-span-2 space-y-1 sm:col-span-1">
            <Label :for="`ps-mult-${i}`" class="text-xs">倍率</Label>
            <div class="relative">
              <span class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-sm">×</span>
              <Input :id="`ps-mult-${i}`" v-model="r.multiplier" inputmode="decimal" class="pl-6 font-mono tabular-nums" :aria-invalid="!!errs[i]?.multiplier" />
            </div>
          </div>
        </div>
        <p v-if="errs[i]?.start || errs[i]?.end || errs[i]?.multiplier" class="text-destructive text-xs" role="alert">
          {{ [errs[i]?.start && `开始：${errs[i]?.start}`, errs[i]?.end && `结束：${errs[i]?.end}`, errs[i]?.multiplier && `倍率：${errs[i]?.multiplier}`].filter(Boolean).join('；') }}
        </p>
        <p v-else-if="crossesMidnight(i)" class="text-xs text-sky-700 dark:text-sky-400" data-testid="cross-midnight">
          跨零点：{{ r.start }} 至{{ nextDayName(i) }} {{ r.end }}（时段属于开始的那一天）。
        </p>
        <p v-if="errs[i]?.['']" class="text-destructive flex items-start gap-1.5 text-xs" role="alert" data-testid="row-overlap">
          <CircleAlert class="mt-0.5 size-3.5 shrink-0" />{{ errs[i]?.[''] }}
        </p>
      </li>
    </ol>
    <p v-else class="text-muted-foreground rounded-md border border-dashed p-3 text-center text-xs">
      未设置时段：全天按原价计费。
    </p>

    <div class="flex flex-wrap items-center gap-2">
      <Button type="button" variant="outline" size="sm" :disabled="form.rows.length >= MAX_ROWS" data-testid="schedule-add" @click="addRow">
        <Plus />
        添加时段
      </Button>
      <span class="text-muted-foreground text-xs">结束时间早于开始时间表示跨零点（例如 22:00–06:00）。</span>
    </div>

    <template v-if="form.rows.length">
      <FormField label="时段所用时区" for="ps-tz" :error="errors.scheduleTimezone">
        <TimezoneSelect id="ps-tz" v-model="form.timezone" :invalid="!!errors.scheduleTimezone" />
      </FormField>
      <div class="space-y-1.5">
        <p class="text-muted-foreground text-xs">
          预览（{{ form.timezone }}）
        </p>
        <ScheduleTimeline :periods="periods" :highlight="focused" />
      </div>
    </template>
  </div>
</template>
