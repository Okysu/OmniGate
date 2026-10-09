<script setup lang="ts">
/**
 * Small dependency-free SVG column chart (stacked or grouped) with a legend,
 * hover tooltip and theme-aware colours. Colours come from CSS variables
 * (`--viz-*`) defined below for light and dark mode.
 */
import { computed, ref, useTemplateRef } from 'vue'
import { useElementSize } from '@vueuse/core'

export interface ColumnSeries {
  key: string
  label: string
  /** One of the palette slots defined in this component. */
  color: 'blue' | 'orange' | 'red' | 'aqua'
  values: number[]
}

const props = withDefaults(defineProps<{
  categories: string[]
  /** Tooltip / axis label for a category (defaults to the raw value). */
  categoryLabel?: (c: string) => string
  series: ColumnSeries[]
  stacked?: boolean
  height?: number
  formatValue?: (n: number) => string
  formatAxis?: (n: number) => string
  chartLabel: string
}>(), {
  categoryLabel: (c: string) => c,
  stacked: false,
  height: 220,
  formatValue: (n: number) => n.toLocaleString('zh-CN'),
  formatAxis: undefined,
})

const root = useTemplateRef<HTMLDivElement>('root')
const { width } = useElementSize(root)

const PAD = { top: 8, right: 8, bottom: 24, left: 44 }
const plotW = computed(() => Math.max(0, width.value - PAD.left - PAD.right))
const plotH = computed(() => props.height - PAD.top - PAD.bottom)

function niceMax(v: number): number {
  if (v <= 0)
    return 1
  const exp = 10 ** Math.floor(Math.log10(v))
  const f = v / exp
  const nice = f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10
  return nice * exp
}

const maxValue = computed(() => {
  let m = 0
  props.categories.forEach((_, i) => {
    if (props.stacked)
      m = Math.max(m, props.series.reduce((s, se) => s + (se.values[i] ?? 0), 0))
    else
      for (const se of props.series) m = Math.max(m, se.values[i] ?? 0)
  })
  return niceMax(m)
})

const ticks = computed(() => [0, 0.25, 0.5, 0.75, 1].map(f => f * maxValue.value))
const y = (v: number) => PAD.top + plotH.value - (v / maxValue.value) * plotH.value
const axisFmt = computed(() => props.formatAxis ?? ((n: number) => {
  if (n >= 1e9)
    return `${+(n / 1e9).toFixed(1)}B`
  if (n >= 1e6)
    return `${+(n / 1e6).toFixed(1)}M`
  if (n >= 1e3)
    return `${+(n / 1e3).toFixed(1)}K`
  return `${+n.toFixed(2)}`
}))

const band = computed(() => (props.categories.length ? plotW.value / props.categories.length : 0))

/** Path for a bar whose top corners are rounded (square at the baseline). */
function barPath(x: number, top: number, w: number, bottom: number, rounded: boolean): string {
  const h = bottom - top
  if (h <= 0 || w <= 0)
    return ''
  const r = rounded ? Math.min(4, w / 2, h) : 0
  return `M${x},${bottom}V${top + r}${r ? `Q${x},${top} ${x + r},${top}` : ''}H${x + w - r}${r ? `Q${x + w},${top} ${x + w},${top + r}` : ''}V${bottom}Z`
}

interface Bar { key: string, color: string, d: string }

const bars = computed<Bar[]>(() => {
  const out: Bar[] = []
  const b = band.value
  const base = y(0)
  props.categories.forEach((_, i) => {
    const x0 = PAD.left + i * b
    if (props.stacked) {
      const w = Math.min(24, b * 0.7)
      const x = x0 + (b - w) / 2
      let acc = 0
      const visible = props.series.filter(s => (s.values[i] ?? 0) > 0)
      visible.forEach((s, si) => {
        const v = s.values[i] ?? 0
        const bottom = y(acc) - (si > 0 ? 2 : 0) // 2px surface gap between segments
        acc += v
        const top = y(acc)
        out.push({ key: `${s.key}-${i}`, color: s.color, d: barPath(x, Math.min(top, bottom), w, bottom, si === visible.length - 1) })
      })
    }
    else {
      const k = props.series.length
      const gap = 2
      const w = Math.max(1, Math.min(16, (b * 0.8 - gap * (k - 1)) / k))
      const groupW = w * k + gap * (k - 1)
      props.series.forEach((s, si) => {
        const v = s.values[i] ?? 0
        const x = x0 + (b - groupW) / 2 + si * (w + gap)
        out.push({ key: `${s.key}-${i}`, color: s.color, d: barPath(x, y(v), w, base, true) })
      })
    }
  })
  return out
})

