<script setup lang="ts">
import type { ComponentPublicInstance } from 'vue'
import { onBeforeUnmount, ref, useId, watch } from 'vue'
import { PopoverAnchor, PopoverRoot } from 'reka-ui'
import { PopoverContent } from '@/components/ui/popover'
import { cn } from '@/lib/utils'

/**
 * Small informational hover card (分时价格, 阶梯价格, 长上下文档 …): a title row, a body
 * slot and an optional footer. The `trigger` slot's single element becomes the anchor.
 *
 * Opens on mouse hover (short delays, the pointer may move into the card), on keyboard
 * focus, and on click / tap — a click pins it open until a second click, a click outside
 * or Esc. Touch has no hover: a tap opens, the next tap closes.
 */
const props = withDefaults(defineProps<{
  title: string
  /** Shown after the title, muted: "分时价格 · Asia/Shanghai". */
  subtitle?: string | null
  footer?: string | null
  side?: 'top' | 'bottom' | 'left' | 'right'
  align?: 'start' | 'center' | 'end'
  contentClass?: string
  /** data-testid of the card. */
  testid?: string
}>(), { subtitle: null, footer: null, side: 'top', align: 'center', contentClass: '', testid: 'info-hover-card' })

const OPEN_DELAY = 120
const CLOSE_DELAY = 160

const open = ref(false)
/** Why it is open: a pinned card ignores hover / blur. */
let reason: 'hover' | 'focus' | 'pinned' | null = null
let timer: ReturnType<typeof setTimeout> | undefined
const contentId = useId()
const anchor = ref<ComponentPublicInstance | null>(null)

function clearTimer() {
  if (timer !== undefined)
    clearTimeout(timer)
  timer = undefined
}
function show(why: 'hover' | 'focus', delay = 0) {
  clearTimer()
  if (open.value)
    return
  const go = () => {
    open.value = true
    reason = why
  }
  if (delay)
    timer = setTimeout(go, delay)
  else
    go()
}
function hide(delay = 0) {
  clearTimer()
  if (reason === 'pinned')
    return
  const go = () => {
    open.value = false
  }
  if (delay)
    timer = setTimeout(go, delay)
  else
    go()
}
watch(open, (v) => {
  if (!v)
    reason = null
})
onBeforeUnmount(clearTimer)

function triggerEl(): HTMLElement | null {
  const el = anchor.value?.$el as unknown
  return el instanceof HTMLElement ? el : null
}

function onPointerEnter(e: PointerEvent) {
  if (e.pointerType === 'mouse')
    show('hover', OPEN_DELAY)
}
function onPointerLeave(e: PointerEvent) {
  if (e.pointerType === 'mouse' && reason !== 'pinned')
    hide(CLOSE_DELAY)
}
function onFocus() {
  show('focus')
}
function onBlur(e: FocusEvent) {
  const next = e.relatedTarget as Node | null
  if (next && document.getElementById(contentId)?.contains(next))
    return
  if (reason !== 'pinned')
    hide()
}
function toggle() {
  clearTimer()
  if (open.value && reason === 'pinned') {
    open.value = false
    return
  }
  open.value = true
  reason = 'pinned'
}
function onClick(e: MouseEvent) {
  // The trigger often sits in a clickable row / stretched card link.
  e.stopPropagation()
  e.preventDefault()
  toggle()
}
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault()
    e.stopPropagation()
    toggle()
  }
  else if (e.key === 'Escape' && open.value) {
    e.stopPropagation()
    open.value = false
  }
}
function onContentEnter() {
  if (reason === 'hover')
    clearTimer()
}
function onContentLeave(e: PointerEvent) {
  if (e.pointerType === 'mouse' && reason === 'hover')
    hide(CLOSE_DELAY)
}
/** A press on the trigger is handled by its click (toggle), not as an outside press. */
function onInteractOutside(e: Event) {
  const t = e.target as Node | null
  if (t && triggerEl()?.contains(t))
    e.preventDefault()
}
</script>

<template>
  <PopoverRoot v-model:open="open">
    <PopoverAnchor
      ref="anchor"
      as-child
      role="button"
      tabindex="0"
      aria-haspopup="dialog"
      :aria-expanded="open"
      :aria-controls="open ? contentId : undefined"
      :data-state="open ? 'open' : 'closed'"
      @pointerenter="onPointerEnter"
      @pointerleave="onPointerLeave"
      @focus="onFocus"
      @blur="onBlur"
      @click="onClick"
      @keydown="onKeydown"
    >
      <slot name="trigger" />
    </PopoverAnchor>
    <PopoverContent
      :id="contentId"
      :side="side"
      :align="align"
      :side-offset="6"
      :collision-padding="12"
      :class="cn('w-auto max-w-[min(24rem,calc(100vw-1.5rem))] min-w-52 gap-0 overflow-hidden p-0 text-xs', props.contentClass)"
      :aria-label="subtitle ? `${title} · ${subtitle}` : title"
      :data-testid="testid"
      @open-auto-focus.prevent
      @close-auto-focus.prevent
      @interact-outside="onInteractOutside"
      @pointerenter="onContentEnter"
      @pointerleave="onContentLeave"
    >
      <div class="bg-muted/50 flex items-center gap-1.5 border-b px-3 py-2 leading-tight">
        <slot name="icon" />
        <span class="font-medium whitespace-nowrap">{{ title }}</span>
        <template v-if="subtitle">
          <span class="text-muted-foreground" aria-hidden="true">·</span>
          <span class="text-muted-foreground truncate">{{ subtitle }}</span>
        </template>
      </div>
      <div class="px-3 py-2">
        <slot />
      </div>
      <div v-if="footer || $slots.footer" class="text-muted-foreground border-t px-3 py-1.5 text-[11px] leading-snug" data-slot="hover-card-footer">
        <slot name="footer">
          {{ footer }}
        </slot>
      </div>
    </PopoverContent>
  </PopoverRoot>
</template>
