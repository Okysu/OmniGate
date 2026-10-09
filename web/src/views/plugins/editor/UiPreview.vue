<script setup lang="ts">
// "UI 预览": renders the draft manifest's uiContributions with sample data taken
// from the latest test-run output of each bound capability.
import type { ResultMap } from '@/components/plugin-ui/bindings'
import type { CapabilityDecl, UiContribution } from '@/lib/types'
import { computed } from 'vue'
import { LayoutDashboard } from '@lucide/vue'
import UiContributionCard from '@/components/plugin-ui/UiContributionCard.vue'
import UiNode from '@/components/plugin-ui/UiNode.vue'

const props = defineProps<{
  /** Current manifest.json text from the editor. */
  manifestText: string | undefined
  samples: ResultMap
}>()

const parsed = computed<{ ui: UiContribution[], labels: Record<string, string>, error: string | null }>(() => {
  if (props.manifestText === undefined)
    return { ui: [], labels: {}, error: '草稿中没有 manifest.json' }
  try {
    const m = JSON.parse(props.manifestText) as { uiContributions?: unknown, capabilities?: Record<string, CapabilityDecl> }
    const ui = Array.isArray(m.uiContributions)
      ? m.uiContributions.filter((c): c is UiContribution => c !== null && typeof c === 'object' && typeof (c as UiContribution).component === 'object' && (c as UiContribution).component !== null)
      : []
    const labels: Record<string, string> = {}
    for (const [k, v] of Object.entries(m.capabilities ?? {})) {
      if (v?.label)
        labels[k] = v.label
    }
    return { ui, labels, error: null }
  }
  catch (err) {
    return { ui: [], labels: {}, error: `manifest.json 不是合法 JSON：${err instanceof Error ? err.message : String(err)}` }
  }
})

const SLOTS: Array<{ slot: string, label: string }> = [
  { slot: 'channel.detail.overview', label: '渠道详情 · 概览' },
  { slot: 'channel.detail.capabilities', label: '渠道详情 · 厂商能力' },
  { slot: 'channel.list.badge', label: '渠道列表 · 名称旁徽标' },
]
const grouped = computed(() => SLOTS
  .map(s => ({ ...s, items: parsed.value.ui.filter(c => c.slot === s.slot) }))
  .filter(g => g.items.length > 0))
const unknownSlots = computed(() => parsed.value.ui.filter(c => !SLOTS.some(s => s.slot === c.slot)).map(c => c.slot))
const sampled = computed(() => Object.keys(props.samples))
</script>

<template>
  <div class="h-full overflow-y-auto p-3">
    <p v-if="parsed.error" class="text-destructive text-sm">
      {{ parsed.error }}
    </p>
    <div v-else-if="parsed.ui.length === 0" class="text-muted-foreground flex h-full flex-col items-center justify-center gap-2 text-center text-sm">
      <LayoutDashboard class="size-6" />
      manifest.json 中没有 uiContributions。
    </div>
    <div v-else class="space-y-4">
      <p class="text-muted-foreground text-xs">
        预览使用 manifest.json 当前内容（未保存也会生效）；示例数据取自本次会话中各能力最近一次测试的输出：
        <span v-if="sampled.length" class="font-mono">{{ sampled.join('、') }}</span>
        <span v-else>暂无，请先运行测试用例。</span>
        动作按钮在预览中不会执行。
      </p>
      <p v-if="unknownSlots.length" class="text-destructive text-xs">
        不支持的插槽：{{ unknownSlots.join('、') }}
      </p>
      <section v-for="g in grouped" :key="g.slot" class="space-y-2">
        <h4 class="text-muted-foreground text-xs font-medium">
          {{ g.label }} <span class="font-mono">{{ g.slot }}</span>
        </h4>
        <div v-if="g.slot === 'channel.list.badge'" class="flex flex-wrap items-center gap-2 rounded-lg border p-3 text-sm">
          <span class="font-medium">示例渠道</span>
          <UiNode v-for="(c, i) in g.items" :key="i" :node="c.component" :results="samples" />
        </div>
        <div v-else class="grid gap-3 xl:grid-cols-2">
          <UiContributionCard v-for="(c, i) in g.items" :key="i" :contribution="c" :results="samples" :labels="parsed.labels" preview />
        </div>
      </section>
    </div>
  </div>
</template>
