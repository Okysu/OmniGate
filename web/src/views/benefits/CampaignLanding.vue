<script setup lang="ts">
import type { PublicCampaign } from '@/lib/benefits'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { toast } from 'vue-sonner'
import { ArrowLeft, Check, Clock3, Gift, LoaderCircle, Package, Share2, Wallet } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { benefitsApi, campaignState } from '@/lib/benefits'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage } from '@/lib/api'
import { formatDuration, ruleSummary } from '@/lib/quota'
import { useAuthStore } from '@/stores/auth'
const { money } = useCurrency()
const route = useRoute()
const auth = useAuthStore()
const campaign = ref<PublicCampaign | null>(null)
const error = ref('')
const claiming = ref(false)
const count = ref(0)
const key = ref(crypto.randomUUID())
const now = ref(Date.now())
const timer = setInterval(() => { now.value = Date.now() }, 10000)
onBeforeUnmount(() => clearInterval(timer))
const state = computed(() => campaign.value ? campaignState(campaign.value, now.value) : '')
async function load() {
  error.value = ''
  try {
    campaign.value = await benefitsApi.get(String(route.params.id))
    if (auth.isAuthenticated) count.value = (await benefitsApi.claims()).counts[campaign.value.id] ?? 0
  }
  catch (err) { error.value = errorMessage(err) }
}
watch(() => route.params.id, () => { campaign.value = null; count.value = 0; key.value = crypto.randomUUID(); void load() }, { immediate: true })
async function claim() {
  if (!campaign.value || claiming.value) return
  claiming.value = true
  error.value = ''
  try {
    await benefitsApi.claim(campaign.value.id, key.value, campaign.value.price)
    key.value = crypto.randomUUID()
    toast.success(campaign.value.rewardType === 'balance' ? '余额已到账' : '套餐已开通')
    await load()
  }
  catch (err) { error.value = errorMessage(err) }
  finally { claiming.value = false }
}
async function share() {
  try { await navigator.clipboard.writeText(location.href); toast.success('活动链接已复制') }
  catch { toast.error('复制失败，请复制浏览器地址栏链接') }
}
</script>

