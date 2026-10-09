<script setup lang="ts">
import type { BillingMode } from '@/lib/billingMode'
import type { TierBase, TierPriceKey, TierRow } from '@/lib/priceTiers'
import { computed } from 'vue'
import { CircleAlert, Layers, Plus, Sparkles, Trash2 } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { amountSign } from '@/lib/money'
import {
  emptyTierRow,
  formatTokenThreshold,
  LONG_CONTEXT_THRESHOLD,
  longContextPreset,
  MAX_TIERS,
  parseTokenThreshold,
  TIER_MODE_KEYS,
  tierPreview,
  tierRowErrors,
} from '@/lib/priceTiers'

/** "阶梯价格（按上下文长度）" editor of the price dialog (phase10 §1; Token / 自定义组合). */
const props = defineProps<{
  mode: BillingMode
  /** The dialog's base amounts (preset and preview). */
  base: TierBase & { inputPerM: string, outputPerM: string }
  symbol: string
  money: (amount: string) => string
  /** Merged client + server errors (`tiers`, `tiers.<i>.<field>`). */
  errors: Record<string, string>
}>()
const rows = defineModel<TierRow[]>({ required: true })

const LABELS: Record<TierPriceKey, string> = {
  inputPerM: '输入',
  outputPerM: '输出',
  cacheReadPerM: '缓存读',
  cacheWritePerM: '缓存写',
  imageInputPerM: '图片输入',
  audioInputPerM: '音频输入',
  audioOutputPerM: '音频输出',
}
const keys = computed(() => TIER_MODE_KEYS[props.mode] ?? TIER_MODE_KEYS.token!)
const tokenKeys = computed(() => keys.value.slice(0, 4))
const mediaKeys = computed(() => keys.value.slice(4))

/** Placeholder of an optional price: what it inherits. */
function placeholder(k: TierPriceKey): string {
  if (k === 'inputPerM' || k === 'outputPerM')
    return '0'
  const v = props.base[k]?.trim()
  if (v && amountSign(v) >= 0 && v !== '')
    return `继承 ${v}`
  if (k === 'imageInputPerM' || k === 'audioInputPerM')
    return '按本档输入'
  if (k === 'audioOutputPerM')
    return '按本档输出'
  return '继承基础价'
}

function sortRows(list: TierRow[]): TierRow[] {
  return [...list].sort((a, b) => (parseTokenThreshold(a.threshold) ?? Infinity) - (parseTokenThreshold(b.threshold) ?? Infinity))
}
function addRow() {
  if (rows.value.length >= MAX_TIERS)
    return
  const last = rows.value.at(-1)
  const n = last ? parseTokenThreshold(last.threshold) : null
  rows.value = [...rows.value, emptyTierRow(n ? '' : formatTokenThreshold(LONG_CONTEXT_THRESHOLD))]
}
function removeRow(i: number) {
  rows.value = rows.value.filter((_, j) => j !== i)
}
const baseSet = computed(() => amountSign(props.base.inputPerM) > 0 || amountSign(props.base.outputPerM) > 0)
function applyPreset() {
  const preset = longContextPreset(props.base, props.mode)
  const at = rows.value.findIndex(r => parseTokenThreshold(r.threshold) === LONG_CONTEXT_THRESHOLD)
  if (at >= 0)
    rows.value = rows.value.map((r, j) => (j === at ? preset : r))
  else if (rows.value.length < MAX_TIERS)
    rows.value = sortRows([...rows.value, preset])
}

const errs = computed(() => rows.value.map((_, i) => tierRowErrors(props.errors, i)))
function thresholdHint(r: TierRow): string | null {
  const n = parseTokenThreshold(r.threshold)
  return n === null ? null : `> ${n.toLocaleString('en-US')} token`
}
const preview = computed(() => tierPreview(props.base, rows.value, props.money))
</script>

