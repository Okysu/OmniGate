<script setup lang="ts">
import type { MultiSelectOption } from '@/components/MultiSelect.vue'
import type { ChannelView, GatewayKey, KeyCreated, KeySpendUsage, QuotaOverflow, SpendLimit } from '@/lib/types'
import { computed, onMounted, ref } from 'vue'
import { KeyRound, MoreHorizontal, Pencil, Plus, RefreshCw, RotateCw, Trash2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import QuotaBar from '@/components/quota/QuotaBar.vue'
import SecretOnceDialog from '@/components/SecretOnceDialog.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, isVersionConflict } from '@/lib/api'
import { billingApi, fetchAllChannels, keysApi, modelsApi } from '@/lib/endpoints'
import { formatDateTime, formatRelative } from '@/lib/format'
import { CHANNEL_TYPE_LABELS } from '@/lib/labels'
import { amountRatio, levelOf, normalizeBillingLimits, normalizeSpendLimit, percentText, spendLimitText } from '@/lib/limits'
import { effectiveQuotaOverflow, isQuotaOverflow, KEY_QUOTA_OVERFLOW_LABELS, QUOTA_OVERFLOW_SHORT } from '@/lib/quota'
import { useAuthStore } from '@/stores/auth'
import KeyFormDialog from './KeyFormDialog.vue'
import UsageExamples from './UsageExamples.vue'

const auth = useAuthStore()

const items = ref<GatewayKey[]>([])
const loading = ref(false)
const loadError = ref<unknown>(null)

async function load() {
  loading.value = true
  loadError.value = null
  try {
    items.value = (await keysApi.list()).items
  }
  catch (err) {
    loadError.value = err
  }
  finally {
    loading.value = false
  }
}
onMounted(() => {
  void load()
  void ensureOptions()
  void loadAccountOverflow()
  void loadSpend()
})

// ---------- spend limits (phase8 §2.3, best effort) ----------
const { money } = useCurrency()
const spend = ref(new Map<string, KeySpendUsage>())
async function loadSpend() {
  try {
    const res = normalizeBillingLimits(await billingApi.limits())
    spend.value = new Map(res.keys.map(k => [k.id, k]))
  }
  catch {
    // Older backend (404) or no access: the column falls back to the policy only.
  }
}
function spendLimitOf(k: GatewayKey): SpendLimit | null {
  return normalizeSpendLimit(k.policy.spendLimit) ?? spend.value.get(k.id)?.spendLimit ?? null
}
function spendTitle(k: GatewayKey): string {
  const l = spendLimitOf(k)
  if (!l)
    return '不限（只统计平台渠道的计费金额）'
  const u = spend.value.get(k.id)
  const lines = [`上限：${spendLimitText(l, money)}`]
  if (u)
    lines.push(`已用：${money(u.spent)}`)
  if (u?.resetsAt)
    lines.push(`重置：${formatDateTime(u.resetsAt)}`)
  lines.push('只统计平台渠道的计费金额')
  return lines.join('\n')
}

// The account's 「套餐额度用完后」 setting, for keys that follow it (best effort).
const accountOverflow = ref<QuotaOverflow | null>(null)
async function loadAccountOverflow() {
  if (!auth.can('billing.own'))
    return
  try {
    const res = await billingApi.preferences()
    accountOverflow.value = isQuotaOverflow(res.quotaOverflow) ? res.quotaOverflow : null
  }
  catch {
    accountOverflow.value = null
  }
}

// Options for the policy pickers (loaded lazily when the dialog opens).
const modelOptions = ref<MultiSelectOption[]>([])
const channels = ref<ChannelView[]>([])
const optionsLoading = ref(false)
let optionsLoaded = false
async function ensureOptions() {
  if (optionsLoaded)
    return
  optionsLoaded = true
  optionsLoading.value = true
  try {
    const [m, c] = await Promise.allSettled([
      modelsApi.list(),
      auth.can('channels.read') ? fetchAllChannels() : Promise.resolve([] as ChannelView[]),
    ])
    if (m.status === 'fulfilled')
      modelOptions.value = m.value.items.map(e => ({ value: e.model, label: e.model, hint: `${e.channels} 个渠道` }))
    if (c.status === 'fulfilled')
      channels.value = c.value
    if (m.status === 'rejected' || c.status === 'rejected')
      optionsLoaded = false
  }
  finally {
    optionsLoading.value = false
  }
}
const channelOptions = computed<MultiSelectOption[]>(() =>
  channels.value.map(c => ({ value: c.id, label: c.name, hint: CHANNEL_TYPE_LABELS[c.type] ?? c.type })))
const channelName = computed(() => new Map(channels.value.map(c => [c.id, c.name])))

// ---------- create / edit ----------
const formOpen = ref(false)
const editing = ref<GatewayKey | null>(null)
function openCreate() {
  editing.value = null
  void ensureOptions()
  formOpen.value = true
}
function openEdit(k: GatewayKey) {
  editing.value = k
  void ensureOptions()
  formOpen.value = true
}

// Secret is held only while the dialog is open.
const secret = ref<{ title: string, value: string } | null>(null)
const secretOpen = ref(false)
function showSecret(title: string, res: KeyCreated) {
  secret.value = { title, value: res.secret }
  secretOpen.value = true
}
function onSecretOpenChange(v: boolean) {
  secretOpen.value = v
  if (!v)
    secret.value = null
}

function onCreated(res: KeyCreated) {
  items.value = [res.key, ...items.value]
  showSecret(`API Key「${res.key.name}」已创建`, res)
}
function onUpdated(k: GatewayKey) {
  items.value = items.value.map(i => (i.id === k.id ? k : i))
  toast.success('API Key 已保存')
  void loadSpend()
}

// ---------- status ----------
const toggling = ref<Set<string>>(new Set())
async function toggleStatus(k: GatewayKey, enabled: boolean) {
  toggling.value = new Set(toggling.value).add(k.id)
  try {
    const updated = await keysApi.update(k.id, { status: enabled ? 'enabled' : 'disabled', version: k.version })
    items.value = items.value.map(i => (i.id === k.id ? updated : i))
    toast.success(enabled ? `已启用「${k.name}」` : `已停用「${k.name}」`)
  }
  catch (err) {
    if (isVersionConflict(err)) {
      toast.warning('该 Key 已被修改，已刷新列表')
      await load()
    }
    else {
      toast.error('操作失败', { description: errorMessage(err) })
    }
  }
  finally {
    const next = new Set(toggling.value)
    next.delete(k.id)
    toggling.value = next
  }
}

// ---------- rotate / revoke ----------
type Pending = { kind: 'rotate' | 'revoke', key: GatewayKey }
const pending = ref<Pending | null>(null)
const confirmOpen = ref(false)
const busy = ref(false)
function ask(kind: Pending['kind'], key: GatewayKey) {
  pending.value = { kind, key }
  confirmOpen.value = true
}
async function confirmPending() {
  const p = pending.value
  if (!p)
    return
  busy.value = true
  try {
    if (p.kind === 'rotate') {
      const res = await keysApi.rotate(p.key.id)
      await load()
      confirmOpen.value = false
      showSecret(`API Key「${res.key.name}」已轮换`, res)
    }
    else {
      await keysApi.revoke(p.key.id)
      items.value = items.value.filter(i => i.id !== p.key.id)
      confirmOpen.value = false
      toast.success(`已吊销「${p.key.name}」`)
    }
  }
  catch (err) {
    confirmOpen.value = false
    toast.error(p.kind === 'rotate' ? '轮换失败' : '吊销失败', { description: errorMessage(err) })
    await load()
  }
  finally {
    busy.value = false
  }
}

function isExpired(k: GatewayKey): boolean {
  return !!k.expiresAt && new Date(k.expiresAt).getTime() <= Date.now()
}

function policyBadges(k: GatewayKey): string[] {
  const p = k.policy
  const out: string[] = []
  if (p.allowedModels.length)
    out.push(`模型 ${p.allowedModels.length}`)
  if (p.allowedChannels.length)
    out.push(`渠道 ${p.allowedChannels.length}`)
  if (p.ipAllowlist.length)
    out.push(`IP ${p.ipAllowlist.length}`)
  if (p.rpm)
    out.push(`${p.rpm} RPM`)
  if (p.compatMode === 'lenient')
    out.push('宽松兼容')
  const spendLimit = normalizeSpendLimit(p.spendLimit)
  if (spendLimit)
    out.push(`消费上限 ${spendLimitText(spendLimit, money)}`)
  if (p.quotaOverflow === 'wallet')
    out.push('超额用钱包')
  else if (p.quotaOverflow === 'block')
    out.push('超额阻断')
  return out
}

/** "跟随账户设置（阻断请求）" / "阻断" / "使用钱包". */
function overflowText(k: GatewayKey): string {
  const own = k.policy.quotaOverflow
  if (isQuotaOverflow(own))
    return KEY_QUOTA_OVERFLOW_LABELS[own]
  const eff = effectiveQuotaOverflow(own, accountOverflow.value)
  return eff ? `${KEY_QUOTA_OVERFLOW_LABELS['']}（${QUOTA_OVERFLOW_SHORT[eff]}）` : KEY_QUOTA_OVERFLOW_LABELS['']
}

function policyTitle(k: GatewayKey): string {
  const p = k.policy
  const lines = [
    `模型：${p.allowedModels.length ? p.allowedModels.join('、') : '不限'}`,
    `渠道：${p.allowedChannels.length ? p.allowedChannels.map(id => channelName.value.get(id) ?? id).join('、') : '不限'}`,
    `IP：${p.ipAllowlist.length ? p.ipAllowlist.join('、') : '不限'}`,
    `RPM：${p.rpm ?? '不限'}`,
    `兼容模式：${p.compatMode}`,
    `额度用完后：${overflowText(k)}`,
    `消费上限：${spendLimitOf(k) ? spendLimitText(spendLimitOf(k)!, money) : '不限'}`,
  ]
  return lines.join('\n')
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="API Keys" description="调用网关所用的密钥。完整密钥只在创建或轮换时显示一次。">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
          刷新
        </Button>
        <Button size="sm" @click="openCreate">
          <Plus />
          创建 Key
        </Button>
      </template>
    </PageHeader>

    <Card>
      <CardContent class="space-y-4">
        <ErrorState v-if="loadError" :error="loadError" @retry="load" />
        <div v-else-if="loading && items.length === 0" class="space-y-2">
          <Skeleton v-for="i in 3" :key="i" class="h-12 w-full" />
        </div>
        <EmptyState v-else-if="items.length === 0" :icon="KeyRound" title="还没有 API Key" description="创建一个 Key，然后按下方示例在 SDK 中使用。">
          <Button size="sm" @click="openCreate">
            <Plus />
            创建 Key
          </Button>
        </EmptyState>
        <div v-else class="overflow-x-auto" :class="loading ? 'opacity-60 transition-opacity' : ''">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead>前缀</TableHead>
                <TableHead>启用</TableHead>
                <TableHead class="hidden md:table-cell">
                  策略
                </TableHead>
                <TableHead class="hidden md:table-cell">
                  消费上限
                </TableHead>
                <TableHead class="hidden sm:table-cell">
                  过期时间
                </TableHead>
                <TableHead class="hidden lg:table-cell">
                  最近使用
                </TableHead>
                <TableHead class="hidden xl:table-cell">
                  创建时间
                </TableHead>
                <TableHead class="w-10">
                  <span class="sr-only">操作</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="k in items" :key="k.id">
                <TableCell class="font-medium">
                  {{ k.name }}
                </TableCell>
                <TableCell class="font-mono text-xs whitespace-nowrap">
                  {{ k.prefix }}
                </TableCell>
                <TableCell>
                  <div class="flex items-center gap-2">
                    <Switch
                      :model-value="k.status === 'enabled'"
                      :disabled="toggling.has(k.id)"
                      :aria-label="`${k.name} 是否启用`"
                      @update:model-value="(v: boolean) => toggleStatus(k, v)"
                    />
                    <Badge v-if="isExpired(k)" variant="destructive">
                      已过期
                    </Badge>
                  </div>
                </TableCell>
                <TableCell class="hidden md:table-cell">
                  <div class="flex flex-wrap gap-1" :title="policyTitle(k)">
                    <Badge v-for="b in policyBadges(k)" :key="b" variant="secondary">
                      {{ b }}
                    </Badge>
                    <span v-if="policyBadges(k).length === 0" class="text-muted-foreground text-xs">不限</span>
                  </div>
                </TableCell>
                <TableCell class="hidden md:table-cell" :title="spendTitle(k)" data-testid="key-spend">
                  <div v-if="spendLimitOf(k) && spend.get(k.id)" class="w-40 space-y-1">
                    <div class="flex items-baseline justify-between gap-2 text-xs tabular-nums">
                      <span class="truncate">{{ money(spend.get(k.id)!.spent) }} / {{ spendLimitText(spendLimitOf(k)!, money) }}</span>
                      <span class="text-muted-foreground shrink-0">{{ percentText(amountRatio(spend.get(k.id)!.spent, spendLimitOf(k)!.amount)) }}</span>
                    </div>
                    <QuotaBar
                      :ratio="amountRatio(spend.get(k.id)!.spent, spendLimitOf(k)!.amount)"
                      :level="levelOf(amountRatio(spend.get(k.id)!.spent, spendLimitOf(k)!.amount))"
                      size="sm"
                      :label="`${k.name} 消费上限用量`"
                    />
                  </div>
                  <span v-else-if="spendLimitOf(k)" class="text-xs tabular-nums">{{ spendLimitText(spendLimitOf(k)!, money) }}</span>
                  <span v-else class="text-muted-foreground text-xs">不限</span>
                </TableCell>
                <TableCell class="hidden whitespace-nowrap sm:table-cell" :class="isExpired(k) ? 'text-destructive' : ''">
                  {{ k.expiresAt ? formatDateTime(k.expiresAt) : '永不过期' }}
                </TableCell>
                <TableCell class="hidden lg:table-cell" :title="formatDateTime(k.lastUsedAt)">
                  {{ k.lastUsedAt ? formatRelative(k.lastUsedAt) : '从未使用' }}
                </TableCell>
                <TableCell class="text-muted-foreground hidden text-xs xl:table-cell">
                  {{ formatDateTime(k.createdAt) }}
                </TableCell>
                <TableCell>
                  <DropdownMenu>
                    <DropdownMenuTrigger as-child>
                      <Button variant="ghost" size="icon-sm" :aria-label="`${k.name} 的操作`">
                        <MoreHorizontal />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem @select="openEdit(k)">
                        <Pencil />
                        编辑
                      </DropdownMenuItem>
                      <DropdownMenuItem @select="ask('rotate', k)">
                        <RotateCw />
                        轮换
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="destructive" @select="ask('revoke', k)">
                        <Trash2 />
                        吊销
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>

    <UsageExamples />

    <KeyFormDialog
      v-model:open="formOpen"
      :gateway-key="editing"
      :model-options="modelOptions"
      :channel-options="channelOptions"
      :options-loading="optionsLoading"
      :account-overflow="accountOverflow"
      @created="onCreated"
      @updated="onUpdated"
      @conflict="load"
    />

    <SecretOnceDialog
      v-if="secret"
      :open="secretOpen"
      :title="secret.title"
      description="请复制下面的密钥，用作 Authorization: Bearer 或 x-api-key 的值。"
      :secret="secret.value"
      @update:open="onSecretOpenChange"
    />

    <ConfirmDialog
      v-model:open="confirmOpen"
      :title="pending?.kind === 'rotate' ? `轮换「${pending.key.name}」？` : `吊销「${pending?.key.name ?? ''}」？`"
      :confirm-text="pending?.kind === 'rotate' ? '轮换' : '吊销'"
      destructive
      :loading="busy"
      @confirm="confirmPending"
    >
      <template v-if="pending?.kind === 'rotate'">
        <p>将生成一个新密钥（继承名称与策略），<strong class="text-foreground">旧密钥立即失效</strong>，使用旧密钥的客户端会马上收到认证错误。</p>
        <p>新密钥只显示一次，请准备好替换各客户端中的配置。</p>
      </template>
      <template v-else>
        <p>吊销后该密钥<strong class="text-foreground">立即无法调用网关</strong>，且无法恢复。已有的请求日志会保留。</p>
      </template>
    </ConfirmDialog>
  </div>
</template>
