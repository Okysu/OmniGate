<script setup lang="ts">
import type { CurrencyInfo } from '@/lib/money'
import type { PlazaModel } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { Calculator, Check, Minus } from '@lucide/vue'
import CopyButton from '@/components/CopyButton.vue'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import type { PriceFieldKey } from '@/lib/billingMode'
import { priceCaption, priceSummary, showsTokenPrices } from '@/lib/billingMode'
import { formatMoney } from '@/lib/money'
import {
  CAPABILITIES,
  displayNameOf,
  estimateCallCost,
  estimateCost,
  formatTokenCount,
  audioModes,
  freeBillingLabel,
  hasAudioPrice,
  isChargedAmount,
  isImageModel,
  isMyPlazaModel,
  parseTokenInput,
  PROTOCOL_DESCRIPTIONS,
  PROTOCOL_LABELS,
  sortProtocols,
  sourceEntries,
} from '@/lib/plaza'
import { modelSnippets } from '@/lib/snippets'
import type { TierPriceKey } from '@/lib/priceTiers'
import { formatTokenThreshold, priceForPrompt } from '@/lib/priceTiers'
import TierNote from './TierNote.vue'
import { isNonUnitMultiplier, multiplierLabel } from '@/lib/groups'
import { useAuthStore } from '@/stores/auth'
import MyModelBadges from './MyModelBadges.vue'
import ScheduleNote from './ScheduleNote.vue'
import VendorMark from './VendorMark.vue'

/** Model detail: description, specs, full pricing, a cost calculator and call samples. */
const props = defineProps<{ model: PlazaModel | null, currency: CurrencyInfo | null }>()
const open = defineModel<boolean>('open', { required: true })

function money(v: string | null | undefined) {
  return formatMoney(v, props.currency)
}

const m = computed(() => props.model)
const mine = computed(() => (m.value && isMyPlazaModel(m.value) ? m.value : null))
const name = computed(() => (m.value ? displayNameOf(m.value) : ''))
const protocols = computed(() => sortProtocols(m.value?.protocols ?? []))
/** False when the model is only served by own / shared channels (no platform price applies). */
const platformPriced = computed(() => !mine.value || mine.value.billing !== 'free' || mine.value.sources.platform > 0)
const priceHeading = computed(() => (mine.value?.billing === 'free' ? '回退到平台渠道时的价格' : '价格'))

/** phase8 §1.1: the user's group multiplier and list price ("我的模型" only). */
const auth = useAuthStore()
const groupMultiplier = computed(() => (mine.value && isNonUnitMultiplier(mine.value.priceMultiplier) ? mine.value.priceMultiplier! : null))
const base = computed(() => (groupMultiplier.value ? mine.value?.basePrice ?? null : null))
const groupLine = computed(() => {
  if (!groupMultiplier.value)
    return null
  const name = auth.group?.name
  return `${name ? `你的分组：${name} ` : '你的分组倍率 '}${multiplierLabel(groupMultiplier.value)}，下表为你的价格${base.value ? '，划线为原价' : ''}`
})

/** Billing mode + main parts of the current price. */
const summary = computed(() => priceSummary(m.value?.price ?? null))
/** Token layout (input / output / cache rows first) or the rows of a per-call / per-image / audio price. */
const tokenLayout = computed(() => showsTokenPrices(summary.value))

interface PriceRow {
  key: string
  label: string
  hint: string
  value: string | null
  base: string | null
  /** Unit after the amount ("次", "张" …); token rows rely on the heading. */
  unit?: string
  /** Shown when `value` is null. */
  fallback: string
  /** Token price that context-length tiers override (phase10 §1). */
  tierKey?: TierPriceKey
}

