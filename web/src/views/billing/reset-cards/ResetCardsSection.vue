<script setup lang="ts">
import type { MyResetCards, ResetCard, ResetCardUseResult } from '@/lib/types'
import { computed, onMounted, ref } from 'vue'
import { ChevronDown, RotateCcw } from '@lucide/vue'
import ErrorState from '@/components/ErrorState.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { resetCardsApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import {
  cardExpiryText,
  cardHistory,
  groupUsableCards,
  planRestrictionText,
  RESET_CARD_KIND_HINTS,
  RESET_CARD_KIND_LABELS,
  RESET_CARD_STATUS_LABELS,
} from '@/lib/resetCards'
import UseResetCardDialog from './UseResetCardDialog.vue'

/** 我的重置卡 (phase11-api.md §2.2) on the billing page; hidden until loaded and while the user has no cards. */
const props = defineProps<{ now: number }>()
const emit = defineEmits<{ used: [result: ResetCardUseResult] }>()

const data = ref<MyResetCards | null>(null)
const loading = ref(false)
const loadError = ref<unknown>(null)
async function load() {
  loading.value = true
  loadError.value = null
  try {
    data.value = await resetCardsApi.mine()
  }
  catch (err) {
    loadError.value = err
  }
  finally {
    loading.value = false
  }
}
onMounted(load)
defineExpose({ load })

const groups = computed(() => groupUsableCards(data.value?.items ?? []))
const history = computed(() => cardHistory(data.value?.items ?? []))
const historyOpen = ref(false)

const useOpen = ref(false)
const using = ref<ResetCard | null>(null)
function openUse(card: ResetCard) {
  using.value = card
  useOpen.value = true
}
function onUsed(res: ResetCardUseResult) {
  void load()
  emit('used', res)
}

function historyText(c: ResetCard): string {
  if (c.status === 'used')
    return `${formatDateTime(c.usedAt)} 用于「${c.subscription?.planName ?? '已删除的订阅'}」`
  if (c.status === 'revoked')
    return `${formatDateTime(c.revokedAt)} 被管理员作废`
  return `${formatDateTime(c.expiresAt)} 过期`
}
</script>

<template>
  <section v-if="loadError || (data && data.items.length > 0)" class="space-y-3" aria-labelledby="my-cards-title" data-testid="my-reset-cards">
    <div class="space-y-1">
      <h2 id="my-cards-title" class="flex items-center gap-2 text-base font-semibold">
        <RotateCcw class="size-4" />
        我的重置卡
      </h2>
      <p class="text-muted-foreground text-sm">
        对有效订阅使用：对应额度立即清零，窗口从使用时起重新计时。每张卡只能使用一次。
      </p>
    </div>
    <ErrorState v-if="loadError" :error="loadError" @retry="load" />
    <template v-else-if="data">
      <div v-if="groups.length" class="grid gap-3 md:grid-cols-2 xl:grid-cols-3" :class="loading ? 'opacity-60 transition-opacity' : ''">
        <div v-for="g in groups" :key="g.kind" class="bg-card ring-foreground/10 flex min-w-0 flex-col gap-3 rounded-xl p-4 ring-1" :data-testid="`card-group-${g.kind}`">
          <div class="flex items-start justify-between gap-2">
            <div class="min-w-0 space-y-0.5">
              <p class="font-medium">
                {{ RESET_CARD_KIND_LABELS[g.kind] }}
              </p>
              <p class="text-muted-foreground text-xs">
                {{ RESET_CARD_KIND_HINTS[g.kind] }}
              </p>
            </div>
            <span class="shrink-0 text-2xl font-semibold tabular-nums" :aria-label="`${g.count} 张`">×{{ g.count }}</span>
          </div>
          <ul class="divide-y rounded-lg border">
            <li v-for="lot in g.lots" :key="lot.batchId" class="flex items-center gap-2 px-3 py-2">
              <div class="min-w-0 flex-1 text-xs">
                <p class="tabular-nums">
                  <span class="font-medium">{{ lot.cards.length }} 张</span>
                  <span class="text-muted-foreground"> · </span>
                  <span :title="lot.expiresAt ? `有效期至 ${formatDateTime(lot.expiresAt)}` : undefined">{{ cardExpiryText(lot.expiresAt, props.now) }}</span>
                </p>
                <p v-if="lot.plans.length" class="text-muted-foreground truncate" :title="planRestrictionText(lot.plans)">
                  {{ planRestrictionText(lot.plans) }}
                </p>
                <p v-if="lot.note" class="text-muted-foreground truncate" :title="lot.note">
                  {{ lot.note }}
                </p>
              </div>
              <Button size="sm" variant="outline" data-testid="use-card" @click="openUse(lot.cards[0]!)">
                使用
              </Button>
            </li>
          </ul>
        </div>
      </div>
      <p v-else class="text-muted-foreground rounded-xl border px-4 py-3 text-sm">
        暂无可用的重置卡。
      </p>
      <Collapsible v-if="history.length" v-model:open="historyOpen">
        <CollapsibleTrigger as-child>
          <Button variant="ghost" size="sm" class="text-muted-foreground">
            <ChevronDown class="transition-transform" :class="historyOpen ? 'rotate-180' : ''" />
            已使用 / 已过期 / 已作废（{{ history.length }}）
          </Button>
        </CollapsibleTrigger>
        <CollapsibleContent>
          <ul class="divide-y rounded-xl border text-sm">
            <li v-for="c in history" :key="c.id" class="flex flex-wrap items-center gap-x-2 gap-y-1 px-3 py-2">
              <span class="font-medium">{{ RESET_CARD_KIND_LABELS[c.kind] ?? c.kind }}</span>
              <Badge :variant="c.status === 'used' ? 'secondary' : 'outline'">
                {{ RESET_CARD_STATUS_LABELS[c.status] ?? c.status }}
              </Badge>
              <span class="text-muted-foreground min-w-0 text-xs">{{ historyText(c) }}</span>
            </li>
          </ul>
        </CollapsibleContent>
      </Collapsible>
    </template>

    <UseResetCardDialog v-model:open="useOpen" :card="using" @used="onUsed" @stale="load" />
  </section>
</template>
