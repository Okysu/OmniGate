<script setup lang="ts">
import type { Component } from 'vue'
import type { ModelCapabilities, ModelCapability } from '@/lib/types'
import { computed } from 'vue'
import { Binary, Brain, Eye, ImagePlus, Mic, Volume2, Wrench } from '@lucide/vue'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { CAPABILITIES } from '@/lib/plaza'

/** Icons for the capabilities a model has, each with a tooltip. */
const props = withDefaults(defineProps<{ capabilities: ModelCapabilities, size?: 'sm' | 'md' }>(), { size: 'sm' })

const ICONS: Record<ModelCapability, Component> = { vision: Eye, tools: Wrench, reasoning: Brain, embedding: Binary, imageGeneration: ImagePlus, audioInput: Mic, audioOutput: Volume2 }
const COLORS: Record<ModelCapability, string> = {
  vision: 'text-sky-700 bg-sky-500/10 dark:text-sky-300',
  tools: 'text-amber-700 bg-amber-500/10 dark:text-amber-300',
  reasoning: 'text-violet-700 bg-violet-500/10 dark:text-violet-300',
  embedding: 'text-teal-700 bg-teal-500/10 dark:text-teal-300',
  imageGeneration: 'text-pink-700 bg-pink-500/10 dark:text-pink-300',
  audioInput: 'text-orange-700 bg-orange-500/10 dark:text-orange-300',
  audioOutput: 'text-cyan-700 bg-cyan-500/10 dark:text-cyan-300',
}
const active = computed(() => CAPABILITIES.filter(c => props.capabilities[c.key]))
</script>

<template>
  <ul v-if="active.length" class="flex shrink-0 items-center gap-1" aria-label="模型能力">
    <li v-for="c in active" :key="c.key">
      <Tooltip>
        <TooltipTrigger as-child>
          <span
            class="relative z-10 flex items-center justify-center rounded-md"
            :class="[COLORS[c.key], size === 'md' ? 'size-7' : 'size-6']"
            tabindex="0"
            :aria-label="c.label"
            :data-capability="c.key"
          >
            <component :is="ICONS[c.key]" :class="size === 'md' ? 'size-4' : 'size-3.5'" aria-hidden="true" />
          </span>
        </TooltipTrigger>
        <TooltipContent>
          <p class="font-medium">
            {{ c.label }}
          </p>
          <p class="opacity-70">
            {{ c.description }}
          </p>
        </TooltipContent>
      </Tooltip>
    </li>
  </ul>
</template>
