<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import type { SuggestOptionsInput } from '@/lib/suggest'
import { computed, nextTick, ref, useTemplateRef } from 'vue'
import { Check, ChevronDown, Plus, X } from '@lucide/vue'
import { AutocompleteInput, AutocompleteRoot, ComboboxAnchor, ComboboxTrigger } from 'reka-ui'
import { ComboboxItem, ComboboxList, ComboboxViewport } from '@/components/ui/combobox'
import { InputGroup, InputGroupAddon, InputGroupButton } from '@/components/ui/input-group'
import { filterSuggestions, highlightParts, isKnownSuggestion, normalizeSuggestOptions } from '@/lib/suggest'
import { cn } from '@/lib/utils'

/**
 * Free text with suggestions (replaces `<input list> + <datalist>`): a shadcn-styled
 * input whose popup filters `options` as you type. Values outside the list are allowed
 * ("使用 “xxx”"). ↑↓ move, Enter picks the highlighted suggestion, Esc closes.
 *
 * Enter on the typed text itself (or with the popup closed) is not swallowed: it emits
 * `enter` and, inside a `<form>`, submits it — like the native input did.
 * Other attributes (aria-label, name, …) go to the `<input>`.
 */
defineOptions({ inheritAttrs: false })

const props = withDefaults(defineProps<{
  options: SuggestOptionsInput
  id?: string
  placeholder?: string
  disabled?: boolean
  /** Sets aria-invalid (destructive border). */
  invalid?: boolean
  /** Monospace text, for model names. */
  mono?: boolean
  /** Show an × button while there is text. */
  clearable?: boolean
  /** Offer the typed text when it is not an option. Default true. */
  creatable?: boolean
  /** Values to leave out of the suggestions (e.g. already added). */
  exclude?: readonly string[]
  /** Most suggestions rendered at once. */
  limit?: number
  maxlength?: number
  /** Verb of the creatable entry: 「使用 “xxx”」. */
  createVerb?: string
  /** Text when nothing matches and nothing can be created. */
  emptyText?: string
  /** Classes for the outer wrapper (width etc.). */
  class?: HTMLAttributes['class']
  inputClass?: HTMLAttributes['class']
}>(), {
  id: undefined,
  placeholder: undefined,
  disabled: false,
  invalid: false,
  mono: false,
  clearable: false,
  creatable: true,
  exclude: () => [],
  limit: 100,
  maxlength: undefined,
  createVerb: '使用',
  emptyText: '没有匹配的选项',
  class: undefined,
  inputClass: undefined,
})

const emit = defineEmits<{
  /** A suggestion (or the creatable entry) was picked with the mouse or keyboard. */
  select: [value: string]
  /** Enter that did not pick a suggestion. */
  enter: [event: KeyboardEvent]
  /** The × button cleared the text. */
  clear: []
}>()

const model = defineModel<string>({ default: '' })
const open = ref(false)
/** Opened on a value that is already an option: list everything until the user types. */
const browsing = ref(false)
/** Value of the highlighted entry while the popup is open. */
const highlighted = ref<string | null>(null)
const inputRef = useTemplateRef<{ $el: HTMLInputElement }>('input')

const normalized = computed(() => normalizeSuggestOptions(props.options))
const result = computed(() => filterSuggestions(
  normalized.value,
  browsing.value ? '' : model.value,
  { limit: props.limit, creatable: props.creatable, exclude: props.exclude },
))
const query = computed(() => (browsing.value ? '' : model.value))
const hasContent = computed(() => result.value.items.length > 0 || result.value.create !== null || model.value.trim() !== '')

function onOpenChange(v: boolean) {
  if (v)
    browsing.value = isKnownSuggestion(normalized.value, model.value)
  else
    highlighted.value = null
  open.value = v
}

function onHighlight(payload: { value: unknown } | undefined) {
  highlighted.value = typeof payload?.value === 'string' ? payload.value : null
}

function onKeydownCapture(e: KeyboardEvent) {
  if (e.key !== 'Enter' || e.isComposing)
    return
  const current = model.value.trim()
  if (open.value && highlighted.value !== null && highlighted.value !== current)
    return // reka picks the highlighted suggestion (and prevents form submission)
  // Enter on the typed text: keep the native behaviour (form submit / caller's handler).
  if (open.value) {
    e.stopImmediatePropagation()
    open.value = false
    highlighted.value = null
  }
  emit('enter', e)
}

