<script setup lang="ts">
// Channel detail: overview (basic info, health, models, plugin, plugin
// `channel.detail.overview` contributions), vendor capabilities (owner or
// channels.manage only) and configuration.
import type { Channel, ChannelCapabilities, ChannelView, UiContribution } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeft, Blocks, Clock, KeyRound, Loader2, Pencil, Play, RefreshCw, User as UserIcon, UsersRound, Zap } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import ChannelBadges from '@/components/plugin-ui/ChannelBadges.vue'
import UiContributionCard from '@/components/plugin-ui/UiContributionCard.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { errorMessage } from '@/lib/api'
import { schemaFields } from '@/lib/configSchema'
import { normalizeSharedWith } from '@/lib/channelForm'
import { declinedShares, SHARE_STATUS_CLASSES, SHARE_STATUS_LABELS, shareIndex } from '@/lib/shares'
import { adminApi, channelsApi, pluginsApi } from '@/lib/endpoints'
import { formatDateTime, formatMs, formatRelative } from '@/lib/format'
import { CAPABILITY_LABELS, CAPABILITY_OUTPUT_LABELS, CHANNEL_TYPE_LABELS, HEALTH_LABELS, SCOPE_LABELS } from '@/lib/labels'
import { isFullChannel } from '@/lib/types'
import { useAuthStore } from '@/stores/auth'
import { useGroupsStore } from '@/stores/groups'
import ChannelFormSheet from './ChannelFormSheet.vue'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const id = computed(() => String(route.params.id ?? ''))
const channel = ref<ChannelView | null>(null)
const loadError = ref<unknown>(null)
const loading = ref(false)
const caps = ref<ChannelCapabilities | null>(null)
const capsError = ref<string | null>(null)
const tab = ref('overview')

const full = computed<Channel | null>(() => (channel.value && isFullChannel(channel.value) ? channel.value : null))

// ---------- share targets (phase8 §1.2) ----------
const groups = useGroupsStore()
const shared = computed(() => (full.value?.scope === 'shared' ? normalizeSharedWith(full.value.sharedWith) : null))
const userNames = ref(new Map<string, string>())
// phase5-api.md §5.2: invitation status per user (owner view; absent on older backends).
const shareIdx = computed(() => shareIndex(full.value?.shares))
const declinedUsers = computed(() => (shared.value ? declinedShares(full.value?.shares, shared.value) : []))
watch(() => full.value?.shares, (list) => {
  for (const s of list ?? [])
    userNames.value.set(s.userId, s.displayName)
}, { immediate: true })
watch(shared, async (s) => {
  if (!s || !auth.can('users.read'))
    return
  if (s.groups.length)
    void groups.load()
  // Best effort: label up to 20 users (no bulk lookup endpoint).
  const missing = s.users.filter(id => !userNames.value.has(id)).slice(0, 20)
  await Promise.allSettled(missing.map(async (id) => {
    const d = await adminApi.getUser(id)
    userNames.value.set(id, d.user.displayName)
  }))
}, { immediate: true })

async function loadCapabilities() {
  if (!full.value) {
    caps.value = null
    return
  }
  capsError.value = null
  try {
    caps.value = await channelsApi.capabilities(id.value)
  }
  catch (err) {
    caps.value = null
    capsError.value = errorMessage(err)
  }
}

async function load() {
  loading.value = true
  loadError.value = null
  try {
    channel.value = await channelsApi.get(id.value)
    await loadCapabilities()
  }
  catch (err) {
    loadError.value = err
  }
  finally {
    loading.value = false
  }
}
watch(id, load, { immediate: true })

const hasCapabilities = computed(() => (caps.value?.capabilities.length ?? 0) > 0)
watch(hasCapabilities, (has) => {
  if (!has && tab.value === 'capabilities')
    tab.value = 'overview'
})
const results = computed(() => caps.value?.results ?? {})
const labels = computed(() => Object.fromEntries((caps.value?.capabilities ?? []).map(c => [c.name, c.label])))
const triggerable = computed(() => new Set((caps.value?.capabilities ?? []).filter(c => c.userTriggerable).map(c => c.name)))
function slotItems(slot: string): UiContribution[] {
  return (caps.value?.ui ?? []).filter(u => u.slot === slot)
}

