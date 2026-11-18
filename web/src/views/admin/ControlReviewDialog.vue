<script setup lang="ts">
import type { ControlEvent, ControlReview } from '@/lib/control'
import { computed, ref, watch } from 'vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { errorMessage } from '@/lib/api'
import { controlApi, controlPercent } from '@/lib/control'
import { useAuthStore } from '@/stores/auth'

interface ReviewRow {
  event: ControlEvent
  review?: ControlReview
  error?: string
  applied?: boolean
}

const props = defineProps<{ events: ControlEvent[] }>()
const emit = defineEmits<{ applied: [id: string, decision: Pick<ControlEvent, 'reviewProbability' | 'reviewThreshold' | 'effectiveHit' | 'reviewedAt'>] }>()
const open = defineModel<boolean>('open', { required: true })
const auth = useAuthStore()
const models = ref<string[]>([])
const model = ref('')
const thresholdInput = ref<number | string>('')
const threshold = computed(() => thresholdInput.value === '' ? undefined : Number(thresholdInput.value) / 100)
const thresholdInvalid = computed(() => threshold.value !== undefined && (!Number.isFinite(threshold.value) || threshold.value < 0 || threshold.value > 1))
const rows = ref<ReviewRow[]>([])
const busy = ref(false)
const loading = ref(false)
const error = ref('')
const applicable = computed(() => rows.value.filter(row => row.review?.applyToken && !row.applied))
let loadVersion = 0

function displayedThreshold(row: ReviewRow, review: ControlReview) {
  return row.applied || thresholdInvalid.value ? review.threshold : threshold.value ?? review.threshold
}

function displayedDecision(row: ReviewRow, review: ControlReview) {
  if (review.probability === null) return '待人工判断'
  return review.probability >= displayedThreshold(row, review) ? '命中' : '未命中'
}

watch(open, async (value) => {
  const version = ++loadVersion
  if (!value) return
  model.value = ''
  thresholdInput.value = ''
  models.value = []
  error.value = ''
  rows.value = []
  loading.value = true
  try {
    const found = await controlApi.reviewModels()
    if (version === loadVersion) models.value = found.models
  }
  catch (err) {
    if (version === loadVersion) error.value = errorMessage(err)
  }
  finally {
    if (version === loadVersion) loading.value = false
  }
})

async function run() {
  const chosen = model.value
  if (!chosen || busy.value || rows.value.length) return
  busy.value = true
  rows.value = props.events.map(event => ({ event }))
  try {
    for (const row of rows.value) {
      try {
        row.review = await controlApi.review(row.event.id, chosen)
      }
      catch (err) {
        row.error = errorMessage(err)
      }
    }
  }
  finally {
    busy.value = false
  }
}

async function apply() {
  if (busy.value || thresholdInvalid.value || !auth.can('users.write')) return
  busy.value = true
  try {
    for (const row of applicable.value) {
      const review = row.review
      if (!review) continue
      try {
        const decision = await controlApi.applyReview(row.event.id, review.applyToken, threshold.value)
        review.threshold = decision.reviewThreshold ?? review.threshold
        review.effectiveHit = decision.effectiveHit
        row.applied = true
        row.error = undefined
        emit('applied', row.event.id, decision)
      }
      catch (err) {
        row.error = errorMessage(err)
      }
    }
  }
  finally {
    busy.value = false
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="value => { if (!busy) open = value }">
    <DialogContent class="max-h-[85svh] overflow-y-auto sm:max-w-2xl">
      <DialogHeader>
        <DialogTitle>{{ events.length === 1 ? '模型复核' : `批量复核（${events.length} 条）` }}</DialogTitle>
        <DialogDescription>选择平台模型。应用复核后，系统按复核分数和选定阈值更新有效判定。</DialogDescription>
      </DialogHeader>
      <Select v-model="model" :disabled="loading || busy || rows.length > 0">
        <SelectTrigger class="w-full" aria-label="复核模型">
          <SelectValue :placeholder="loading ? '读取平台模型…' : '选择复核模型'" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem v-for="item in models" :key="item" :value="item">
            {{ item }}
          </SelectItem>
        </SelectContent>
      </Select>
      <p v-if="error || (!loading && !models.length)" class="text-destructive text-sm" role="alert">
        {{ error || '没有可用的平台模型' }}
      </p>
      <div v-if="applicable.length && auth.can('users.write')" class="space-y-2">
        <Label for="control-review-threshold">复核阈值（%）</Label>
        <Input id="control-review-threshold" v-model.number="thresholdInput" type="number" min="0" max="100" step="any" placeholder="留空沿用各条记录当前的阈值" :disabled="busy" :aria-invalid="thresholdInvalid" />
        <p v-if="thresholdInvalid" class="text-destructive text-sm" role="alert">
          请输入 0 到 100 之间的百分比。
        </p>
      </div>
      <div v-for="row in rows" :key="row.event.id" class="space-y-2 rounded-md border p-3 text-sm">
        <p class="font-medium">
          {{ row.event.userName || row.event.userId }} · {{ row.event.reason }}
        </p>
        <p class="text-muted-foreground break-all font-mono text-xs">
          {{ row.event.requestId }}
        </p>
        <template v-if="row.review">
          <p>
            复核 {{ controlPercent(row.review.probability) }} · 阈值 {{ controlPercent(displayedThreshold(row, row.review)) }} ·
            {{ displayedDecision(row, row.review) }}{{ row.applied ? ' · 已应用' : '' }}
          </p>
          <p v-if="row.review.sourceRequestId !== row.event.requestId" class="text-muted-foreground break-all text-xs">
            参考快照来自请求 {{ row.review.sourceRequestId }}
          </p>
          <p class="whitespace-pre-wrap break-words">
            {{ row.review.result }}
          </p>
        </template>
        <p v-if="row.error" class="text-destructive" role="alert">
          {{ row.error }}
        </p>
        <p v-else-if="!row.review" class="text-muted-foreground">
          等待复核结果…
        </p>
      </div>
      <DialogFooter>
        <Button variant="outline" :disabled="busy" @click="open = false">
          关闭
        </Button>
        <Button v-if="!rows.length" :disabled="!model || busy" @click="run">
          开始复核
        </Button>
        <Button v-else-if="auth.can('users.write') && applicable.length" :disabled="busy || thresholdInvalid" @click="apply">
          应用复核（{{ applicable.length }} 条）
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