function onPick(value: string) {
  // The root updates v-model first; report after it has settled.
  nextTick(() => emit('select', value))
}

function clear() {
  model.value = ''
  emit('clear')
  inputRef.value?.$el.focus()
}

const inputClasses = computed(() => cn(
  'h-full min-w-0 flex-1 rounded-none border-0 bg-transparent px-2.5 py-1 text-base outline-none placeholder:text-muted-foreground md:text-sm disabled:cursor-not-allowed',
  props.mono && 'font-mono md:text-xs',
  props.inputClass,
))
</script>

<template>
  <AutocompleteRoot
    v-model="model"
    :open="open && hasContent"
    :disabled="disabled"
    ignore-filter
    open-on-click
    :class="cn('min-w-0', props.class)"
    data-slot="suggest-input"
    @update:open="onOpenChange"
    @highlight="onHighlight"
  >
    <ComboboxAnchor as-child>
      <InputGroup
        :data-disabled="disabled ? 'true' : undefined"
        class="data-[disabled=true]:cursor-not-allowed data-[disabled=true]:opacity-50"
      >
        <AutocompleteInput
          :id="id"
          ref="input"
          v-bind="$attrs"
          data-slot="input-group-control"
          :class="inputClasses"
          :placeholder="placeholder"
          :maxlength="maxlength"
          :disabled="disabled"
          :aria-invalid="invalid || undefined"
          spellcheck="false"
          autocapitalize="off"
          @keydown.capture="onKeydownCapture"
          @input="browsing = false"
        />
        <InputGroupAddon align="inline-end" class="gap-0.5">
          <InputGroupButton
            v-if="clearable && model && !disabled"
            size="icon-xs"
            aria-label="清除"
            data-slot="suggest-clear"
            @mousedown.prevent
            @click="clear"
          >
            <X />
          </InputGroupButton>
          <ComboboxTrigger
            v-if="normalized.length"
            :disabled="disabled"
            tabindex="-1"
            aria-label="显示建议"
            class="text-muted-foreground hover:text-foreground flex size-6 items-center justify-center rounded-[calc(var(--radius)-5px)] disabled:pointer-events-none"
            @mousedown.prevent
          >
            <ChevronDown class="size-4 transition-transform" :class="open && hasContent ? 'rotate-180' : ''" />
          </ComboboxTrigger>
        </InputGroupAddon>
      </InputGroup>
    </ComboboxAnchor>

    <ComboboxList
      align="start"
      :collision-padding="8"
      class="w-auto max-w-[min(28rem,var(--reka-combobox-content-available-width))] min-w-(--reka-combobox-trigger-width)"
    >
      <!-- mousedown.prevent keeps focus (and the caret) in the input while picking. -->
      <ComboboxViewport class="max-h-[min(--spacing(72),var(--reka-combobox-content-available-height))]" @mousedown.prevent>
        <ComboboxItem
          v-if="result.create !== null"
          :value="result.create"
          class="pr-2"
          data-slot="suggest-create"
          @select="onPick(result.create)"
        >
          <Plus class="text-muted-foreground" />
          <span class="min-w-0 truncate">{{ createVerb }} “<span :class="mono ? 'font-mono text-xs' : ''">{{ result.create }}</span>”</span>
        </ComboboxItem>
        <ComboboxItem
          v-for="o in result.items"
          :key="o.value"
          :value="o.value"
          :text-value="o.label"
          @select="onPick(o.value)"
        >
          <span class="min-w-0 flex-1">
            <span class="block truncate" :class="mono ? 'font-mono text-xs' : ''" :title="o.label">
              <template v-for="(p, i) in highlightParts(o.label, query)" :key="i"><mark v-if="p.match" class="text-foreground bg-transparent font-semibold">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template>
            </span>
            <span v-if="o.description" class="text-muted-foreground block truncate text-xs">{{ o.description }}</span>
          </span>
          <Check v-if="o.value === model.trim()" class="absolute right-2" />
        </ComboboxItem>
        <p v-if="result.hidden" class="text-muted-foreground px-2 py-1.5 text-xs">
          还有 {{ result.hidden }} 项，继续输入以缩小范围
        </p>
        <p v-if="!result.items.length && result.create === null" class="text-muted-foreground px-2 py-2 text-center text-xs">
          {{ emptyText }}
        </p>
      </ComboboxViewport>
    </ComboboxList>
  </AutocompleteRoot>
</template>
