<script setup lang="ts">
import type { PreviewInbound, RoutePreview, RouteStrategy, User } from '@/lib/types'
import { computed, ref } from 'vue'
import { CircleSlash, Loader2, Play, Route as RouteIcon, UserRound, X } from '@lucide/vue'
import SuggestInput from '@/components/SuggestInput.vue'
import UserPicker from '@/components/UserPicker.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage } from '@/lib/api'
import { routesApi } from '@/lib/endpoints'
import { formatMs } from '@/lib/format'
import { CHANNEL_TYPE_LABELS, INBOUND_LABELS } from '@/lib/labels'
import { TIER_LABELS } from '@/lib/plaza'
import { BREAKER_LABELS, HOP_LABELS, PREVIEW_INBOUNDS, STRATEGY_LABELS } from '@/lib/routeForm'

defineProps<{ models: string[] }>()
const emit = defineEmits<{ openRule: [id: string] }>()

const { money } = useCurrency()

const model = ref('')
const inbound = ref<PreviewInbound>('openai.chat')
const userPick = ref<string[]>([])
const userLabel = ref<string | null>(null)
const userPopover = ref(false)
function onUserPicked(u: User | null) {
  userLabel.value = u?.displayName ?? null
  userPopover.value = false
}
function clearUser() {
  userPick.value = []
  userLabel.value = null
}

const loading = ref(false)
const error = ref<string | null>(null)
const result = ref<RoutePreview | null>(null)
/** Inputs of the shown result (the form may have changed since). */
const shownFor = ref<{ model: string, inbound: PreviewInbound } | null>(null)

async function run() {
  const m = model.value.trim()
  if (!m) {
    error.value = '请输入模型名'
    return
  }
  loading.value = true
  error.value = null
  try {
    result.value = await routesApi.preview({ model: m, inbound: inbound.value, userId: userPick.value[0] || undefined })
    shownFor.value = { model: m, inbound: inbound.value }
  }
  catch (err) {
    error.value = errorMessage(err)
    result.value = null
  }
  finally {
    loading.value = false
  }
}

function setInbound(v: unknown) {
  if (typeof v === 'string' && (PREVIEW_INBOUNDS as readonly string[]).includes(v))
    inbound.value = v as PreviewInbound
}

const strategyLabel = computed(() => {
  const s = result.value?.strategy
  return s ? (STRATEGY_LABELS[s as RouteStrategy] ?? s) : ''
})
const tried = computed(() => result.value?.candidates.filter(c => !c.skipped).length ?? 0)

const HOP_CLASSES: Record<number, string> = {
  0: 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400',
  1: 'border-amber-500/40 text-amber-700 dark:text-amber-400',
  2: 'border-orange-500/50 text-orange-700 dark:text-orange-400',
}
/** The tier column appears once the backend reports tiers (older backends omit them). */
const hasTiers = computed(() => !!result.value?.candidates.some(c => c.tier))
const TIER_CLASSES: Record<string, string> = {
  own: 'border-emerald-500/50 text-emerald-700 dark:text-emerald-400',
  shared: 'border-sky-500/50 text-sky-700 dark:text-sky-400',
  platform: 'text-muted-foreground',
}
function tierLabel(t: string | undefined): string {
  return t ? (TIER_LABELS[t as keyof typeof TIER_LABELS] ?? t) : '—'
}
const tierOrderText = computed(() => (result.value?.tierOrder?.length ? result.value.tierOrder.map(tierLabel).join(' → ') : ''))

const BREAKER_DOT: Record<string, string> = {
  closed: 'bg-emerald-500',
  half_open: 'bg-amber-500',
  open: 'bg-red-500',
}
</script>