<template>
  <div class="space-y-3 rounded-lg border p-3" role="group" aria-labelledby="pt-title" data-testid="tier-editor">
    <div class="flex flex-wrap items-start justify-between gap-2">
      <div class="min-w-0 flex-1 basis-64">
        <p id="pt-title" class="flex items-center gap-1.5 text-sm font-medium">
          <Layers class="size-4" aria-hidden="true" />
          阶梯价格（按上下文长度）
        </p>
        <p class="text-muted-foreground text-xs">
          一次请求的输入 + 缓存 token 总数超过阈值时，<strong class="text-foreground font-medium">整次请求</strong>的全部 token 按该档单价计费（等于阈值仍按上一档）；按次、按张、按时长的单价不分档。分时与分组倍率照常叠加。
        </p>
      </div>
      <Button
        type="button"
        variant="outline"
        size="sm"
        class="h-auto min-h-8 max-w-full py-1.5 text-left whitespace-normal"
        :disabled="!baseSet"
        :title="baseSet ? '按基础单价预填：输入、缓存读写 ×2，输出 ×1.5，之后可再修改' : '先填写基础的输入 / 输出单价'"
        data-testid="tier-preset"
        @click="applyPreset"
      >
        <Sparkles />
        长上下文：&gt;272K 输入 token 全部单价翻倍
      </Button>
    </div>

    <p v-if="errors.tiers" class="text-destructive flex items-start gap-1.5 text-xs" role="alert">
      <CircleAlert class="mt-0.5 size-3.5 shrink-0" />{{ errors.tiers }}
    </p>

    <ol v-if="rows.length" class="space-y-2">
      <li
        v-for="(r, i) in rows"
        :key="i"
        class="bg-muted/30 relative space-y-2 rounded-md border p-2.5"
        :data-testid="`tier-row-${i}`"
      >
        <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span class="text-muted-foreground w-full text-xs font-medium tabular-nums sm:w-auto">第 {{ i + 2 }} 档</span>
          <Label :for="`pt-th-${i}`" class="text-xs font-normal">输入 + 缓存超过</Label>
          <Input
            :id="`pt-th-${i}`"
            v-model="r.threshold"
            class="h-8 w-28 font-mono tabular-nums"
            placeholder="272K"
            :aria-invalid="!!errs[i]?.aboveInputTokens"
            :aria-describedby="`pt-th-hint-${i}`"
            data-testid="tier-threshold"
          />
          <span class="text-xs">token</span>
          <Button type="button" variant="ghost" size="icon-sm" class="ml-auto max-sm:absolute max-sm:top-1.5 max-sm:right-1.5" :aria-label="`删除第 ${i + 2} 档`" @click="removeRow(i)">
            <Trash2 />
          </Button>
        </div>
        <p :id="`pt-th-hint-${i}`" class="text-xs" :class="errs[i]?.aboveInputTokens ? 'text-destructive' : 'text-muted-foreground tabular-nums'" :role="errs[i]?.aboveInputTokens ? 'alert' : undefined" data-testid="tier-threshold-hint">
          {{ errs[i]?.aboveInputTokens ?? thresholdHint(r) ?? '支持 272K、1M、272000 等写法（K = 1,000，M = 1,000,000）' }}
        </p>
        <div class="grid grid-cols-2 gap-2 sm:grid-cols-4" :data-testid="`tier-fields-${i}`">
          <div v-for="k in tokenKeys" :key="k" class="min-w-0 space-y-1">
            <Label :for="`pt-${k}-${i}`" class="gap-0.5 text-xs">
              {{ LABELS[k] }}<span v-if="k === 'inputPerM' || k === 'outputPerM'" class="text-destructive" aria-hidden="true">*</span>
            </Label>
            <div class="relative">
              <span class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-sm">{{ symbol }}</span>
              <Input :id="`pt-${k}-${i}`" v-model="r[k]" inputmode="decimal" :placeholder="placeholder(k)" class="h-8 pl-7 font-mono text-sm tabular-nums placeholder:font-sans placeholder:text-xs" :aria-invalid="!!errs[i]?.[k]" :data-field="k" />
            </div>
          </div>
        </div>
        <div v-if="mediaKeys.length" class="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <div v-for="k in mediaKeys" :key="k" class="min-w-0 space-y-1">
            <Label :for="`pt-${k}-${i}`" class="text-xs">{{ LABELS[k] }}</Label>
            <div class="relative">
              <span class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-sm">{{ symbol }}</span>
              <Input :id="`pt-${k}-${i}`" v-model="r[k]" inputmode="decimal" :placeholder="placeholder(k)" class="h-8 pl-7 font-mono text-sm tabular-nums placeholder:font-sans placeholder:text-xs" :aria-invalid="!!errs[i]?.[k]" :data-field="k" />
            </div>
          </div>
        </div>
        <p v-if="keys.some(k => errs[i]?.[k])" class="text-destructive text-xs" role="alert">
          {{ keys.filter(k => errs[i]?.[k]).map(k => `${LABELS[k]}：${errs[i]?.[k]}`).join('；') }}
        </p>
      </li>
    </ol>
    <p v-else class="text-muted-foreground rounded-md border border-dashed p-3 text-center text-xs">
      未设置阶梯：不论上下文多长都按上方单价计费。
    </p>

    <div class="flex flex-wrap items-center gap-2">
      <Button type="button" variant="outline" size="sm" :disabled="rows.length >= MAX_TIERS" data-testid="tier-add" @click="addRow">
        <Plus />
        添加一档
      </Button>
      <span class="text-muted-foreground text-xs">最多 {{ MAX_TIERS }} 档，阈值须逐档递增；留空的可选单价沿用上方的基础单价。</span>
    </div>

    <p v-if="preview" class="bg-muted/40 rounded-md border px-3 py-2 text-xs leading-relaxed tabular-nums" data-testid="tier-preview" aria-live="polite">
      <span class="text-muted-foreground mr-1.5">阶梯预览</span>{{ preview }}
    </p>
  </div>
</template>
