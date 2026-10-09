<script setup lang="ts">
import { computed } from 'vue'

/** Monogram for a model vendor (first character), tinted by a stable hash of the name. */
const props = withDefaults(defineProps<{ vendor: string, model: string, size?: 'sm' | 'md' | 'lg' }>(), { size: 'md' })

const PALETTE = [
  'bg-sky-500/12 text-sky-700 dark:text-sky-300',
  'bg-violet-500/12 text-violet-700 dark:text-violet-300',
  'bg-emerald-500/12 text-emerald-700 dark:text-emerald-300',
  'bg-amber-500/14 text-amber-700 dark:text-amber-300',
  'bg-rose-500/12 text-rose-700 dark:text-rose-300',
  'bg-teal-500/12 text-teal-700 dark:text-teal-300',
  'bg-indigo-500/12 text-indigo-700 dark:text-indigo-300',
]

const source = computed(() => props.vendor.trim() || props.model)
const letter = computed(() => [...source.value][0]?.toUpperCase() ?? '?')
const tint = computed(() => {
  let h = 0
  for (const ch of source.value.toLowerCase())
    h = (h * 31 + ch.charCodeAt(0)) >>> 0
  return PALETTE[h % PALETTE.length]
})
const sizeClass = computed(() => ({ sm: 'size-8 text-sm', md: 'size-10 text-base', lg: 'size-12 text-lg' }[props.size]))
</script>

<template>
  <span class="flex shrink-0 items-center justify-center rounded-lg font-semibold select-none" :class="[tint, sizeClass]" aria-hidden="true">
    {{ letter }}
  </span>
</template>
