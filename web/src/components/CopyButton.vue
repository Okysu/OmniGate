<script setup lang="ts">
import { ref } from 'vue'
import { Check, Copy } from '@lucide/vue'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'

const props = withDefaults(defineProps<{
  value: string
  label?: string
  /** Show the label next to the icon. */
  showLabel?: boolean
  size?: 'xs' | 'sm' | 'icon-xs' | 'icon-sm' | 'default'
  variant?: 'ghost' | 'outline' | 'secondary' | 'default'
}>(), {
  label: '复制',
  showLabel: false,
  size: undefined,
  variant: 'ghost',
})

const copied = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined

async function writeClipboard(text: string): Promise<void> {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text)
    return
  }
  // Fallback for non-secure origins (plain-HTTP self-hosting).
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  const ok = document.execCommand('copy')
  ta.remove()
  if (!ok)
    throw new Error('copy failed')
}

async function copy() {
  try {
    await writeClipboard(props.value)
    copied.value = true
    clearTimeout(timer)
    timer = setTimeout(() => (copied.value = false), 1500)
  }
  catch {
    toast.error('复制失败，请手动选择文本复制')
  }
}
</script>

<template>
  <Button
    type="button"
    :variant="variant"
    :size="size ?? (showLabel ? 'sm' : 'icon-xs')"
    :aria-label="label"
    :title="label"
    @click.stop="copy"
  >
    <Check v-if="copied" class="text-emerald-600 dark:text-emerald-400" />
    <Copy v-else />
    <span v-if="showLabel">{{ copied ? '已复制' : label }}</span>
  </Button>
</template>
