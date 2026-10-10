<script setup lang="ts">
import type { ResetCard, ResetCardPreview, ResetCardUseResult } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { ArrowRight, Loader2, TriangleAlert } from '@lucide/vue'
import { toast } from 'vue-sonner'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, isApiError } from '@/lib/api'
import { resetCardsApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { planRestrictionText, RESET_CARD_ERROR_MESSAGES, RESET_CARD_KIND_HINTS, RESET_CARD_KIND_LABELS, ruleChange } from '@/lib/resetCards'

/** 使用重置卡 (phase11-api.md §2.3): choose a subscription, see before → after, confirm. */
const props = defineProps<{ card: ResetCard | null }>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{
  used: [result: ResetCardUseResult]
  /** The card can no longer be used (used / expired / revoked elsewhere): reload the list. */
  stale: []
}>()
const { money } = useCurrency()

const preview = ref<ResetCardPreview | null>(null)
const loading = ref(false)
const loadError = ref<unknown>(null)
const selected = ref('')
const saving = ref(false)
const useError = ref<string | null>(null)
/** Client time the preview was loaded; "after" refresh times are relative to it. */
const openedAt = ref(Date.now())

async function load() {
  const c = props.card
  if (!c)
    return
  loading.value = true
  loadError.value = null
  try {
    preview.value = await resetCardsApi.preview(c.id)
    openedAt.value = Date.now()
    selected.value = preview.value.subscriptions[0]?.id ?? ''
  }
  catch (err) {
    loadError.value = err
  }
  finally {
    loading.value = false
  }
}

// The parent sets the card and opens the dialog in one tick: react to both props.
watch(() => [open.value, props.card?.id] as const, ([o, id]) => {
  if (!o || !id)
    return
  preview.value = null
  selected.value = ''
  useError.value = null
  void load()
}, { immediate: true })

const target = computed(() => preview.value?.subscriptions.find(s => s.id === selected.value) ?? null)
const changes = computed(() => (target.value ? target.value.rules.map(r => ruleChange(r, openedAt.value, money)) : []))

async function submit() {
  const c = props.card
  if (!c || !target.value || saving.value)
    return
  saving.value = true
  useError.value = null
  try {
    const res = await resetCardsApi.use(c.id, target.value.id)
    const titles = changes.value.filter(x => res.rules.includes(x.id)).map(x => x.title).join('、')
    toast.success(`已使用${RESET_CARD_KIND_LABELS[c.kind]}`, { description: `「${res.subscription.plan.name}」的${titles || '额度'}已清零，并从现在起重新计时。` })
    emit('used', res)
    open.value = false
  }
  catch (err) {
    const code = isApiError(err) ? err.code : ''
    useError.value = RESET_CARD_ERROR_MESSAGES[code] ?? errorMessage(err)
    if (code === 'card_used' || code === 'card_expired' || code === 'card_revoked')
      emit('stale')
  }
  finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-lg" data-testid="use-card-dialog">
      <DialogHeader>
        <DialogTitle>使用{{ card ? RESET_CARD_KIND_LABELS[card.kind] : '重置卡' }}</DialogTitle>
        <DialogDescription>
          {{ card ? RESET_CARD_KIND_HINTS[card.kind] : '' }}。每张卡只能使用一次，使用后不可撤销。
          <template v-if="card && card.plans.length">
            {{ planRestrictionText(card.plans) }}。
          </template>
        </DialogDescription>
      </DialogHeader>

      <ErrorState v-if="loadError" :error="loadError" @retry="load" />
      <div v-else-if="loading || !preview" class="space-y-2">
        <Skeleton v-for="i in 2" :key="i" class="h-16 w-full" />
      </div>
      <EmptyState
        v-else-if="preview.subscriptions.length === 0"
        title="没有可用此卡的订阅"
        :description="`你的有效订阅中没有${card?.kind === 'weekly' ? '每周（7 天）' : card?.kind === '5h' ? '5 小时' : '5 小时或每周'}额度${card && card.plans.length ? '，或套餐不在此卡的适用范围内' : ''}。`"
        class="rounded-lg border"
      />
      <div v-else class="space-y-4">
        <fieldset class="space-y-2">
          <legend class="mb-2 text-sm font-medium">
            选择订阅
          </legend>
          <label
            v-for="s in preview.subscriptions"
            :key="s.id"
            class="hover:bg-muted/50 has-checked:border-primary has-checked:bg-primary/5 flex cursor-pointer items-start gap-2.5 rounded-lg border p-3 text-sm"
            data-testid="use-card-sub"
          >
            <input v-model="selected" type="radio" name="reset-card-sub" class="accent-primary mt-0.5" :value="s.id">
            <span class="min-w-0 flex-1">
              <span class="block truncate font-medium">{{ s.plan.name }}</span>
              <span class="text-muted-foreground block text-xs">有效期至 {{ formatDateTime(s.endsAt) }} · 可重置 {{ s.rules.length }} 项额度</span>
            </span>
          </label>
        </fieldset>

        <div v-if="target" class="space-y-2 rounded-lg border p-3" data-testid="use-card-changes">
          <p class="text-muted-foreground text-xs">
            使用后
          </p>
          <ul class="space-y-2">
            <li v-for="c in changes" :key="c.id" class="text-sm">
              <p class="font-medium">
                {{ c.title }}
              </p>
              <p class="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 tabular-nums">
                <span class="text-muted-foreground">{{ c.before }}</span>
                <ArrowRight class="text-muted-foreground size-3.5 shrink-0" aria-label="变为" />
                <span class="text-emerald-700 dark:text-emerald-400">{{ c.after }}</span>
              </p>
            </li>
          </ul>
        </div>
        <p v-if="target && !target.hasUsage" class="flex gap-1.5 text-xs text-amber-700 dark:text-amber-400" role="status" data-testid="use-card-idle">
          <TriangleAlert class="mt-px size-3.5 shrink-0" />
          <span>这些额度当前没有用量：使用后不会多出可用额度，只会让窗口从现在起重新计时。建议在额度快用完时再使用。</span>
        </p>
      </div>

      <DialogFooter class="items-center">
        <p v-if="useError" class="text-destructive mr-auto text-xs" role="alert">
          {{ useError }}
        </p>
        <Button variant="outline" :disabled="saving" @click="open = false">
          取消
        </Button>
        <Button :disabled="!target || saving" data-testid="use-card-submit" @click="submit">
          <Loader2 v-if="saving" class="animate-spin" />
          确认使用
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