<template>
  <main class="mx-auto min-h-screen max-w-5xl px-5 py-6 sm:px-8 sm:py-10">
    <nav aria-label="返回导航" class="mb-10 flex items-center justify-between gap-4 sm:mb-14">
      <Button as-child variant="outline" size="sm" class="group rounded-full px-4">
        <RouterLink to="/console/benefits">
          <ArrowLeft class="size-4 transition-transform group-hover:-translate-x-0.5 motion-reduce:transition-none" />
          返回福利中心
        </RouterLink>
      </Button>
      <span class="text-muted-foreground text-xs tracking-widest">福利活动</span>
    </nav>
    <Alert v-if="error" variant="destructive" class="mb-6">
      <AlertDescription>{{ error }}</AlertDescription>
    </Alert>
    <template v-if="campaign">
      <header class="mb-8 flex items-start justify-between gap-6 sm:mb-10">
        <div class="min-w-0 space-y-4">
          <div class="flex flex-wrap items-center gap-3">
            <Badge :variant="state === '进行中' ? 'default' : 'secondary'">
              {{ state }}
            </Badge>
            <span class="text-muted-foreground text-sm">每人可领取 {{ campaign.perUserLimit }} 次</span>
          </div>
          <h1 class="text-3xl font-semibold tracking-tight break-words sm:text-4xl">
            {{ campaign.title }}
          </h1>
          <p v-if="campaign.description" class="text-muted-foreground max-w-2xl whitespace-pre-wrap leading-relaxed">
            {{ campaign.description }}
          </p>
        </div>
        <div class="bg-muted hidden size-16 shrink-0 items-center justify-center rounded-2xl sm:flex" aria-hidden="true">
          <Gift class="size-7" />
        </div>
      </header>
      <div class="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <Card class="gap-8">
          <CardHeader class="gap-4">
            <div class="text-muted-foreground flex items-center gap-2 text-sm">
              <component :is="campaign.rewardType === 'balance' ? Wallet : Package" class="size-4" aria-hidden="true" />
              {{ campaign.rewardType === 'balance' ? '余额奖励' : '限时套餐' }}
            </div>
            <CardTitle class="break-words" :class="campaign.rewardType === 'balance' ? 'text-4xl font-semibold tabular-nums tracking-tight sm:text-5xl' : 'text-2xl leading-tight'">
              {{ campaign.rewardType === 'balance' ? money(campaign.amount) : campaign.plan?.name }}
            </CardTitle>
            <CardDescription>
              {{ campaign.rewardType === 'balance' ? '领取后直接存入你的账户余额。' : `套餐有效期 ${formatDuration(campaign.plan?.duration ?? '')}，从领取时开始计算。` }}
            </CardDescription>
          </CardHeader>
          <CardContent class="space-y-6">
            <div v-if="campaign.plan" class="bg-muted/50 space-y-3 rounded-lg p-4">
              <ul class="space-y-2 text-sm">
                <li v-for="rule in campaign.plan.rules" :key="rule.id" class="flex items-start gap-2">
                  <Check class="text-muted-foreground mt-0.5 size-4 shrink-0" aria-hidden="true" />
                  {{ ruleSummary(rule, money) }}
                </li>
              </ul>
              <p class="text-muted-foreground break-words text-xs leading-relaxed">
                支持模型：{{ campaign.plan.models.join('、') || '全部模型' }}
              </p>
            </div>
            <section class="space-y-3 border-t pt-6" aria-labelledby="requirements-title">
              <h2 id="requirements-title" class="font-medium">
                领取条件
              </h2>
              <ul v-if="campaign.requirements.length" class="space-y-3 text-sm">
                <li v-for="item in campaign.requirements" :key="item" class="flex items-start gap-2.5">
                  <Check class="text-muted-foreground mt-0.5 size-4 shrink-0" aria-hidden="true" />
                  <span class="leading-relaxed">{{ item }}</span>
                </li>
              </ul>
              <p v-else class="text-muted-foreground text-sm">
                登录后即可领取。
              </p>
            </section>
            <dl v-if="campaign.stock >= 0 || campaign.startsAt || campaign.endsAt" class="text-muted-foreground space-y-3 border-t pt-6 text-sm">
              <div v-if="campaign.stock >= 0" class="flex flex-wrap items-center justify-between gap-2">
                <dt class="flex items-center gap-2">
                  <Package class="size-4" aria-hidden="true" />剩余份数
                </dt>
                <dd class="text-foreground font-medium tabular-nums">
                  {{ campaign.stock }} 份
                </dd>
              </div>
              <div v-if="campaign.startsAt" class="flex flex-wrap items-center justify-between gap-2">
                <dt class="flex items-center gap-2">
                  <Clock3 class="size-4" aria-hidden="true" />开始时间
                </dt>
                <dd class="text-foreground">
                  {{ new Date(campaign.startsAt).toLocaleString('zh-CN') }}
                </dd>
              </div>
              <div v-if="campaign.endsAt" class="flex flex-wrap items-center justify-between gap-2">
                <dt class="flex items-center gap-2">
                  <Clock3 class="size-4" aria-hidden="true" />截止时间
                </dt>
                <dd class="text-foreground">
                  {{ new Date(campaign.endsAt).toLocaleString('zh-CN') }}
                </dd>
              </div>
            </dl>
          </CardContent>
        </Card>
        <Card class="bg-muted/30">
          <CardHeader class="gap-2">
            <CardDescription>参与活动</CardDescription>
            <CardTitle class="text-2xl font-semibold">
              {{ Number(campaign.price) > 0 ? money(campaign.price) : '免费领取' }}
            </CardTitle>
            <CardDescription v-if="Number(campaign.price) > 0">
              从钱包余额支付
            </CardDescription>
          </CardHeader>
          <CardContent class="space-y-5">
            <p v-if="auth.isAuthenticated" class="text-muted-foreground flex items-center justify-between text-sm">
              <span>已领取次数</span>
              <span class="text-foreground font-medium tabular-nums">{{ count }} / {{ campaign.perUserLimit }}</span>
            </p>
            <div class="grid gap-3">
              <Button v-if="auth.isAuthenticated" size="lg" :disabled="claiming || state !== '进行中' || count >= campaign.perUserLimit" @click="claim">
                <LoaderCircle v-if="claiming" class="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
                {{ claiming ? '正在发放…' : count >= campaign.perUserLimit ? '已达到领取上限' : state !== '进行中' ? state : Number(campaign.price) > 0 ? '支付并领取' : '立即领取' }}
              </Button>
              <Button v-else as-child size="lg">
                <RouterLink :to="{ path: '/login', query: { redirect: route.fullPath } }">
                  登录后参与
                </RouterLink>
              </Button>
              <Button variant="outline" class="w-full" @click="share">
                <Share2 class="size-4" />分享活动
              </Button>
            </div>
          </CardContent>
        </Card>
      </div>
    </template>
    <p v-else-if="!error" class="text-muted-foreground flex items-center gap-2 py-16 text-sm" role="status">
      <LoaderCircle class="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
      正在加载活动…
    </p>
  </main>
</template>
