<script setup lang="ts">
import type { RedeemBatchCreated } from '@/lib/types'
import { computed } from 'vue'
import { Download, TriangleAlert } from '@lucide/vue'
import CopyButton from '@/components/CopyButton.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useCurrency } from '@/composables/useCurrency'
import { downloadText, toCsv } from '@/lib/csv'

/** Shows freshly generated redeem codes once. The parent drops them on close. */
const props = defineProps<{ result: RedeemBatchCreated }>()
const open = defineModel<boolean>('open', { required: true })
const { money } = useCurrency()

const allText = computed(() => props.result.codes.join('\n'))

function downloadCsv() {
  const b = props.result.batch
  const isPlan = b.kind === 'plan'
  const rows: Array<Array<string | number | null>> = [isPlan
    ? ['code', 'plan', 'periods', 'batch_id', 'valid_from', 'expires_at', 'note']
    : ['code', 'amount', 'batch_id', 'valid_from', 'expires_at', 'note']]
  for (const c of props.result.codes)
    rows.push(isPlan ? [c, b.planName, b.periods, b.id, b.validFrom, b.expiresAt, b.note] : [c, b.amount, b.id, b.validFrom, b.expiresAt, b.note])
  const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')
  downloadText(`omnigate-redeem-codes-${stamp}.csv`, toCsv(rows))
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90svh] grid-rows-[auto_1fr_auto] sm:max-w-lg" @interact-outside="(e: Event) => e.preventDefault()">
      <DialogHeader>
        <DialogTitle>已生成 {{ result.codes.length }} 个兑换码</DialogTitle>
        <DialogDescription>
          <template v-if="result.batch.kind === 'plan'">
            每个兑换码开通套餐「{{ result.batch.planName ?? '—' }}」× {{ result.batch.periods ?? '?' }} 份。
          </template>
          <template v-else>
            每个面额 {{ money(result.batch.amount) }}。
          </template>
        </DialogDescription>
      </DialogHeader>
      <div class="min-h-0 space-y-3 overflow-y-auto">
        <div class="flex gap-2 rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 text-xs">
          <TriangleAlert class="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" />
          <p>兑换码明文<strong>只显示这一次</strong>，服务器仅保存摘要。关闭前请复制或下载 CSV，并通过安全渠道分发。</p>
        </div>
        <pre class="bg-muted/50 max-h-72 overflow-y-auto rounded-lg border p-3 font-mono text-xs leading-relaxed select-all" data-testid="codes-list">{{ allText }}</pre>
      </div>
      <DialogFooter class="gap-2">
        <CopyButton :value="allText" label="复制全部" show-label variant="outline" size="default" />
        <Button variant="outline" @click="downloadCsv">
          <Download />
          下载 CSV
        </Button>
        <Button @click="open = false">
          我已保存
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
