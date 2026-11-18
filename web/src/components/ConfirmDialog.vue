<script setup lang="ts">
import { Loader2 } from '@lucide/vue'
import { watch } from 'vue'
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'

const props = withDefaults(defineProps<{
  title: string
  description?: string
  confirmText?: string
  cancelText?: string
  destructive?: boolean
  loading?: boolean
  /** Disables the confirm button (e.g. nothing to apply yet). */
  confirmDisabled?: boolean
}>(), {
  description: undefined,
  confirmText: '确认',
  cancelText: '取消',
  destructive: false,
  loading: false,
  confirmDisabled: false,
})

const open = defineModel<boolean>('open', { required: true })
defineEmits<{ confirm: [] }>()

let returnFocus: HTMLElement | null = null
watch(open, (value) => {
  if (value) {
    const active = document.activeElement
    returnFocus = active instanceof HTMLElement ? active : null
  }
}, { flush: 'sync' })

function onOpenChange(value: boolean) {
  if (!props.loading) open.value = value
}

function restoreFocus(event: Event) {
  if (open.value) return
  const target = returnFocus
  returnFocus = null
  if (!target?.isConnected) return
  event.preventDefault()
  target.focus({ preventScroll: true })
}
</script>

<template>
  <AlertDialog :open="open" @update:open="onOpenChange">
    <AlertDialogContent @close-auto-focus="restoreFocus">
      <AlertDialogHeader>
        <AlertDialogTitle>{{ title }}</AlertDialogTitle>
        <AlertDialogDescription v-if="description || $slots.default" as="div" class="space-y-2">
          <p v-if="description">
            {{ description }}
          </p>
          <slot />
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel :disabled="loading">
          {{ cancelText }}
        </AlertDialogCancel>
        <!-- Plain button (not AlertDialogAction) so the dialog stays open while the request runs. -->
        <Button :variant="destructive ? 'destructive' : 'default'" :disabled="loading || confirmDisabled" @click="$emit('confirm')">
          <Loader2 v-if="loading" class="animate-spin" />
          {{ confirmText }}
        </Button>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
