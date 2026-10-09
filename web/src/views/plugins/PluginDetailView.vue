<script setup lang="ts">
import type { Plugin, PluginManifest, PluginVersionSummary } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeft, Code2, Download, ExternalLink, Eye, Gauge, Loader2, RefreshCw, Trash2, Workflow } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatTile from '@/components/StatTile.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { safeHttpsUrl } from '@/components/plugin-ui/bindings'
import { errorMessage } from '@/lib/api'
import { pluginsApi } from '@/lib/endpoints'
import { formatDateTime, formatRelative } from '@/lib/format'
import { customMeterId, declaredMeters, isBillingPlugin } from '@/lib/customMeters'
import { CUSTOM_PROTOCOL_REJECTS, isCustomProtocol, STREAM_CALL_LIMIT_MS, STREAM_TOTAL_LIMIT_MS } from '@/lib/pluginProtocol'
import { useAuthStore } from '@/stores/auth'
import ApprovalBadge from './ApprovalBadge.vue'
import DeletePluginDialog from './DeletePluginDialog.vue'
import { downloadVersionZip } from './download'
import KindBadges from './KindBadges.vue'
import ProtocolBadge from './ProtocolBadge.vue'
import SourceBadge from './SourceBadge.vue'
import VersionSheet from './VersionSheet.vue'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const canManage = computed(() => auth.can('plugins.manage'))
const canTrust = computed(() => auth.can('plugins.trust'))

const id = computed(() => String(route.params.id ?? ''))
const plugin = ref<Plugin | null>(null)
const loading = ref(false)
const loadError = ref<unknown>(null)

/** Manifest of the latest approved version: protocol / kind / meters when the plugin object lacks them. */
const latestManifest = ref<PluginManifest | null>(null)

async function loadLatestManifest(p: Plugin) {
  latestManifest.value = null
  if (!p.latest)
    return
  try {
    const v = await pluginsApi.version(p.id, p.latest.id)
    if (plugin.value?.id === p.id)
      latestManifest.value = v.manifest
  }
  catch {
    // Optional enrichment only.
  }
}

async function load() {
  loading.value = true
  loadError.value = null
  try {
    plugin.value = await pluginsApi.get(id.value)
    void loadLatestManifest(plugin.value)
  }
  catch (err) {
    loadError.value = err
  }
  finally {
    loading.value = false
  }
}
watch(id, load, { immediate: true })

const builtin = computed(() => plugin.value?.source === 'builtin')
/** Only uploaded / editor-created plugins can be deleted (builtin & bundled ship with the gateway). */
const deletable = computed(() => canManage.value && (plugin.value?.source === 'upload' || plugin.value?.source === 'editor'))
const deleteOpen = ref(false)
function onDeleted() {
  void router.push({ name: 'plugins' })
}
const homepage = computed(() => safeHttpsUrl(plugin.value?.homepage))
const versions = computed(() => plugin.value?.versions ?? [])

/** Plugin fields first (list / detail JSON), the latest manifest as fallback. */
const protocolSource = computed(() => {
  const p = plugin.value
  const m = latestManifest.value
  return {
    protocol: p?.protocol || m?.protocol || '',
    extends: p?.extends || m?.extends || m?.inherits || '',
    kind: p?.kind ?? m?.kind,
  }
})
const custom = computed(() => isCustomProtocol(protocolSource.value))
const billing = computed(() => isBillingPlugin(protocolSource.value))
const meters = computed(() => {
  const p = plugin.value
  if (p?.meters)
    return declaredMeters(p)
  return declaredMeters(latestManifest.value)
})

const sheetOpen = ref(false)
const selected = ref<PluginVersionSummary | null>(null)
function view(v: PluginVersionSummary) {
  selected.value = v
  sheetOpen.value = true
}
// Deep link: /plugins/:id?version=<vid> opens that version.
watch([versions, () => route.query.version], ([list, vid]) => {
  if (typeof vid === 'string' && !sheetOpen.value) {
    const v = list.find(x => x.id === vid)
    if (v)
      view(v)
  }
})
watch(sheetOpen, (o) => {
  if (!o && route.query.version)
    void router.replace({ query: { ...route.query, version: undefined } })
})
const hasOtherApproved = computed(() => versions.value.some(v => v.approval === 'approved' && v.id !== selected.value?.id))

