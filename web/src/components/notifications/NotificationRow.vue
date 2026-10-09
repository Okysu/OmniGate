<script setup lang="ts">
import type { AppNotification } from '@/lib/types'
import { computed } from 'vue'
import { Check, ExternalLink } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { formatDateTime, formatRelative } from '@/lib/format'
import { CATEGORY_LABELS, categoryOf, eventLabel, notificationLink, severityOf } from '@/lib/notifications'
import SeverityIcon from './SeverityIcon.vue'

/**
 * One in-app notification. Clicking the row expands the body and marks it read
 * ("mark read on open"); the link button navigates (and marks it read as well).
 */
const props = withDefaults(defineProps<{
  notification: AppNotification
  /** Current time (ms) for the relative timestamp; pass a ticking value. */
  now: number
  expanded?: boolean
  /** Overview card: smaller, no body, no actions except the link. */
  compact?: boolean
}>(), { expanded: false, compact: false })
const emit = defineEmits<{ open: [], read: [], follow: [] }>()

const n = computed(() => props.notification)
const unread = computed(() => !n.value.readAt)
const severity = computed(() => severityOf(n.value))
const link = computed(() => notificationLink(n.value.link))
const category = computed(() => categoryOf(n.value.type))
</script>

<template>
  <li
    class="group/n relative flex gap-3"
    :class="compact ? 'py-2' : 'hover:bg-muted/40 px-4 py-3 transition-colors'"
    :data-unread="unread ? 'true' : undefined"
    data-testid="notification-row"
  >
    <SeverityIcon :severity="severity" :size="compact ? 'sm' : 'md'" class="mt-0.5" />
    <div class="min-w-0 flex-1">
      <button
        v-if="!compact"
        type="button"
        class="focus-visible:ring-ring/50 block w-full rounded-sm text-left outline-none focus-visible:ring-3"
        :aria-expanded="expanded"
        @click="emit('open')"
      >
        <span class="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span v-if="unread" class="bg-primary size-2 shrink-0 rounded-full" aria-hidden="true" />
          <span class="min-w-0 text-sm break-words" :class="unread ? 'font-semibold' : 'font-medium'">{{ n.title }}</span>
          <span v-if="unread" class="sr-only">（未读）</span>
          <Badge v-if="category" variant="outline" class="text-muted-foreground h-4 px-1.5 text-[10px] font-normal" :title="eventLabel(n.type)">
            {{ CATEGORY_LABELS[category] }}
          </Badge>
        </span>
        <span
          v-if="n.body"
          class="text-muted-foreground mt-1 block text-sm break-words whitespace-pre-line"
          :class="expanded ? '' : 'line-clamp-2'"
        >{{ n.body }}</span>
      </button>
      <template v-else>
        <p class="truncate text-sm" :class="unread ? 'font-medium' : ''" :title="n.title">
          {{ n.title }}
        </p>
      </template>
      <div class="text-muted-foreground mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
        <Tooltip>
          <TooltipTrigger as-child>
            <time :datetime="n.createdAt" tabindex="0" class="focus-visible:ring-ring/50 rounded-sm outline-none focus-visible:ring-2">{{ formatRelative(n.createdAt, now) }}</time>
          </TooltipTrigger>
          <TooltipContent>{{ formatDateTime(n.createdAt) }}</TooltipContent>
        </Tooltip>
        <span v-if="!compact && !category" class="font-mono">{{ n.type }}</span>
      </div>
    </div>
    <div class="flex shrink-0 items-start gap-1" :class="compact ? '' : 'pt-0.5'">
      <Button v-if="link?.kind === 'internal'" variant="outline" size="xs" as-child>
        <RouterLink :to="link.to" @click="emit('follow')">
          查看
        </RouterLink>
      </Button>
      <Button v-else-if="link?.kind === 'external'" variant="outline" size="xs" as-child>
        <a :href="link.href" target="_blank" rel="noopener noreferrer" @click="emit('follow')">
          查看
          <ExternalLink />
        </a>
      </Button>
      <Tooltip v-if="!compact && unread">
        <TooltipTrigger as-child>
          <Button variant="ghost" size="icon-xs" aria-label="标为已读" @click="emit('read')">
            <Check />
          </Button>
        </TooltipTrigger>
        <TooltipContent>标为已读</TooltipContent>
      </Tooltip>
    </div>
  </li>
</template>