const ROW_HINTS: Record<PriceFieldKey, string> = {
  inputPerM: '每 1M 提示词 token（不含缓存命中）',
  outputPerM: '每 1M 生成 token（含推理）',
  cacheReadPerM: '每 1M 命中提示词缓存的 token',
  cacheWritePerM: '每 1M 写入提示词缓存的 token',
  perRequest: '每次请求固定收取，与用量无关',
  perImage: '按输出图片张数计',
  imageInputPerM: '每 1M 图片输入 token',
  audioInputPerM: '每 1M 音频输入 token',
  audioOutputPerM: '每 1M 音频输出 token',
  perMinute: '转写 / 翻译按输入音频时长，按秒计',
  perMCharacters: '语音合成按输入文本字符数',
}

function baseOf(key: PriceFieldKey): string | null {
  return base.value ? base.value[key] ?? null : null
}

/** Rows of a price not billed by tokens: its main parts, then the other set prices. */
const modeRows = computed<PriceRow[]>(() => {
  const s = summary.value
  if (!s || tokenLayout.value)
    return []
  return [...s.main, ...s.extra].map(p => ({ key: p.key, label: p.label, hint: ROW_HINTS[p.key], value: p.amount, base: baseOf(p.key), unit: p.unit, fallback: '—' }))
})

const priceRows = computed<PriceRow[]>(() => {
  const p = m.value?.price
  if (!p || !tokenLayout.value)
    return []
  const b = base.value
  return [
    { key: 'input', label: '输入', hint: '提示词（不含缓存命中）', value: p.inputPerM, base: b?.inputPerM ?? null, fallback: '—', tierKey: 'inputPerM' },
    { key: 'output', label: '输出', hint: '生成内容（含推理）', value: p.outputPerM, base: b?.outputPerM ?? null, fallback: '—', tierKey: 'outputPerM' },
    { key: 'cacheRead', label: '缓存读', hint: '命中提示词缓存的部分', value: p.cacheReadPerM, base: b?.cacheReadPerM ?? null, fallback: '—', tierKey: 'cacheReadPerM' },
    { key: 'cacheWrite', label: '缓存写', hint: '写入提示词缓存的部分', value: p.cacheWritePerM, base: b?.cacheWritePerM ?? null, fallback: '—', tierKey: 'cacheWritePerM' },
  ]
})
/** phase7 §1.1 image prices; only when the backend sends them. */
const imagePriceRows = computed<PriceRow[]>(() => {
  const p = m.value?.price
  if (!p || !tokenLayout.value || (!isChargedAmount(p.perImage) && p.imageInputPerM == null))
    return []
  return [
    { key: 'perImage', label: '每张图片', hint: '按输出图片张数计', value: isChargedAmount(p.perImage) ? p.perImage : null, base: baseOf('perImage'), unit: '张', fallback: '—' },
    { key: 'imageInput', label: '图片输入', hint: '每 1M 图片输入 token', value: p.imageInputPerM ?? null, base: baseOf('imageInputPerM'), fallback: '按输入价', tierKey: 'imageInputPerM' },
  ]
})

/** phase9 §1.1 audio prices; only when the backend sends them. */
const audioPriceRows = computed<PriceRow[]>(() => {
  const p = m.value?.price
  if (!p || !tokenLayout.value || !hasAudioPrice(p))
    return []
  return [
    { key: 'audioInput', label: '音频输入', hint: '每 1M 音频输入 token', value: p.audioInputPerM ?? null, base: baseOf('audioInputPerM'), fallback: '按输入价', tierKey: 'audioInputPerM' },
    { key: 'audioOutput', label: '音频输出', hint: '每 1M 音频输出 token', value: p.audioOutputPerM ?? null, base: baseOf('audioOutputPerM'), fallback: '按输出价', tierKey: 'audioOutputPerM' },
    { key: 'perMinute', label: '每分钟音频', hint: '转写 / 翻译按输入音频时长，按秒计', value: isChargedAmount(p.perMinute) ? p.perMinute : null, base: baseOf('perMinute'), unit: '分钟', fallback: '—' },
    { key: 'perMCharacters', label: '每 1M 字符', hint: '语音合成按输入文本字符数', value: isChargedAmount(p.perMCharacters) ? p.perMCharacters : null, base: baseOf('perMCharacters'), unit: '1M 字符', fallback: '—' },
  ]
})

