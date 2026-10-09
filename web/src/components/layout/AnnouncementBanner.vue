<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Megaphone, X } from '@lucide/vue'
import { dismissAnnouncement, isAnnouncementDismissed } from '@/lib/site'

/**
 * Site-wide announcement (`site.announcement`). Plain text only; dismissal is
 * remembered per announcement text, so a new announcement shows up again.
 * `preview` renders the banner without the dismiss state (settings page).
 */
const props = withDefaults(defineProps<{ text: string, preview?: boolean }>(), { preview: false })

const dismissed = ref(false)
watch(() => props.text, (t) => {
  dismissed.value = !props.preview && isAnnouncementDismissed(t)
}, { immediate: true })

const visible = computed(() => props.text.trim() !== '' && !dismissed.value)

function dismiss() {
  if (props.preview)
    return
  dismissAnnouncement(props.text)
  dismissed.value = true
}
</script>

<template>
  <div
    v-if="visible"
    class="flex items-start gap-2.5 border-b border-amber-500/30 bg-amber-50 px-4 py-2 text-sm text-amber-900 dark:bg-amber-500/10 dark:text-amber-200"
    role="status"
    data-testid="announcement"
  >
    <Megaphone class="mt-0.5 size-4 shrink-0" aria-hidden="true" />
    <p class="min-w-0 flex-1 break-words whitespace-pre-line">
      {{ text.trim() }}
    </p>
    <button
      type="button"
      class="-mr-1 shrink-0 rounded-sm p-0.5 hover:bg-amber-500/20 focus-visible:ring-2 focus-visible:ring-amber-500/50 focus-visible:outline-none"
      aria-label="关闭公告"
      :disabled="preview"
      @click="dismiss"
    >
      <X class="size-4" />
    </button>
  </div>
</template>