// ---------- invoke ----------
const running = ref<Set<string>>(new Set())
async function invoke(name: string) {
  running.value = new Set(running.value).add(name)
  try {
    const res = await channelsApi.invokeCapability(id.value, name)
    const label = labels.value[name] || CAPABILITY_LABELS[name] || name
    if (res.unsupported)
      toast.info(`${label}：不支持`)
    else if (res.ok)
      toast.success(`${label}：执行成功`, { description: formatMs(res.durationMs) })
    else
      toast.error(`${label}：执行失败`, { description: res.error ?? undefined })
    await loadCapabilities()
    // Badges in the list come from the same results.
    try {
      channel.value = await channelsApi.get(id.value)
    }
    catch { /* best-effort */ }
  }
  catch (err) {
    toast.error('执行失败', { description: errorMessage(err) })
  }
  finally {
    const next = new Set(running.value)
    next.delete(name)
    running.value = next
  }
}

function unsupportedReason(output: unknown): string {
  if (output !== null && typeof output === 'object' && typeof (output as Record<string, unknown>).reason === 'string')
    return (output as Record<string, string>).reason ?? ''
  return ''
}

// ---------- test ----------
const testing = ref(false)
async function testConnection() {
  testing.value = true
  try {
    const res = await channelsApi.test(id.value)
    if (res.ok)
      toast.success('连接正常', { description: `延迟 ${res.latencyMs} ms · HTTP ${res.statusCode}` })
    else
      toast.error('连接失败', { description: `${res.error ?? '未知错误'}${res.statusCode ? `（HTTP ${res.statusCode}）` : ''}` })
    channel.value = await channelsApi.get(id.value)
  }
  catch (err) {
    toast.error('测试失败', { description: errorMessage(err) })
  }
  finally {
    testing.value = false
  }
}

// ---------- edit ----------
const sheetOpen = ref(false)
const editing = ref<Channel | null>(null)
async function openEdit() {
  try {
    const latest = await channelsApi.get(id.value)
    if (!isFullChannel(latest)) {
      toast.error('你没有管理该渠道的权限')
      return
    }
    channel.value = latest
    editing.value = latest
    sheetOpen.value = true
  }
  catch (err) {
    toast.error('无法加载渠道', { description: errorMessage(err) })
  }
}
function onSaved() {
  void load()
}

const pluginLink = computed(() => channel.value?.plugin ? { name: 'plugin-detail', params: { id: channel.value.plugin.id }, query: { version: channel.value.plugin.versionId } } : null)
const pluginConfigEntries = computed(() => {
  const c = full.value
  if (!c)
    return []
  return Object.entries(c.pluginConfig ?? {}).map(([k, v]) => ({ key: k, value: typeof v === 'string' ? v : JSON.stringify(v) }))
})
const secretEntries = computed(() => Object.entries(full.value?.secretFields ?? {}))

// Config field titles come from the pinned version's schema (best-effort).
const fieldTitles = ref<Record<string, string>>({})
watch(() => full.value?.plugin?.versionId, async (vid) => {
  fieldTitles.value = {}
  const p = full.value?.plugin
  if (!vid || !p)
    return
  try {
    const v = await pluginsApi.version(p.id, vid)
    fieldTitles.value = Object.fromEntries(schemaFields(v.manifest?.configSchema).map(f => [f.name, f.prop.title || f.name]))
  }
  catch { /* titles are cosmetic */ }
})

const HEALTH_CLASSES: Record<string, string> = {
  healthy: 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400',
  degraded: 'border-amber-500/50 text-amber-700 dark:text-amber-400',
  open: 'border-destructive/50 text-destructive',
}
</script>