/** Show at most ~8 x labels. */
const labelEvery = computed(() => Math.max(1, Math.ceil(props.categories.length / Math.max(1, Math.floor(plotW.value / 64)))))

const hover = ref<number | null>(null)
const tooltipStyle = computed(() => {
  if (hover.value === null)
    return {}
  const cx = PAD.left + (hover.value + 0.5) * band.value
  const left = Math.min(Math.max(cx, 80), Math.max(80, width.value - 80))
  return { left: `${left}px`, top: `${PAD.top}px` }
})
</script>

<template>
  <div class="viz-root space-y-2">
    <ul class="text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs">
      <li v-for="s in series" :key="s.key" class="flex items-center gap-1.5">
        <span class="inline-block size-2.5 rounded-[3px]" :style="{ background: `var(--viz-${s.color})` }" />
        {{ s.label }}
      </li>
    </ul>
    <div ref="root" class="relative w-full" :style="{ height: `${height}px` }">
      <svg v-if="width > 0" :width="width" :height="height" role="img" :aria-label="chartLabel" class="block">
        <g>
          <template v-for="t in ticks" :key="t">
            <line :x1="PAD.left" :x2="width - PAD.right" :y1="y(t)" :y2="y(t)" class="stroke-border" stroke-width="1" />
            <text :x="PAD.left - 6" :y="y(t)" text-anchor="end" dominant-baseline="middle" class="fill-muted-foreground text-[10px] tabular-nums">
              {{ axisFmt(t) }}
            </text>
          </template>
        </g>
        <path v-for="b in bars" :key="b.key" :d="b.d" :style="{ fill: `var(--viz-${b.color})` }" />
        <g>
          <template v-for="(c, i) in categories" :key="c">
            <text
              v-if="i % labelEvery === 0"
              :x="PAD.left + (i + 0.5) * band"
              :y="height - 6"
              text-anchor="middle"
              class="fill-muted-foreground text-[10px]"
            >
              {{ categoryLabel(c) }}
            </text>
          </template>
        </g>
        <rect
          v-if="hover !== null"
          :x="PAD.left + hover * band"
          :y="PAD.top"
          :width="band"
          :height="plotH"
          class="fill-foreground/5 pointer-events-none"
        />
        <rect
          v-for="(c, i) in categories"
          :key="`hit-${c}`"
          :x="PAD.left + i * band"
          :y="PAD.top"
          :width="band"
          :height="plotH"
          fill="transparent"
          @pointerenter="hover = i"
          @pointerleave="hover = null"
        />
      </svg>
      <div
        v-if="hover !== null"
        class="bg-popover text-popover-foreground ring-foreground/10 pointer-events-none absolute z-10 -translate-x-1/2 rounded-md px-2.5 py-1.5 text-xs shadow-md ring-1"
        :style="tooltipStyle"
      >
        <p class="mb-1 font-medium">
          {{ categoryLabel(categories[hover] ?? '') }}
        </p>
        <p v-for="s in series" :key="s.key" class="flex items-center gap-1.5 whitespace-nowrap">
          <span class="inline-block size-2 rounded-[2px]" :style="{ background: `var(--viz-${s.color})` }" />
          <span class="text-muted-foreground">{{ s.label }}</span>
          <span class="ml-auto pl-3 tabular-nums">{{ formatValue(s.values[hover] ?? 0) }}</span>
        </p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.viz-root {
  --viz-blue: #2a78d6;
  --viz-orange: #eb6834;
  --viz-aqua: #1baf7a;
  --viz-red: #e34948;
}
:global(.dark) .viz-root {
  --viz-blue: #3987e5;
  --viz-orange: #d95926;
  --viz-aqua: #199e70;
  --viz-red: #e66767;
}
</style>
