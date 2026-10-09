<script setup lang="ts">
// One `uiContributions` entry: title, failure/unsupported notes for the
// capabilities it binds to, the component tree and its action buttons.
import type { ResultMap } from './bindings'
import type { UiAction, UiContribution } from '@/lib/types'
import { computed, ref } from 'vue'
import { Loader2, Play } from '@lucide/vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { CAPABILITY_LABELS } from '@/lib/labels'
import { referencedCapabilities } from './bindings'
import UiNode from './UiNode.vue'

const props = withDefaults(defineProps<{
  contribution: UiContribution
  results: ResultMap
  /** Capability labels from the channel's capability list. */
  labels?: Record<string, string>
  /** Capabilities currently running (buttons show a spinner). */
  running?: ReadonlySet<string>
  /** Capabilities the viewer may trigger; others render disabled. */
  triggerable?: ReadonlySet<string> | null
  /** Editor preview: actions are inert. */
  preview?: boolean
  /** Render without the card chrome (e.g. inside another card). */
  bare?: boolean
}>(), {
  labels: () => ({}),
  running: () => new Set<string>(),
  triggerable: null,
  preview: false,
  bare: false,
})
const emit = defineEmits<{ action: [capability: string] }>()

function capLabel(name: string): string {
  return props.labels[name] || CAPABILITY_LABELS[name] || name
}

interface Note {
  capability: string
  kind: 'failed' | 'unsupported' | 'missing'
  text: string
}

const notes = computed<Note[]>(() => {
  const out: Note[] = []
  for (const cap of referencedCapabilities(props.contribution.component)) {
    const r = props.results[cap]
    if (!r) {
      out.push({ capability: cap, kind: 'missing', text: props.preview ? '还没有测试输出：运行一个该能力的测试用例后在此预览' : '尚未执行' })
    }
    else if (r.unsupported) {
      const reason = r.output && typeof r.output === 'object' && typeof (r.output as Record<string, unknown>).reason === 'string'
        ? (r.output as Record<string, string>).reason
        : ''
      out.push({ capability: cap, kind: 'unsupported', text: `不支持${reason ? `：${reason}` : ''}` })
    }
    else if (!r.ok) {
      out.push({ capability: cap, kind: 'failed', text: `最近一次执行失败${r.error ? `：${r.error}` : ''}` })
    }
  }
  return out
})

const pendingAction = ref<UiAction | null>(null)
const confirmOpen = ref(false)
function trigger(a: UiAction) {
  if (props.preview)
    return
  if (a.confirm) {
    pendingAction.value = a
    confirmOpen.value = true
    return
  }
  emit('action', a.capability)
}
function confirmAction() {
  if (pendingAction.value)
    emit('action', pendingAction.value.capability)
  confirmOpen.value = false
}
</script>

<template>
  <component :is="bare ? 'div' : Card" :class="bare ? 'space-y-3' : 'gap-3'">
    <component :is="bare ? 'div' : CardHeader" v-if="contribution.title || contribution.actions?.length" class="flex flex-wrap items-center justify-between gap-2">
      <component :is="bare ? 'p' : CardTitle" class="text-sm font-semibold">
        {{ contribution.title || '插件信息' }}
      </component>
      <div v-if="contribution.actions?.length" class="flex flex-wrap gap-2">
        <Button
          v-for="a in contribution.actions"
          :key="a.capability + a.label"
          variant="outline"
          size="sm"
          :disabled="preview || running.has(a.capability) || (triggerable !== null && !triggerable.has(a.capability))"
          :title="preview ? '预览模式下不会执行能力' : triggerable !== null && !triggerable.has(a.capability) ? '该能力不允许手动触发' : `执行 ${a.capability}`"
          @click="trigger(a)"
        >
          <Loader2 v-if="running.has(a.capability)" class="animate-spin" />
          <Play v-else />
          {{ a.label }}
        </Button>
      </div>
    </component>
    <component :is="bare ? 'div' : CardContent" class="space-y-3">
      <ul v-if="notes.length" class="space-y-1">
        <li
          v-for="n in notes"
          :key="n.capability"
          class="text-xs break-words"
          :class="n.kind === 'failed' ? 'text-destructive/80' : 'text-muted-foreground'"
        >
          {{ capLabel(n.capability) }}：{{ n.text }}
        </li>
      </ul>
      <UiNode :node="contribution.component" :results="results" />
    </component>
  </component>

  <ConfirmDialog
    v-model:open="confirmOpen"
    :title="pendingAction?.label ?? '执行能力'"
    :description="pendingAction?.confirm"
    confirm-text="执行"
    @confirm="confirmAction"
  />
</template>