<template>
  <div class="space-y-6">
    <Button variant="ghost" size="sm" class="-ml-2" @click="router.push({ name: 'channels' })">
      <ArrowLeft />
      渠道
    </Button>

    <ErrorState v-if="loadError" :error="loadError" @retry="load" />
    <div v-else-if="!channel" class="space-y-4">
      <Skeleton class="h-10 w-64" />
      <Skeleton class="h-40 w-full" />
    </div>

    <template v-else>
      <PageHeader :title="channel.name">
        <template #badge>
          <Badge variant="outline">
            {{ CHANNEL_TYPE_LABELS[channel.type] ?? channel.type }}
          </Badge>
          <Badge :variant="channel.scope === 'global' ? 'default' : 'secondary'">
            {{ SCOPE_LABELS[channel.scope] ?? channel.scope }}
          </Badge>
          <Badge v-if="channel.status === 'disabled'" variant="destructive">
            已停用
          </Badge>
          <ChannelBadges v-if="full?.badges?.length" :badges="full.badges" />
        </template>
        <template #actions>
          <Button variant="outline" size="sm" :disabled="loading" @click="load">
            <RefreshCw :class="loading ? 'animate-spin' : ''" />
            刷新
          </Button>
          <template v-if="full">
            <Button variant="outline" size="sm" :disabled="testing" @click="testConnection">
              <Loader2 v-if="testing" class="animate-spin" />
              <Zap v-else />
              测试连接
            </Button>
            <Button size="sm" @click="openEdit">
              <Pencil />
              编辑
            </Button>
          </template>
        </template>
      </PageHeader>

      <p v-if="!full" class="text-muted-foreground text-sm">
        你可以通过 API Key 使用此渠道，但不能管理它：凭据、Base URL 与厂商能力（如余额）只对渠道所有者和渠道管理员可见。
      </p>

      <Tabs v-model="tab">
        <TabsList v-if="full">
          <TabsTrigger value="overview">
            概览
          </TabsTrigger>
          <TabsTrigger v-if="hasCapabilities" value="capabilities">
            厂商能力
          </TabsTrigger>
          <TabsTrigger value="config">
            配置
          </TabsTrigger>
        </TabsList>

        <!-- 概览 -->
        <TabsContent value="overview" class="space-y-4 pt-2">
          <div class="grid gap-4 lg:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle class="text-sm">
                  基本信息
                </CardTitle>
              </CardHeader>
              <CardContent>
                <dl class="grid grid-cols-[6rem_1fr] gap-x-4 gap-y-2 text-sm">
                  <dt class="text-muted-foreground">
                    状态
                  </dt>
                  <dd>{{ channel.status === 'enabled' ? '已启用' : '已停用' }}</dd>
                  <template v-if="full">
                    <dt class="text-muted-foreground">
                      Base URL
                    </dt>
                    <dd class="font-mono text-xs break-all">
                      {{ full.baseUrl }}
                    </dd>
                    <dt class="text-muted-foreground">
                      优先级 / 权重
                    </dt>
                    <dd class="tabular-nums">
                      {{ full.priority }} / {{ full.weight }}
                    </dd>
                    <dt class="text-muted-foreground">
                      所有者
                    </dt>
                    <dd>{{ full.owner.displayName }}<span v-if="full.owner.id === auth.user?.id" class="text-muted-foreground">（我）</span></dd>
                    <dt class="text-muted-foreground">
                      API Key
                    </dt>
                    <dd class="font-mono text-xs">
                      {{ full.secret.set ? `已设置 ${full.secret.hint ?? ''}` : '未设置' }}
                    </dd>
                    <template v-if="shared">
                      <dt class="text-muted-foreground">
                        共享给
                      </dt>
                      <dd class="min-w-0 space-y-1.5" data-testid="channel-shared-with">
                        <p v-if="!shared.users.length && !shared.groups.length" class="text-muted-foreground">
                          尚未共享给任何人
                        </p>
                        <div v-if="shared.groups.length" class="flex flex-wrap items-center gap-1">
                          <span class="text-muted-foreground mr-1 inline-flex items-center gap-1 text-xs"><UsersRound class="size-3.5" />用户组 {{ shared.groups.length }}</span>
                          <Badge v-for="gid in shared.groups" :key="gid" variant="outline" class="max-w-48 border-violet-500/40 font-normal text-violet-700 dark:text-violet-300" :title="gid" data-kind="group">
                            <span class="truncate">{{ groups.nameOf(gid) ?? `${gid.slice(0, 8)}…` }}</span>
                            <span v-if="groups.byId.get(gid)" class="text-muted-foreground tabular-nums">· {{ groups.byId.get(gid)!.members }} 人</span>
                          </Badge>
                        </div>
                        <div v-if="shared.users.length" class="flex flex-wrap items-center gap-1">
                          <span class="text-muted-foreground mr-1 inline-flex items-center gap-1 text-xs"><UserIcon class="size-3.5" />用户 {{ shared.users.length }}</span>
                          <Badge v-for="uid in shared.users" :key="uid" variant="outline" class="max-w-48 border-sky-500/40 font-normal text-sky-700 dark:text-sky-300" :title="uid" data-kind="user">
                            <span class="truncate" :class="userNames.has(uid) ? '' : 'font-mono text-[11px]'">{{ userNames.get(uid) ?? `${uid.slice(0, 8)}…` }}</span>
                            <span v-if="shareIdx.get(uid)" class="inline-flex h-4 shrink-0 items-center rounded-sm border px-1 text-[10px] font-medium" :class="SHARE_STATUS_CLASSES[shareIdx.get(uid)!.status]" data-testid="share-status">{{ SHARE_STATUS_LABELS[shareIdx.get(uid)!.status] }}</span>
                          </Badge>
                        </div>
                        <div v-if="declinedUsers.length" class="flex flex-wrap items-center gap-1">
                          <span class="text-muted-foreground mr-1 text-xs">已拒绝 / 已退出</span>
                          <Badge v-for="d in declinedUsers" :key="d.userId" variant="outline" class="max-w-48 font-normal" :class="SHARE_STATUS_CLASSES.declined" :title="d.respondedAt ? `${d.userId} · ${formatDateTime(d.respondedAt)}` : d.userId">
                            <span class="truncate">{{ d.displayName || `${d.userId.slice(0, 8)}…` }}</span>
                          </Badge>
                        </div>
                        <p v-if="shared.users.length" class="text-muted-foreground text-xs">
                          被邀请的用户接受后才能使用此渠道。
                        </p>
                      </dd>
                    </template>
                    <template v-if="full.alerts !== undefined">
                      <dt class="text-muted-foreground">
                        余额告警
                      </dt>
                      <dd class="font-mono text-xs tabular-nums" data-testid="channel-balance-below">
                        {{ full.alerts?.balanceBelow ? `低于 ${full.alerts.balanceBelow}` : '未设置' }}
                      </dd>
                    </template>
                    <dt class="text-muted-foreground">
                      更新时间
                    </dt>
                    <dd :title="formatDateTime(full.updatedAt)">
                      {{ formatRelative(full.updatedAt) }}
                    </dd>
                  </template>
                </dl>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle class="text-sm">
                  健康
                </CardTitle>
              </CardHeader>
              <CardContent class="space-y-2 text-sm">
                <Badge variant="outline" :class="HEALTH_CLASSES[channel.health.state]">
                  {{ HEALTH_LABELS[channel.health.state] ?? channel.health.state }}
                </Badge>
                <p class="text-muted-foreground">
                  连续失败 {{ channel.health.consecutiveFailures }} 次<template v-if="channel.health.lastCheckedAt">
                    · 最近检查 {{ formatRelative(channel.health.lastCheckedAt) }}
                  </template>
                </p>
                <p v-if="channel.health.lastError" class="text-destructive font-mono text-xs break-all">
                  {{ channel.health.lastError }}
                </p>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle class="text-sm">
                  模型（{{ channel.models?.length ?? 0 }}）
                </CardTitle>
                <CardDescription>逻辑模型名 → 上游模型名</CardDescription>
              </CardHeader>
              <CardContent>
                <ul class="max-h-64 space-y-1 overflow-y-auto font-mono text-xs">
                  <li v-for="m in channel.models ?? []" :key="m.model" class="flex gap-1.5">
                    <span class="truncate">{{ m.model }}</span>
                    <template v-if="m.upstreamModel && m.upstreamModel !== m.model">
                      <span class="text-muted-foreground">→</span>
                      <span class="text-muted-foreground truncate">{{ m.upstreamModel }}</span>
                    </template>
                  </li>
                </ul>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle class="flex items-center gap-1.5 text-sm">
                  <Blocks class="size-4" />
                  插件
                </CardTitle>
              </CardHeader>
              <CardContent class="space-y-1 text-sm">
                <template v-if="channel.plugin">
                  <RouterLink v-if="pluginLink" :to="pluginLink" class="font-medium hover:underline">
                    {{ channel.plugin.name }}
                  </RouterLink>
                  <p class="text-muted-foreground font-mono text-xs">
                    {{ channel.plugin.key }} · v{{ channel.plugin.version }}
                  </p>
                  <p v-if="channel.plugin.key.startsWith('builtin.')" class="text-muted-foreground text-xs">
                    内置协议，由网关原生实现。
                  </p>
                  <p v-else-if="full && caps" class="text-muted-foreground text-xs">
                    {{ caps.capabilities.length }} 项厂商能力
                  </p>
                </template>
                <p v-else class="text-muted-foreground">
                  未固定插件版本（使用内置协议）
                </p>
              </CardContent>
            </Card>
          </div>

          <p v-if="capsError" class="text-destructive text-sm">
            无法加载插件能力：{{ capsError }}
          </p>
          <UiContributionCard
            v-for="(u, i) in slotItems('channel.detail.overview')"
            :key="`ov-${i}`"
            :contribution="u"
            :results="results"
            :labels="labels"
            :running="running"
            :triggerable="triggerable"
            @action="invoke"
          />
        </TabsContent>

        <!-- 厂商能力 -->
        <TabsContent v-if="full && hasCapabilities" value="capabilities" class="space-y-4 pt-2">
          <Card>
            <CardHeader>
              <CardTitle class="text-sm">
                能力
              </CardTitle>
              <CardDescription>
                由插件 {{ caps?.plugin?.name }} v{{ caps?.plugin?.version }} 提供。结果保存最近一次执行；定时能力由网关在后台按间隔刷新。
              </CardDescription>
            </CardHeader>
            <CardContent class="p-0">
              <ul class="divide-y">
                <li v-for="c in caps?.capabilities ?? []" :key="c.name" class="flex flex-col gap-2 px-4 py-3 sm:flex-row sm:items-start">
                  <div class="min-w-0 flex-1 space-y-1">
                    <p class="flex flex-wrap items-center gap-2 text-sm font-medium">
                      {{ c.label || CAPABILITY_LABELS[c.name] || c.name }}
                      <span class="text-muted-foreground font-mono text-xs font-normal">{{ c.name }}</span>
                      <Badge variant="outline" class="font-normal">
                        {{ CAPABILITY_OUTPUT_LABELS[c.output as keyof typeof CAPABILITY_OUTPUT_LABELS] ?? c.output }}
                      </Badge>
                      <Badge v-if="c.schedule" variant="secondary" class="font-normal">
                        <Clock />
                        每 {{ c.schedule }}
                      </Badge>
                    </p>
                    <template v-if="results[c.name]">
                      <p class="text-muted-foreground text-xs">
                        最近执行 {{ formatRelative(results[c.name]!.fetchedAt) }} · {{ formatMs(results[c.name]!.durationMs) }} · 插件 v{{ results[c.name]!.pluginVersion }}
                      </p>
                      <p v-if="results[c.name]!.unsupported" class="text-muted-foreground text-sm">
                        不支持{{ unsupportedReason(results[c.name]!.output) ? `：${unsupportedReason(results[c.name]!.output)}` : '' }}
                      </p>
                      <p v-else-if="!results[c.name]!.ok" class="text-destructive text-sm break-words">
                        {{ results[c.name]!.error ?? '执行失败' }}
                      </p>
                      <p v-else class="text-sm text-emerald-700 dark:text-emerald-400">
                        成功
                      </p>
                    </template>
                    <p v-else class="text-muted-foreground text-xs">
                      尚未执行
                    </p>
                  </div>
                  <Button
                    v-if="c.userTriggerable"
                    variant="outline"
                    size="sm"
                    class="shrink-0"
                    :disabled="running.has(c.name)"
                    @click="invoke(c.name)"
                  >
                    <Loader2 v-if="running.has(c.name)" class="animate-spin" />
                    <Play v-else />
                    立即执行
                  </Button>
                </li>
              </ul>
            </CardContent>
          </Card>

          <UiContributionCard
            v-for="(u, i) in slotItems('channel.detail.capabilities')"
            :key="`cap-${i}`"
            :contribution="u"
            :results="results"
            :labels="labels"
            :running="running"
            :triggerable="triggerable"
            @action="invoke"
          />
        </TabsContent>

        <!-- 配置 -->
        <TabsContent v-if="full" value="config" class="pt-2">
          <Card>
            <CardHeader class="flex flex-wrap items-start justify-between gap-2">
              <div class="space-y-1">
                <CardTitle class="text-sm">
                  配置
                </CardTitle>
                <CardDescription>凭据、模型映射、路由与插件配置在编辑面板中修改。</CardDescription>
              </div>
              <Button size="sm" @click="openEdit">
                <Pencil />
                编辑配置
              </Button>
            </CardHeader>
            <CardContent>
              <dl class="grid grid-cols-[8rem_1fr] gap-x-4 gap-y-2 text-sm">
                <dt class="text-muted-foreground">
                  超时
                </dt>
                <dd>{{ full.config.timeoutSeconds ? `${full.config.timeoutSeconds} 秒` : '默认（60 秒）' }}</dd>
                <dt class="text-muted-foreground">
                  自定义请求头
                </dt>
                <dd class="font-mono text-xs">
                  {{ Object.keys(full.config.headers ?? {}).join('、') || '无' }}
                </dd>
                <template v-if="full.type === 'openai'">
                  <dt class="text-muted-foreground">
                    Responses API
                  </dt>
                  <dd>{{ full.config.supportsResponses ? '支持' : '不支持' }}</dd>
                  <dt class="text-muted-foreground">
                    Completions（FIM）
                  </dt>
                  <dd>{{ full.config.supportsCompletions ? '支持' : '不支持' }}</dd>
                </template>
                <dt class="text-muted-foreground">
                  插件配置
                </dt>
                <dd>
                  <span v-if="!pluginConfigEntries.length" class="text-muted-foreground">无</span>
                  <ul v-else class="space-y-0.5">
                    <li v-for="e in pluginConfigEntries" :key="e.key">
                      <span class="text-muted-foreground">{{ fieldTitles[e.key] ?? e.key }}：</span>
                      <span class="font-mono text-xs">{{ e.value }}</span>
                    </li>
                  </ul>
                </dd>
                <dt class="text-muted-foreground">
                  插件密钥
                </dt>
                <dd>
                  <span v-if="!secretEntries.length" class="text-muted-foreground">无</span>
                  <ul v-else class="space-y-0.5">
                    <li v-for="[k, s] in secretEntries" :key="k" class="flex items-center gap-1.5">
                      <KeyRound class="text-muted-foreground size-3.5" />
                      {{ fieldTitles[k] ?? k }}：<span class="font-mono text-xs">{{ s.set ? `已设置 ${s.hint ?? ''}` : '未设置' }}</span>
                    </li>
                  </ul>
                </dd>
              </dl>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </template>

    <ChannelFormSheet v-model:open="sheetOpen" :channel="editing" :can-manage="auth.can('channels.manage')" @saved="onSaved" />
  </div>
</template>
