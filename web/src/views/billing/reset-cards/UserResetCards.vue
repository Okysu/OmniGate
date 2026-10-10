<script setup lang="ts">
import type { ResetCard } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { ExternalLink, RotateCcw } from '@lucide/vue'
import ErrorState from '@/components/ErrorState.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { adminResetCardsApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { RESET_CARD_KIND_LABELS, RESET_CARD_KINDS, RESET_CARD_STATUS_LABELS } from '@/lib/resetCards'

/** A user's reset cards in the admin user detail (phase11-api.md §2.1, newest 50). */
const props = defineProps<{ userId: string }>()

const PAGE_SIZE = 50
const cards = ref<ResetCard[] | null>(null)
const total = ref(0)
const loadError = ref<unknown>(null)
async function load() {
  const id = props.userId
  loadError.value = null
  try {
    const res = await adminResetCardsApi.listCards({ page: 1, pageSize: PAGE_SIZE, userId: id })
    if (props.userId === id) {
      cards.value = res.items
      total.value = res.total
    }
  }
  catch (err) {
    loadError.value = err
  }
}
watch(() => props.userId, () => {
  cards.value = null
  void load()
}, { immediate: true })
defineExpose({ load })

/** Usable cards by kind among the loaded ones. */
const usable = computed(() => RESET_CARD_KINDS
  .map(k => ({ kind: k, n: (cards.value ?? []).filter(c => c.kind === k && c.status === 'available').length }))
  .filter(x => x.n > 0))

function detail(c: ResetCard): string {
  switch (c.status) {
    case 'used':
      return `${formatDateTime(c.usedAt)} 用于「${c.subscription?.planName ?? '—'}」`
    case 'revoked':
      return `${formatDateTime(c.revokedAt)} 作废`
    case 'expired':
      return `${formatDateTime(c.expiresAt)} 过期`
    default:
      return c.expiresAt ? `有效期至 ${formatDateTime(c.expiresAt)}` : '长期有效'
  }
}
</script>

<template>
  <section class="space-y-3" aria-labelledby="ud-cards" data-testid="ud-reset-cards">
    <div class="flex min-h-8 flex-wrap items-center justify-between gap-2">
      <h3 id="ud-cards" class="flex items-center gap-1.5 text-sm font-semibold">
        <RotateCcw class="size-4" />
        重置卡
        <span v-if="total > PAGE_SIZE" class="text-muted-foreground font-normal">（最近 {{ PAGE_SIZE }} 张，共 {{ total }} 张）</span>
      </h3>
      <Button variant="ghost" size="sm" as-child>
        <RouterLink to="/console/billing/reset-cards">
          <ExternalLink />
          发放重置卡
        </RouterLink>
      </Button>
    </div>
    <ErrorState v-if="loadError" :error="loadError" @retry="load" />
    <Skeleton v-else-if="cards === null" class="h-12 w-full" />
    <p v-else-if="cards.length === 0" class="text-muted-foreground rounded-lg border border-dashed p-3 text-sm">
      没有重置卡。
    </p>
    <template v-else>
      <p class="text-sm">
        可用：
        <template v-if="usable.length">
          <span v-for="(u, i) in usable" :key="u.kind">{{ i ? '、' : '' }}{{ RESET_CARD_KIND_LABELS[u.kind] }} × {{ u.n }}</span>
        </template>
        <span v-else class="text-muted-foreground">无</span>
      </p>
      <ul class="max-h-64 divide-y overflow-y-auto rounded-lg border text-sm">
        <li v-for="c in cards" :key="c.id" class="flex flex-wrap items-center gap-x-2 gap-y-1 px-3 py-2">
          <span class="font-medium">{{ RESET_CARD_KIND_LABELS[c.kind] ?? c.kind }}</span>
          <Badge :variant="c.status === 'available' ? 'default' : c.status === 'used' ? 'secondary' : 'outline'">
            {{ RESET_CARD_STATUS_LABELS[c.status] ?? c.status }}
          </Badge>
          <span class="text-muted-foreground min-w-0 text-xs">{{ detail(c) }}</span>
          <span v-if="c.note" class="text-muted-foreground min-w-0 truncate text-xs" :title="c.note">· {{ c.note }}</span>
        </li>
      </ul>
    </template>
  </section>
</template>
