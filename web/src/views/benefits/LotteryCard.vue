<script setup lang="ts">
import type { DrawRequest, DrawResult, LotteryStatus } from '@/lib/lottery'
import { onMounted, ref } from 'vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, isApiError } from '@/lib/api'
import { lotteryApi } from '@/lib/lottery'
import { useAuthStore } from '@/stores/auth'

const { money } = useCurrency()
const status = ref<LotteryStatus>()
const result = ref<DrawResult>()
const busy = ref(false)
const error = ref('')
const pending = ref<DrawRequest>()
const storageKey = `lottery-draw:${useAuthStore().user?.id}`

async function load() {
  try {
    error.value = ''
    status.value = await lotteryApi.status()
  }
  catch (err) { error.value = errorMessage(err) }
}

onMounted(() => {
  const saved = sessionStorage.getItem(storageKey)
  if (saved) {
    try { pending.value = JSON.parse(saved) }
    catch { sessionStorage.removeItem(storageKey) }
  }
  void load()
})

async function draw(count: number) {
  if (busy.value)
    return
  busy.value = true
  error.value = ''
  try {
    const request = pending.value ?? { count, requestId: crypto.randomUUID() }
    pending.value = request
    sessionStorage.setItem(storageKey, JSON.stringify(request))
    result.value = await lotteryApi.draw(request)
    pending.value = undefined
    sessionStorage.removeItem(storageKey)
    await load()
  }
  catch (err) {
    if (isApiError(err) && err.status === 422) {
      pending.value = undefined
      sessionStorage.removeItem(storageKey)
      await load()
    }
    error.value = errorMessage(err)
  }
  finally { busy.value = false }
}
</script>

<template>
  <Card>
    <CardHeader>
      <CardTitle>每日消费抽奖</CardTitle>
      <CardDescription v-if="status">
        每个香港自然日，模型消费超过 {{ money(status.threshold) }} 即可获得 1 次机会。钱包和套餐用量一起计算，次数可以累积。
      </CardDescription>
    </CardHeader>
    <CardContent class="space-y-3">
      <template v-if="status">
        <p>可抽 <strong>{{ status.available }}</strong> 次 · 今日模型消费 {{ money(status.todaySpent) }} · {{ status.todayEarned ? '今日次数已获得' : '今日待达标' }}</p>
        <div class="flex flex-wrap gap-2">
          <Button v-if="pending" :disabled="busy" @click="draw(pending.count)">
            查询或重试本次 {{ pending.count }} 抽
          </Button>
          <template v-else>
            <Button v-for="count in [1, 5, 10, 20]" :key="count" :disabled="busy || status.available < count" @click="draw(count)">
              {{ count === 1 ? '单抽' : count + ' 连抽' }}
            </Button>
          </template>
        </div>
        <details class="text-sm">
          <summary class="cursor-pointer">
            奖项与概率
          </summary>
          <ul class="mt-2 grid grid-cols-2 gap-1">
            <li v-for="(prize, index) in status.prizes" :key="index">
              {{ money(prize.amount) }} · {{ prize.percent }}%
            </li>
          </ul>
        </details>
      </template>
      <div v-if="result" role="status" class="rounded-md border p-3">
        <strong>本次 {{ result.rewards.length }} 抽，{{ money(result.total) }} 已到账</strong>
        <RouterLink to="/console/billing" class="ml-2 text-sm underline">查看钱包流水</RouterLink>
        <p v-if="result.rewards.length > 1" class="text-muted-foreground text-sm">
          {{ result.rewards.map(v => money(v)).join('、') }}
        </p>
      </div>
      <div v-if="error" role="alert" class="text-destructive text-sm">
        {{ error }} <Button variant="link" @click="load">
          刷新
        </Button>
      </div>
    </CardContent>
  </Card>
</template>