const exporting = ref<string | null>(null)
async function exportZip(v: PluginVersionSummary) {
  exporting.value = v.id
  try {
    await downloadVersionZip(id.value, v.id, `${plugin.value?.key ?? 'plugin'}-${v.version}.zip`)
  }
  catch (err) {
    toast.error('导出失败', { description: errorMessage(err) })
  }
  finally {
    exporting.value = null
  }
}
</script>

<template>
  <div class="space-y-6">
    <Button variant="ghost" size="sm" class="-ml-2" @click="router.push({ name: 'plugins' })">
      <ArrowLeft />
      插件
    </Button>

    <ErrorState v-if="loadError" :error="loadError" @retry="load" />

    <div v-else-if="!plugin" class="space-y-4">
      <Skeleton class="h-10 w-64" />
      <Skeleton class="h-32 w-full" />
    </div>

    <template v-else>
      <PageHeader :title="plugin.name" :description="plugin.description || undefined">
        <template #badge>
          <SourceBadge :source="plugin.source" />
          <Badge v-if="plugin.status === 'disabled'" variant="destructive">
            已停用
          </Badge>
        </template>
        <template #actions>
          <Button variant="outline" size="sm" :disabled="loading" @click="load">
            <RefreshCw :class="loading ? 'animate-spin' : ''" />
            刷新
          </Button>
          <Button v-if="canManage && !builtin" size="sm" @click="router.push({ name: 'plugin-editor', params: { id: plugin.id } })">
            <Code2 />
            {{ plugin.hasDraft ? '继续编辑草稿' : '打开编辑器' }}
          </Button>
          <Button v-if="deletable" variant="destructive" size="sm" @click="deleteOpen = true">
            <Trash2 />
            删除插件
          </Button>
        </template>
      </PageHeader>

      <div class="text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
        <span class="font-mono text-xs">{{ plugin.key }}</span>
        <span class="inline-flex items-center gap-1">
          <ProtocolBadge :source="protocolSource" />
          <KindBadges :source="protocolSource" />
        </span>
        <span v-if="plugin.author">作者：{{ plugin.author }}</span>
        <a v-if="homepage" :href="homepage" target="_blank" rel="noopener noreferrer" class="text-primary inline-flex items-center gap-1 hover:underline">
          主页
          <ExternalLink class="size-3.5" />
        </a>
        <span>更新于 {{ formatRelative(plugin.updatedAt) }}</span>
      </div>

      <div class="grid gap-3 sm:grid-cols-3">
        <StatTile label="最新已批准版本" :value="plugin.latest ? `v${plugin.latest.version}` : '—'" :hint="plugin.latest ? `发布于 ${formatDateTime(plugin.latest.publishedAt)}` : '尚无可供渠道使用的版本'" />
        <StatTile label="待审批" :value="plugin.pending ? `v${plugin.pending.version}` : '无'" :hint="plugin.pending ? '批准前渠道不能使用该版本' : undefined" />
        <StatTile label="使用中的渠道" :value="String(plugin.channels)" />
      </div>

      <Card v-if="custom" data-testid="custom-protocol-card">
        <CardHeader>
          <CardTitle class="flex items-center gap-2">
            <Workflow class="size-4" />
            自定义协议
          </CardTitle>
          <CardDescription>
            插件实现完整的上游协议：buildRequest 构造上游请求，parseResponse / parseStream 把上游响应转换为网关的 Chat Completions 中间格式。
          </CardDescription>
        </CardHeader>
        <CardContent>
          <ul class="text-muted-foreground list-disc space-y-1 pl-5 text-sm">
            <li>使用该插件的渠道接受 OpenAI Chat、Anthropic Messages 与 OpenAI Responses 客户端（由网关转换）；不支持 {{ CUSTOM_PROTOCOL_REJECTS.join('、') }} 接口。</li>
            <li>流式请求全程固定使用同一个运行时；每次 parseStream 调用限时 {{ STREAM_CALL_LIMIT_MS }} ms，整个请求的 JS 总耗时上限 {{ STREAM_TOTAL_LIMIT_MS / 1000 }} s，超出即中断并返回 plugin_error。</li>
            <li>插件第一个自定义协议版本必须经过审批（即使权限未变化）；之后的版本按权限变化规则审批。</li>
          </ul>
        </CardContent>
      </Card>

      <Card v-if="billing" data-testid="billing-meters-card">
        <CardHeader>
          <CardTitle class="flex items-center gap-2">
            <Gauge class="size-4" />
            计量（{{ meters.length }}）
          </CardTitle>
          <CardDescription>
            计费插件声明的自定义计量，可在套餐规则的「计量」中选择（插件需启用且版本已批准）。数值与单位由插件计算；插件超时、出错或不可用时该次计量按 0 记。
          </CardDescription>
        </CardHeader>
        <CardContent>
          <p v-if="!meters.length" class="text-muted-foreground text-sm">
            {{ plugin.latest ? '最新已批准版本未声明计量。' : '尚无已批准版本。' }}
          </p>
          <div v-else class="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>计量</TableHead>
                  <TableHead>单位</TableHead>
                  <TableHead class="hidden sm:table-cell">
                    规则中的 meter
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="m in meters" :key="m.name">
                  <TableCell>
                    <p>{{ m.label }}</p>
                    <p class="text-muted-foreground font-mono text-xs">
                      {{ m.name }}
                    </p>
                  </TableCell>
                  <TableCell>
                    <span v-if="m.unit">{{ m.unit }}</span>
                    <span v-else class="text-muted-foreground">—</span>
                  </TableCell>
                  <TableCell class="hidden font-mono text-xs break-all sm:table-cell">
                    {{ customMeterId(plugin.key, m.name) }}
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>版本</CardTitle>
          <CardDescription>
            已发布的版本不可变。渠道固定在某个已批准版本上，可在渠道编辑中升级或回滚。
          </CardDescription>
        </CardHeader>
        <CardContent>
          <p v-if="versions.length === 0" class="text-muted-foreground py-6 text-center text-sm">
            还没有发布任何版本。{{ canManage && !builtin ? '在编辑器中完成草稿后点击「发布」。' : '' }}
          </p>
          <div v-else class="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>版本</TableHead>
                  <TableHead>审批</TableHead>
                  <TableHead>风险</TableHead>
                  <TableHead class="hidden sm:table-cell">
                    发布时间
                  </TableHead>
                  <TableHead class="text-right">
                    操作
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="v in versions" :key="v.id">
                  <TableCell class="font-mono">
                    v{{ v.version }}
                    <Badge v-if="plugin.latest?.id === v.id" variant="secondary" class="ml-1 font-sans font-normal">
                      最新
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <ApprovalBadge :approval="v.approval" />
                  </TableCell>
                  <TableCell>
                    <span v-if="v.riskCount > 0" class="text-amber-700 tabular-nums dark:text-amber-400">{{ v.riskCount }} 项提示</span>
                    <span v-else class="text-muted-foreground">无</span>
                  </TableCell>
                  <TableCell class="hidden sm:table-cell" :title="formatDateTime(v.publishedAt)">
                    {{ formatRelative(v.publishedAt) }}
                  </TableCell>
                  <TableCell class="text-right">
                    <div class="flex justify-end gap-1">
                      <Button v-if="!builtin" variant="ghost" size="sm" :disabled="exporting === v.id" :aria-label="`导出 v${v.version} 的 ZIP`" @click="exportZip(v)">
                        <Loader2 v-if="exporting === v.id" class="animate-spin" />
                        <Download v-else />
                        <span class="hidden sm:inline">导出</span>
                      </Button>
                      <Button variant="outline" size="sm" @click="view(v)">
                        <Eye />
                        查看
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>

      <VersionSheet
        v-model:open="sheetOpen"
        :plugin-id="plugin.id"
        :version-id="selected?.id ?? null"
        :builtin="builtin"
        :can-trust="canTrust"
        :has-other-approved="hasOtherApproved"
        :approved-custom="isCustomProtocol(plugin)"
        @decided="load"
      />
      <DeletePluginDialog v-model:open="deleteOpen" :plugin="plugin" @deleted="onDeleted" />
    </template>
  </div>
</template>
