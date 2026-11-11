<script setup lang="ts">
import type { LotteryConfig } from '@/lib/lottery'
import { computed, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/api'
import { lotteryApi } from '@/lib/lottery'

const config = ref<LotteryConfig>()
const busy = ref(false)
const error = ref('')
const sum = computed(() => config.value?.prizes.reduce((n, p) => n + Number(p.percent), 0))
async function load() {
  try { config.value = await lotteryApi.config(); error.value = '' }
  catch (err) { error.value = errorMessage(err) }
}
onMounted(load)
async function save() {
  if (!config.value || busy.value)
    return
  busy.value = true
  try {
    config.value = await lotteryApi.save(config.value)
    error.value = ''
    toast.success('抽奖设置已保存')
  }
  catch (err) { error.value = errorMessage(err) }
  finally { busy.value = false }
}
</script>

<template>
  <Card>
    <CardHeader>
      <CardTitle>每日消费抽奖</CardTitle>
      <CardDescription>降低门槛时，历史消费达标的日期也会补发次数。每人每天最多获得一次。</CardDescription>
    </CardHeader>
    <CardContent class="space-y-3">
      <form v-if="config" class="max-w-xl space-y-4" @submit.prevent="save">
        <label class="block text-sm">每日消费门槛
          <Input v-model="config.threshold" required class="mt-1" inputmode="decimal" />
        </label>
        <p class="text-muted-foreground text-sm">
          填 0 表示当天有任意正额模型消费即可参与。单次奖金最高 3 余额。
        </p>
        <div v-for="(prize, index) in config.prizes" :key="index" class="flex items-end gap-2">
          <label class="text-sm">奖金<Input v-model="prize.amount" required inputmode="decimal" /></label>
          <label class="text-sm">概率（%）<Input :model-value="prize.percent" required type="number" min="1" max="100" step="1" @update:model-value="value => { prize.percent = Number(value) }" /></label>
          <Button type="button" variant="outline" @click="config.prizes.splice(index, 1)">
            移除
          </Button>
        </div>
        <div class="flex flex-wrap items-center gap-3">
          <Button type="button" variant="outline" @click="config.prizes.push({ amount: '1', percent: 1 })">
            添加奖项
          </Button>
          <span class="text-sm">概率合计 {{ sum }}%</span>
          <Button type="submit" :disabled="busy || sum !== 100">
            保存抽奖设置
          </Button>
        </div>
      </form>
      <div v-if="error" role="alert" class="text-destructive text-sm">
        {{ error }} <Button variant="link" @click="load">
          重新加载
        </Button>
      </div>
    </CardContent>
  </Card>
</template>