<template>
  <Card>
    <CardHeader>
      <CardTitle class="flex items-center gap-2 text-base">
        <RouteIcon class="size-4" />
        命中预览
      </CardTitle>
      <CardDescription>
        模拟一次请求，查看命中的规则以及网关将按什么顺序尝试哪些渠道（基于已保存的规则；随机策略只显示一种示例顺序）。
      </CardDescription>
    </CardHeader>
    <CardContent class="space-y-4">
      <form class="grid gap-3 sm:grid-cols-2 lg:grid-cols-[minmax(0,1fr)_14rem_14rem_auto] lg:items-end" @submit.prevent="run">
        <div class="space-y-1.5">
          <Label for="preview-model">模型</Label>
          <SuggestInput id="preview-model" v-model="model" :options="models" placeholder="例如 gpt-4o" mono />
        </div>
        <div class="space-y-1.5">
          <Label for="preview-user">用户</Label>
          <div class="flex gap-1">
            <Popover v-model:open="userPopover">
              <PopoverTrigger as-child>
                <Button id="preview-user" type="button" variant="outline" class="min-w-0 flex-1 justify-start font-normal">
                  <UserRound />
                  <span class="truncate">{{ userPick[0] ? (userLabel ?? `${userPick[0].slice(0, 8)}…`) : '当前用户（我）' }}</span>
                </Button>
              </PopoverTrigger>
              <PopoverContent align="start" class="w-80 gap-2">
                <Label>以哪个用户的身份预览</Label>
                <p class="text-muted-foreground text-xs">
                  用户的角色决定命中哪条规则，可用渠道决定候选范围。
                </p>
                <UserPicker v-model="userPick" :multiple="false" @pick="onUserPicked" />
              </PopoverContent>
            </Popover>
            <Button v-if="userPick.length" type="button" variant="ghost" size="icon" aria-label="改回当前用户" @click="clearUser">
              <X />
            </Button>
          </div>
        </div>
        <div class="space-y-1.5">
          <Label for="preview-inbound">客户端协议</Label>
          <Select :model-value="inbound" @update:model-value="setInbound">
            <SelectTrigger id="preview-inbound" class="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="p in PREVIEW_INBOUNDS" :key="p" :value="p">
                {{ INBOUND_LABELS[p] ?? p }}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
        <Button type="submit" :disabled="loading" class="sm:col-span-2 lg:col-span-1">
          <Loader2 v-if="loading" class="animate-spin" />
          <Play v-else />
          预览
        </Button>
      </form>

      <p v-if="error" class="text-destructive text-sm" role="alert">
        {{ error }}
      </p>

      <div v-if="result" class="space-y-3" data-testid="preview-result">
        <div class="bg-muted/40 flex flex-wrap items-center gap-x-4 gap-y-2 rounded-lg border p-3 text-sm">
          <span class="flex min-w-0 items-center gap-2">
            <span class="text-muted-foreground">命中规则</span>
            <button v-if="result.rule" type="button" class="text-primary truncate font-medium hover:underline" @click="emit('openRule', result.rule.id)">
              {{ result.rule.name }}
            </button>
            <span v-else class="font-medium">未命中规则，使用默认路由</span>
          </span>
          <span class="flex items-center gap-2">
            <span class="text-muted-foreground">策略</span>
            <Badge variant="secondary">{{ strategyLabel }}</Badge>
          </span>
          <span class="flex items-center gap-2">
            <span class="text-muted-foreground">最多尝试</span>
            <span class="tabular-nums">{{ result.maxAttempts }} 次</span>
          </span>
          <span v-if="shownFor" class="text-muted-foreground ml-auto text-xs">
            {{ shownFor.model }} · {{ INBOUND_LABELS[shownFor.inbound] ?? shownFor.inbound }}
          </span>
        </div>

        <div v-if="result.candidates.length" class="overflow-x-auto rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead class="w-8">
                  #
                </TableHead>
                <TableHead>渠道</TableHead>
                <TableHead v-if="hasTiers">
                  层级
                </TableHead>
                <TableHead>类型</TableHead>
                <TableHead class="text-right">
                  优先级
                </TableHead>
                <TableHead class="text-right">
                  权重
                </TableHead>
                <TableHead>上游协议</TableHead>
                <TableHead>熔断</TableHead>
                <TableHead class="text-right">
                  延迟
                </TableHead>
                <TableHead class="text-right" title="该渠道该模型的成本价：每百万 tokens 输入 + 输出单价之和">
                  成本 / M
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <template v-for="(c, i) in result.candidates" :key="c.channelId">
                <TableRow :class="c.skipped ? 'text-muted-foreground opacity-60' : ''" :data-skipped="c.skipped ? 'true' : undefined">
                  <TableCell class="tabular-nums">
                    <CircleSlash v-if="c.skipped" class="size-3.5" aria-label="跳过" />
                    <span v-else>{{ result.candidates.slice(0, i + 1).filter(x => !x.skipped).length }}</span>
                  </TableCell>
                  <TableCell class="max-w-56">
                    <RouterLink :to="`/console/channels/${c.channelId}`" class="block truncate font-medium hover:underline" :title="c.channelName">
                      {{ c.channelName }}
                    </RouterLink>
                    <p v-if="c.skipped" class="truncate text-xs" :title="c.skipped">
                      跳过：{{ c.skipped }}
                    </p>
                  </TableCell>
                  <TableCell v-if="hasTiers" class="whitespace-nowrap">
                    <Badge v-if="c.tier" variant="outline" class="h-4 px-1.5 text-[10px]" :class="TIER_CLASSES[c.tier] ?? ''" data-testid="preview-tier">
                      {{ tierLabel(c.tier) }}
                    </Badge>
                    <span v-else class="text-muted-foreground">—</span>
                  </TableCell>
                  <TableCell class="whitespace-nowrap">
                    {{ CHANNEL_TYPE_LABELS[c.channelType as 'openai'] ?? c.channelType }}
                  </TableCell>
                  <TableCell class="text-right tabular-nums">
                    {{ c.priority }}
                  </TableCell>
                  <TableCell class="text-right tabular-nums">
                    {{ c.weight }}
                  </TableCell>
                  <TableCell>
                    <div class="flex items-center gap-1.5 whitespace-nowrap">
                      <span class="text-xs">{{ INBOUND_LABELS[c.upstreamDialect] ?? c.upstreamDialect }}</span>
                      <Badge variant="outline" class="h-4 px-1 text-[10px]" :class="HOP_CLASSES[c.conversionHops] ?? ''">
                        {{ HOP_LABELS[c.conversionHops] ?? `${c.conversionHops} 次转换` }}
                      </Badge>
                    </div>
                  </TableCell>
                  <TableCell class="whitespace-nowrap">
                    <span class="inline-flex items-center gap-1.5 text-xs">
                      <span class="inline-block size-1.5 rounded-full" :class="BREAKER_DOT[c.breaker] ?? 'bg-muted-foreground'" />
                      {{ BREAKER_LABELS[c.breaker] ?? c.breaker }}
                    </span>
                  </TableCell>
                  <TableCell class="text-right whitespace-nowrap tabular-nums">
                    {{ c.latencyMs === null ? '—' : formatMs(c.latencyMs) }}
                  </TableCell>
                  <TableCell class="text-right whitespace-nowrap tabular-nums" :title="c.costPerM === null ? '没有成本价' : undefined">
                    {{ c.costPerM === null ? '—' : money(c.costPerM) }}
                  </TableCell>
                </TableRow>
              </template>
            </TableBody>
          </Table>
        </div>
        <p v-else class="text-muted-foreground rounded-lg border border-dashed p-4 text-center text-sm">
          没有候选渠道：该用户没有可用于此模型的渠道，请求会失败（503）。
        </p>
        <p v-if="result.candidates.length" class="text-muted-foreground text-xs">
          共 {{ result.candidates.length }} 个候选，{{ tried }} 个会被尝试；单个模型最多尝试 {{ result.maxAttempts }} 次。
        </p>
        <p v-if="hasTiers || tierOrderText" class="text-muted-foreground text-xs" data-testid="preview-tier-note">
          先尝试自有渠道，再共享渠道，最后平台渠道；路由规则只作用于平台渠道；自有/共享渠道不计费<template v-if="tierOrderText">
            （本次顺序：{{ tierOrderText }}）
          </template>。
        </p>

        <div v-if="result.fallbackModels.length" class="flex flex-wrap items-center gap-2 text-sm">
          <span class="text-muted-foreground">全部失败后依次回退到</span>
          <template v-for="(m, i) in result.fallbackModels" :key="m">
            <span v-if="i > 0" class="text-muted-foreground">→</span>
            <Badge variant="outline" class="font-mono">
              {{ m }}
            </Badge>
          </template>
        </div>
      </div>
    </CardContent>
  </Card>
</template>