/** Per-request fee on top of token prices (自定义组合). */
const perRequestRows = computed<PriceRow[]>(() => {
  const p = m.value?.price
  if (!p || !tokenLayout.value || !isChargedAmount(p.perRequest))
    return []
  return [{ key: 'perRequest', label: '每次请求', hint: '每次请求另收，与用量无关', value: p.perRequest, base: baseOf('perRequest'), unit: '次', fallback: '—' }]
})

const allPriceRows = computed(() => [...priceRows.value, ...imagePriceRows.value, ...audioPriceRows.value, ...perRequestRows.value, ...modeRows.value])

/** phase10 §1: context-length tiers (token layout only) — one price column per tier. */
const tiers = computed(() => (tokenLayout.value ? m.value?.price?.tiers ?? [] : []))
const tierColumns = computed(() => {
  const t = tiers.value
  if (!t.length)
    return []
  return [`≤${formatTokenThreshold(t[0]!.aboveInputTokens)}`, ...t.map(x => `>${formatTokenThreshold(x.aboveInputTokens)}`)]
})
/** Value (and struck list price) of a tiered row in tier column `j` (0 = base). */
function tierCell(r: PriceRow, j: number): { value: string | null, base: string | null } {
  if (j === 0 || !r.tierKey)
    return { value: r.value, base: r.base }
  const t = tiers.value[j - 1]
  const b = base.value?.tiers?.[j - 1]
  return { value: t?.[r.tierKey] ?? null, base: b?.[r.tierKey] ?? null }
}
const priceUnitCaption = computed(() => {
  if (!tokenLayout.value)
    return priceCaption(summary.value)
  return imagePriceRows.value.length || audioPriceRows.value.length || perRequestRows.value.length ? 'token 价格每 1M' : '每 1M tokens'
})

// ---------- calculator ----------
const inputTokens = ref('100000')
const outputTokens = ref('20000')
const PRESETS = [
  { label: '一次对话', input: 2000, output: 500 },
  { label: '10 万 / 2 万', input: 100000, output: 20000 },
  { label: '100 万 / 100 万', input: 1000000, output: 1000000 },
]
const inN = computed(() => parseTokenInput(inputTokens.value))
const outN = computed(() => parseTokenInput(outputTokens.value))
/** The calculator's input tokens pick the context-length tier (phase10 §1). */
const priced = computed(() => {
  const p = m.value?.price
  return p ? priceForPrompt(p, inN.value ?? 0) : null
})
const calcPrice = computed(() => priced.value?.price ?? null)
const calcTier = computed(() => priced.value?.tier ?? null)
const estimate = computed(() => estimateCost(calcPrice.value, inN.value, outN.value))
const inputPart = computed(() => estimateCost(calcPrice.value, inN.value, 0))
const outputPart = computed(() => estimateCost(calcPrice.value, 0, outN.value))
/** 按次 / 按张: requests × (perRequest + images per request × perImage). */
const callCalc = computed(() => summary.value?.mode === 'request' || summary.value?.mode === 'image')
const requestCount = ref('100')
const imagesPerRequest = ref('1')
const reqN = computed(() => parseTokenInput(requestCount.value))
const imgN = computed(() => parseTokenInput(imagesPerRequest.value))
const callEstimate = computed(() => {
  if (summary.value?.mode === 'image')
    return imgN.value === null ? null : estimateCallCost(m.value?.price ?? null, reqN.value, imgN.value)
  return estimateCallCost(m.value?.price ?? null, reqN.value, 0)
})
/** The calculator is shown for token prices and per-call / per-image prices (not for audio). */
const showCalculator = computed(() => tokenLayout.value || callCalc.value)
const tokenPerRequest = computed(() => {
  const p = m.value?.price
  return tokenLayout.value && p && isChargedAmount(p.perRequest) ? p.perRequest : null
})
/** With tiers: one preset just past the first threshold, to show the long-context price. */
const presets = computed(() => {
  const t = m.value?.price?.tiers?.[0]
  if (!tokenLayout.value || !t)
    return PRESETS
  const input = Math.ceil((t.aboveInputTokens * 1.1) / 1000) * 1000
  return [...PRESETS, { label: `长上下文 ${formatTokenThreshold(input)} / 2 万`, input, output: 20000 }]
})
function applyPreset(p: { input: number, output: number }) {
  inputTokens.value = String(p.input)
  outputTokens.value = String(p.output)
}

// ---------- samples ----------
type SampleKey = 'openaiCurl' | 'openaiPython' | 'anthropicCurl' | 'anthropicPython' | 'embeddingsCurl' | 'imagesCurl' | 'imagesPython' | 'imageEditCurl'
  | 'transcriptionCurl' | 'transcriptionPython' | 'speechCurl' | 'speechPython'
const snippets = computed(() => (m.value ? modelSnippets(window.location.origin, m.value.model) : null))
const sampleTabs = computed(() => {
  const ps = protocols.value
  // Protocols not reported: offer the chat samples; an image / audio-only model gets its own samples only.
  const caps = m.value?.capabilities
  const imageOnly = ps.length === 0 && !!(caps?.imageGeneration || caps?.audioInput || caps?.audioOutput)
  const unknown = ps.length === 0 && !imageOnly
  const tabs: { key: SampleKey, label: string }[] = []
  if (unknown || ps.includes('openai.chat'))
    tabs.push({ key: 'openaiCurl', label: 'OpenAI · curl' }, { key: 'openaiPython', label: 'OpenAI · Python' })
  if (unknown || ps.includes('anthropic.messages'))
    tabs.push({ key: 'anthropicCurl', label: 'Anthropic · curl' }, { key: 'anthropicPython', label: 'Anthropic · Python' })
  if (ps.includes('openai.embeddings') || m.value?.capabilities.embedding)
    tabs.push({ key: 'embeddingsCurl', label: 'Embeddings · curl' })
  if (m.value && isImageModel(m.value))
    tabs.push({ key: 'imagesCurl', label: '图片生成 · curl' }, { key: 'imagesPython', label: '图片生成 · Python' }, { key: 'imageEditCurl', label: '图片编辑 · curl' })
  const audio = m.value ? audioModes(m.value) : null
  const audioTabs: { key: SampleKey, label: string }[] = []
  if (audio?.transcription)
    audioTabs.push({ key: 'transcriptionCurl', label: '语音转写 · curl' }, { key: 'transcriptionPython', label: '语音转写 · Python' })
  if (audio?.speech)
    audioTabs.push({ key: 'speechCurl', label: '语音合成 · curl' }, { key: 'speechPython', label: '语音合成 · Python' })
  // Models marked as audio models open on their audio samples (the plaza lists Chat for every model).
  return caps?.audioInput || caps?.audioOutput ? [...audioTabs, ...tabs] : [...tabs, ...audioTabs]
})
const sample = ref<SampleKey>('openaiCurl')
watch(sampleTabs, (tabs) => {
  if (!tabs.some(t => t.key === sample.value))
    sample.value = tabs[0]?.key ?? 'openaiCurl'
}, { immediate: true })
// Another model: start on its first sample (audio models list their audio samples first).
watch(() => m.value?.model, () => {
  sample.value = sampleTabs.value[0]?.key ?? 'openaiCurl'
})
const sampleCode = computed(() => (snippets.value ? snippets.value[sample.value] : ''))
</script>

<template>
  <Sheet v-model:open="open">
    <SheetContent class="w-full gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[90vw] data-[side=right]:xl:max-w-3xl" data-testid="plaza-sheet">
      <template v-if="m">
        <SheetHeader class="border-b pr-12">
          <div class="flex min-w-0 items-start gap-3">
            <VendorMark :vendor="m.vendor" :model="m.model" size="lg" />
            <div class="min-w-0 flex-1 space-y-1">
              <SheetTitle class="truncate text-lg">
                {{ name }}
              </SheetTitle>
              <div class="text-muted-foreground flex min-w-0 items-center gap-1">
                <code class="truncate font-mono text-xs">{{ m.model }}</code>
                <CopyButton :value="m.model" label="复制模型名" class="shrink-0" />
              </div>
              <SheetDescription class="flex flex-wrap items-center gap-x-1.5 text-xs">
                <span v-if="m.vendor">{{ m.vendor }}</span>
                <span v-if="m.vendor && m.contextWindow" aria-hidden="true">·</span>
                <span v-if="m.contextWindow" class="tabular-nums">{{ formatTokenCount(m.contextWindow) }} 上下文</span>
                <span v-if="!m.vendor && !m.contextWindow">逻辑模型</span>
              </SheetDescription>
            </div>
          </div>
        </SheetHeader>

        <div class="flex-1 space-y-6 overflow-y-auto p-4 sm:p-6">
          <!-- 简介 -->
          <section class="space-y-2" aria-labelledby="pm-desc">
            <h3 id="pm-desc" class="text-sm font-semibold">
              简介
            </h3>
            <p v-if="m.description" class="text-muted-foreground text-sm leading-relaxed whitespace-pre-line">
              {{ m.description }}
            </p>
            <p v-else class="text-muted-foreground text-sm">
              暂无简介。
            </p>
            <div v-if="m.tags.length" class="flex flex-wrap gap-1">
              <Badge v-for="t in m.tags" :key="t" variant="secondary" class="font-normal">
                {{ t }}
              </Badge>
            </div>
          </section>

          <!-- 我的模型：来源与计费 -->
          <section v-if="mine" class="space-y-2" aria-labelledby="pm-src">
            <h3 id="pm-src" class="text-sm font-semibold">
              渠道来源与计费
            </h3>
            <MyModelBadges :model="mine" />
            <ol class="text-muted-foreground space-y-1 text-xs">
              <li v-for="s in sourceEntries(mine)" :key="s.tier">
                <span class="text-foreground font-medium">{{ s.label }}渠道 {{ s.count }} 个</span>
                ：{{ s.tier === 'platform' ? '按平台价格计费，有效订阅时计入套餐额度' : '不计费，不计入套餐额度' }}
              </li>
            </ol>
            <p v-if="mine.billing === 'free'" class="text-xs text-emerald-700 dark:text-emerald-400">
              {{ freeBillingLabel(mine) }}<template v-if="mine.sources.platform > 0">
                ；全部失败后回退到平台渠道时按下方价格计费。
              </template>
            </p>
          </section>

          <!-- 规格 -->
          <section class="space-y-3" aria-labelledby="pm-spec">
            <h3 id="pm-spec" class="text-sm font-semibold">
              规格
            </h3>
            <dl class="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <div class="bg-muted/40 rounded-lg border p-3">
                <dt class="text-muted-foreground text-xs">
                  上下文长度
                </dt>
                <dd class="mt-1 font-semibold tabular-nums" :title="m.contextWindow ? `${m.contextWindow.toLocaleString('zh-CN')} tokens` : undefined">
                  {{ formatTokenCount(m.contextWindow) }}
                </dd>
              </div>
              <div class="bg-muted/40 rounded-lg border p-3">
                <dt class="text-muted-foreground text-xs">
                  最大输出
                </dt>
                <dd class="mt-1 font-semibold tabular-nums" :title="m.maxOutput ? `${m.maxOutput.toLocaleString('zh-CN')} tokens` : undefined">
                  {{ formatTokenCount(m.maxOutput) }}
                </dd>
              </div>
              <div class="bg-muted/40 col-span-2 rounded-lg border p-3">
                <dt class="text-muted-foreground text-xs">
                  能力
                </dt>
                <dd class="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-sm">
                  <span v-for="c in CAPABILITIES" :key="c.key" class="flex items-center gap-1" :class="m.capabilities[c.key] ? '' : 'text-muted-foreground/70'" :title="c.description">
                    <Check v-if="m.capabilities[c.key]" class="size-3.5 text-emerald-600 dark:text-emerald-400" aria-hidden="true" />
                    <Minus v-else class="size-3.5" aria-hidden="true" />
                    {{ c.label }}<span class="sr-only">{{ m.capabilities[c.key] ? '：支持' : '：不支持' }}</span>
                  </span>
                </dd>
              </div>
            </dl>
            <div v-if="protocols.length" class="flex flex-wrap items-center gap-1.5 text-xs">
              <span class="text-muted-foreground">可用协议</span>
              <Badge v-for="p in protocols" :key="p" variant="outline" class="font-mono text-[10px] font-normal" :title="PROTOCOL_DESCRIPTIONS[p] ?? p">
                {{ PROTOCOL_LABELS[p] ?? p }}
              </Badge>
            </div>
          </section>

          <!-- 价格 -->
          <section v-if="!platformPriced" class="space-y-2" aria-labelledby="pm-price">
            <h3 id="pm-price" class="text-sm font-semibold">
              价格
            </h3>
            <p class="bg-muted/40 text-muted-foreground rounded-lg border p-3 text-sm">
              该模型只由你的自有 / 共享渠道提供，不经过平台渠道，<span class="text-foreground font-medium">不计费</span>。
            </p>
          </section>
          <section v-else class="space-y-3" aria-labelledby="pm-price">
            <div class="flex items-baseline justify-between gap-2">
              <h3 id="pm-price" class="text-sm font-semibold">
                {{ priceHeading }}
              </h3>
              <span class="text-muted-foreground text-xs" data-testid="sheet-price-caption">{{ priceUnitCaption }}<template v-if="currency"> · {{ currency.code }}</template></span>
            </div>
            <p v-if="!m.price" class="bg-muted/40 text-muted-foreground rounded-lg border p-3 text-sm">
              <span class="text-foreground font-medium">未定价</span>：平台未设置售价，调用不计费。
            </p>
            <div v-else class="overflow-hidden rounded-lg border" data-testid="sheet-price-table" :data-mode="summary?.mode" :data-tiers="tiers.length">
              <table class="w-full text-sm">
                <thead v-if="tierColumns.length" class="bg-muted/40 text-muted-foreground text-xs">
                  <tr class="border-b">
                    <th scope="col" class="px-3 py-1.5 text-left font-normal">
                      输入 + 缓存 token
                    </th>
                    <th v-for="(c, j) in tierColumns" :key="c" scope="col" class="px-3 py-1.5 text-right font-medium whitespace-nowrap" :class="j > 0 ? 'text-indigo-700 dark:text-indigo-300' : ''" data-testid="tier-column">
                      {{ c }}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="r in allPriceRows" :key="r.key" class="border-b last:border-0" :data-price="r.key">
                    <th scope="row" class="px-3 py-2 text-left font-normal">
                      {{ r.label }}
                      <span class="text-muted-foreground ml-1 hidden text-xs sm:inline">{{ r.hint }}</span>
                    </th>
                    <template v-if="tierColumns.length && r.tierKey">
                      <td v-for="(c, j) in tierColumns" :key="c" class="px-3 py-2 text-right font-medium whitespace-nowrap tabular-nums" :data-tier-col="j">
                        <span v-if="tierCell(r, j).value == null" class="text-muted-foreground font-normal">{{ r.fallback }}</span>
                        <template v-else>
                          <s v-if="tierCell(r, j).base != null && tierCell(r, j).base !== tierCell(r, j).value" class="text-muted-foreground mr-1.5 text-xs font-normal" title="分组倍率前的原价" data-testid="base-price">{{ money(tierCell(r, j).base) }}</s>
                          {{ money(tierCell(r, j).value) }}
                        </template>
                      </td>
                    </template>
                    <td v-else class="px-3 py-2 text-right font-medium whitespace-nowrap tabular-nums" :colspan="tierColumns.length || undefined" :title="r.value == null ? '未设置' : undefined">
                      <span v-if="r.value == null" class="text-muted-foreground font-normal">{{ r.fallback }}</span>
                      <template v-else>
                        <s v-if="r.base != null && r.base !== r.value" class="text-muted-foreground mr-1.5 text-xs font-normal" title="分组倍率前的原价" data-testid="base-price">{{ money(r.base) }}</s>
                        {{ money(r.value) }}<span v-if="r.unit" class="text-muted-foreground text-xs font-normal"> / {{ r.unit }}</span>
                      </template>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p v-if="tierColumns.length" class="text-muted-foreground text-xs" data-testid="sheet-tier-line">
              阶梯价格：按每次请求的输入 + 缓存 token 总数选档，超过阈值时整次请求的全部 token 按该档单价计费。
            </p>
            <p v-if="groupLine" class="text-muted-foreground text-xs" data-testid="sheet-group-line">
              {{ groupLine }}
            </p>
            <ScheduleNote :price="m.price" />
            <TierNote v-if="!tierColumns.length" :price="m.price" :money="money" />
            <div v-if="m.plans.length" class="flex flex-wrap items-center gap-1.5 text-xs">
              <span class="text-muted-foreground">覆盖该模型的套餐</span>
              <Badge v-for="p in m.plans" :key="p.id" variant="outline" class="border-violet-500/40 font-normal text-violet-700 dark:text-violet-300">
                {{ p.name }}
              </Badge>
            </div>
          </section>

          <!-- 费用估算 -->
          <section v-if="m.price && platformPriced && showCalculator" class="space-y-3" aria-labelledby="pm-calc" data-testid="plaza-calculator">
            <h3 id="pm-calc" class="flex items-center gap-1.5 text-sm font-semibold">
              <Calculator class="size-4" aria-hidden="true" />
              费用估算
            </h3>
            <template v-if="callCalc">
              <div class="grid gap-3 sm:grid-cols-2">
                <div class="space-y-1.5">
                  <Label for="pm-req">请求次数</Label>
                  <Input id="pm-req" v-model="requestCount" inputmode="numeric" class="tabular-nums" :aria-invalid="reqN === null" data-testid="calc-requests" />
                </div>
                <div v-if="summary?.mode === 'image'" class="space-y-1.5">
                  <Label for="pm-img">每次生成图片张数</Label>
                  <Input id="pm-img" v-model="imagesPerRequest" inputmode="numeric" class="tabular-nums" :aria-invalid="imgN === null" data-testid="calc-images" />
                </div>
              </div>
              <div class="bg-muted/40 flex flex-wrap items-end justify-between gap-3 rounded-lg border p-3" aria-live="polite">
                <p class="text-muted-foreground text-xs">
                  {{ summary?.mode === 'image' ? '按每张图片（及每次请求费用）估算' : '按每次请求价格估算' }}，不含套餐抵扣。
                </p>
                <p class="text-right">
                  <span class="text-muted-foreground block text-xs">预估费用</span>
                  <span class="text-xl font-semibold tabular-nums" data-testid="calc-result">
                    {{ callEstimate === null ? '请输入非负整数' : money(callEstimate) }}
                  </span>
                </p>
              </div>
            </template>
            <template v-else>
              <div class="grid gap-3 sm:grid-cols-2">
                <div class="space-y-1.5">
                  <Label for="pm-in">输入 tokens</Label>
                  <Input id="pm-in" v-model="inputTokens" inputmode="numeric" class="tabular-nums" :aria-invalid="inN === null" data-testid="calc-input" />
                </div>
                <div class="space-y-1.5">
                  <Label for="pm-out">输出 tokens</Label>
                  <Input id="pm-out" v-model="outputTokens" inputmode="numeric" class="tabular-nums" :aria-invalid="outN === null" data-testid="calc-output" />
                </div>
              </div>
              <div class="flex flex-wrap gap-1.5">
                <button
                  v-for="p in presets"
                  :key="p.label"
                  type="button"
                  class="hover:bg-muted focus-visible:ring-ring/50 rounded-md border px-2 py-0.5 text-xs outline-none focus-visible:ring-3"
                  @click="applyPreset(p)"
                >
                  {{ p.label }}
                </button>
              </div>
              <div class="bg-muted/40 flex flex-wrap items-end justify-between gap-3 rounded-lg border p-3" aria-live="polite">
                <div class="text-muted-foreground space-y-0.5 text-xs tabular-nums">
                  <p>输入 {{ inputPart === null ? '—' : money(inputPart) }} + 输出 {{ outputPart === null ? '—' : money(outputPart) }}</p>
                  <p v-if="calcTier" class="text-indigo-700 dark:text-indigo-300" data-testid="calc-tier">
                    输入超过 {{ formatTokenThreshold(calcTier.aboveInputTokens) }}，按长上下文档单价：输入 {{ money(calcTier.inputPerM) }} · 输出 {{ money(calcTier.outputPerM) }} / 1M
                  </p>
                  <p>按输入、输出价格估算，不含缓存折扣与套餐抵扣{{ tokenPerRequest ? `，每次请求另收 ${money(tokenPerRequest)}` : '' }}。</p>
                </div>
                <p class="text-right">
                  <span class="text-muted-foreground block text-xs">预估费用</span>
                  <span class="text-xl font-semibold tabular-nums" data-testid="calc-result">
                    {{ estimate === null ? '请输入非负整数' : money(estimate) }}
                  </span>
                </p>
              </div>
            </template>
          </section>

          <!-- 调用示例 -->
          <section class="space-y-3" aria-labelledby="pm-code">
            <h3 id="pm-code" class="text-sm font-semibold">
              调用示例
            </h3>
            <div class="pm-code min-w-0 overflow-hidden rounded-lg border">
              <div class="flex items-center gap-1 overflow-x-auto border-b border-white/10 px-2 py-1.5" role="tablist" aria-label="示例">
                <button
                  v-for="t in sampleTabs"
                  :key="t.key"
                  type="button"
                  role="tab"
                  :aria-selected="sample === t.key"
                  class="shrink-0 rounded-md px-2.5 py-1 text-xs font-medium whitespace-nowrap transition-colors focus-visible:ring-2 focus-visible:ring-white/40 focus-visible:outline-none"
                  :class="sample === t.key ? 'bg-white/10 text-white' : 'text-zinc-400 hover:text-zinc-200'"
                  @click="sample = t.key"
                >
                  {{ t.label }}
                </button>
                <CopyButton :value="sampleCode" label="复制代码" class="ml-auto shrink-0 text-zinc-300 hover:bg-white/10 hover:text-white" />
              </div>
              <pre class="overflow-x-auto p-4 font-mono text-xs leading-relaxed text-zinc-100" data-testid="plaza-snippet"><code>{{ sampleCode }}</code></pre>
              <p class="border-t border-white/10 px-4 py-2 text-[11px] text-zinc-400">
                将占位密钥替换为你在「API Keys」中创建的密钥。
              </p>
            </div>
          </section>
        </div>
      </template>
    </SheetContent>
  </Sheet>
</template>

<style scoped>
/* Code panels stay dark in both themes (like the landing page). */
.pm-code {
  background: oklch(0.18 0.01 264);
  border-color: oklch(0.3 0.01 264);
}
</style>
