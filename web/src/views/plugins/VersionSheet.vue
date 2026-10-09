<script setup lang="ts">
// Version detail: manifest summary, permissions (diffed against the last
// approved version), risk findings and the approval decision. Files open in the
// plugin editor's read-only version mode (/plugins/:id/edit?version=<vid>).
import type { PluginVersion } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Check, Code2, Download, Loader2, X } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ErrorState from '@/components/ErrorState.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { errorMessage } from '@/lib/api'
import { pluginsApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { CAPABILITY_LABELS, CAPABILITY_OUTPUT_LABELS, HOOK_LABELS } from '@/lib/labels'
import { schemaFields } from '@/lib/configSchema'
import { customMeterId, declaredMeters, isBillingPlugin } from '@/lib/customMeters'
import { isCustomProtocol } from '@/lib/pluginProtocol'
import ApprovalBadge from './ApprovalBadge.vue'
import KindBadges from './KindBadges.vue'
import PermissionList from './PermissionList.vue'
import ProtocolBadge from './ProtocolBadge.vue'
import RiskList from './RiskList.vue'
import { downloadVersionZip } from './download'

const props = defineProps<{
  pluginId: string
  versionId: string | null
  builtin: boolean
  canTrust: boolean
  /** Another approved version exists (the server diffs against the newest one). */
  hasOtherApproved: boolean
  /** The newest approved version already uses `protocol: "custom"` (phase9 §4 #22). */
  approvedCustom?: boolean
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ decided: [v: PluginVersion] }>()
const router = useRouter()

const version = ref<PluginVersion | null>(null)
const loading = ref(false)
const loadError = ref<unknown>(null)
const tab = ref('overview')
const note = ref('')
const deciding = ref<'approve' | 'reject' | null>(null)
const exporting = ref(false)

async function load() {
  if (!props.versionId)
    return
  loading.value = true
  loadError.value = null
  try {
    version.value = await pluginsApi.version(props.pluginId, props.versionId)
  }
  catch (err) {
    loadError.value = err
  }
  finally {
    loading.value = false
  }
}

// Immediate: the sheet may mount already open (deep link /plugins/:id?version=…).
watch([open, () => props.versionId], ([o, vid], prev) => {
  const [wasOpen, prevVid] = prev ?? [false, null]
  if (o && vid && (!wasOpen || vid !== prevVid)) {
    version.value = null
    tab.value = 'overview'
    note.value = ''
    void load()
  }
}, { immediate: true })

const manifest = computed(() => version.value?.manifest ?? null)
const capabilities = computed(() => Object.entries(manifest.value?.capabilities ?? {}).sort(([a], [b]) => a.localeCompare(b)))
const configFields = computed(() => schemaFields(manifest.value?.configSchema))
const hooksText = computed(() => (manifest.value?.hooks ?? []).map(name => HOOK_LABELS[name] ?? name).join('、'))
const contributions = computed(() => manifest.value?.uiContributions ?? [])
const custom = computed(() => isCustomProtocol(manifest.value))
/** First version that declares the custom protocol: always needs approval, even with unchanged permissions. */
const firstCustom = computed(() => custom.value && (!props.hasOtherApproved || !props.approvedCustom))
const billing = computed(() => isBillingPlugin(manifest.value))
const meters = computed(() => declaredMeters(manifest.value))
const added = computed(() => version.value?.permissionDiff.added ?? [])
const removed = computed(() => version.value?.permissionDiff.removed ?? [])
/** The diff is meaningful for versions awaiting (or denied) approval. */
const showDiff = computed(() => version.value !== null && version.value.approval !== 'approved')
const firstVersion = computed(() => !props.hasOtherApproved)
const hasDiff = computed(() => showDiff.value && (added.value.length > 0 || removed.value.length > 0))

async function decide(decision: 'approve' | 'reject') {
  if (!version.value)
    return
  deciding.value = decision
  try {
    const v = await pluginsApi.decide(props.pluginId, version.value.id, decision, note.value.trim() || undefined)
    version.value = { ...v, files: version.value.files }
    toast.success(decision === 'approve' ? `已批准 v${v.version}` : `已拒绝 v${v.version}`)
    emit('decided', v)
  }
  catch (err) {
    toast.error('操作失败', { description: errorMessage(err) })
  }
  finally {
    deciding.value = null
  }
}

function openInEditor() {
  if (!version.value)
    return
  void router.push({ name: 'plugin-editor', params: { id: props.pluginId }, query: { version: version.value.id } })
}

async function exportZip() {
  if (!version.value)
    return
  exporting.value = true
  try {
    await downloadVersionZip(props.pluginId, version.value.id, `${manifest.value?.id ?? 'plugin'}-${version.value.version}.zip`)
  }
  catch (err) {
    toast.error('导出失败', { description: errorMessage(err) })
  }
  finally {
    exporting.value = false
  }
}
</script>

<template>
  <Sheet v-model:open="open">
    <SheetContent class="w-full gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[90vw] data-[side=right]:xl:max-w-6xl">
      <SheetHeader class="border-b">
        <SheetTitle class="flex flex-wrap items-center gap-2">
          <span>{{ manifest?.name ?? '插件版本' }}</span>
          <span v-if="version" class="font-mono text-sm">v{{ version.version }}</span>
          <ApprovalBadge v-if="version" :approval="version.approval" />
        </SheetTitle>
        <SheetDescription>
          <template v-if="version">
            发布于 {{ formatDateTime(version.publishedAt) }} · 内容哈希 <span class="font-mono text-xs">{{ version.contentHash.slice(0, 16) }}</span>
          </template>
          <template v-else>
            加载中…
          </template>
        </SheetDescription>
        <div v-if="version && !builtin" class="flex flex-wrap gap-2 pt-1">
          <Button variant="outline" size="sm" @click="openInEditor">
            <Code2 />
            在编辑器中查看
          </Button>
          <Button variant="outline" size="sm" :disabled="exporting" @click="exportZip">
            <Loader2 v-if="exporting" class="animate-spin" />
            <Download v-else />
            导出 ZIP
          </Button>
        </div>
      </SheetHeader>

      <div class="flex-1 overflow-y-auto p-4">
        <ErrorState v-if="loadError" :error="loadError" @retry="load" />
        <div v-else-if="loading || !version" class="space-y-3">
          <Skeleton v-for="i in 4" :key="i" class="h-16 w-full" />
        </div>
        <div v-else class="space-y-4">
          <!-- approval -->
          <section
            v-if="version.approval === 'pending'"
            class="space-y-3 rounded-lg border border-amber-500/40 bg-amber-500/5 p-3"
          >
            <div class="space-y-1 text-sm">
              <p class="font-medium">
                该版本等待权限审批
              </p>
              <p class="text-muted-foreground">
                <template v-if="firstCustom">
                  这是该插件第一个自定义协议版本，必须审批（即使权限与已批准版本相同）：插件将处理全部上游请求与响应，请同时核对代码与权限（见「权限」与「风险」）。
                </template>
                <template v-else-if="firstVersion">
                  该插件还没有已批准的版本，需要审批全部权限（见「权限」）。
                </template>
                <template v-else-if="hasDiff">
                  与上一个已批准版本相比，新增 {{ added.length }} 项、移除 {{ removed.length }} 项权限（见「权限」）。
                </template>
                <template v-else>
                  权限与上一个已批准版本相同。
                </template>
                批准前，渠道不能固定到此版本。
              </p>
            </div>
            <template v-if="canTrust">
              <div class="space-y-1.5">
                <Label for="approve-note">审批备注（可选，写入审计日志）</Label>
                <Textarea id="approve-note" v-model="note" rows="2" maxlength="500" placeholder="例如：已核对新增主机为厂商官方 API 域名" />
              </div>
              <div class="flex flex-wrap gap-2">
                <Button size="sm" :disabled="deciding !== null" @click="decide('approve')">
                  <Loader2 v-if="deciding === 'approve'" class="animate-spin" />
                  <Check v-else />
                  批准
                </Button>
                <Button size="sm" variant="destructive" :disabled="deciding !== null" @click="decide('reject')">
                  <Loader2 v-if="deciding === 'reject'" class="animate-spin" />
                  <X v-else />
                  拒绝
                </Button>
              </div>
            </template>
            <p v-else class="text-muted-foreground text-xs">
              需要系统管理员（plugins.trust）审批。
            </p>
          </section>
          <div v-else-if="version.approvedAt || version.approvalNote" class="text-muted-foreground rounded-lg border p-3 text-sm">
            {{ version.approval === 'rejected' ? '拒绝' : '批准' }}于 {{ formatDateTime(version.approvedAt) }}<template v-if="version.approvalNote">
              ：{{ version.approvalNote }}
            </template>
          </div>

          <Tabs v-model="tab">
            <TabsList class="w-full sm:w-fit">
              <TabsTrigger value="overview">
                概览
              </TabsTrigger>
              <TabsTrigger value="permissions">
                权限<span v-if="hasDiff && added.length" class="ml-1 text-amber-700 dark:text-amber-300">+{{ added.length }}</span>
              </TabsTrigger>
              <TabsTrigger value="risk">
                风险 {{ version.riskCount > 0 ? version.riskCount : '' }}
              </TabsTrigger>
            </TabsList>

            <TabsContent value="overview" class="space-y-4 pt-2">
              <dl class="grid grid-cols-[7rem_1fr] gap-x-4 gap-y-2 text-sm">
                <dt class="text-muted-foreground">
                  插件 ID
                </dt>
                <dd class="font-mono text-xs">
                  {{ manifest?.id }}
                </dd>
                <dt class="text-muted-foreground">
                  协议
                </dt>
                <dd class="flex flex-wrap items-center gap-1">
                  <ProtocolBadge :source="manifest" />
                  <KindBadges :source="manifest" />
                </dd>
                <template v-if="manifest?.entry">
                  <dt class="text-muted-foreground">
                    入口
                  </dt>
                  <dd class="font-mono text-xs">
                    {{ manifest.entry }}
                  </dd>
                </template>
                <template v-if="manifest?.description">
                  <dt class="text-muted-foreground">
                    描述
                  </dt>
                  <dd>{{ manifest.description }}</dd>
                </template>
                <dt class="text-muted-foreground">
                  默认 Base URL
                </dt>
                <dd class="font-mono text-xs break-all">
                  {{ manifest?.defaults.baseUrl || '—' }}
                </dd>
                <dt class="text-muted-foreground">
                  默认模型
                </dt>
                <dd>
                  <span v-if="!manifest?.defaults.models?.length" class="text-muted-foreground">—</span>
                  <span v-else class="flex flex-wrap gap-1">
                    <Badge v-for="m in manifest.defaults.models" :key="m.model" variant="secondary" class="font-mono font-normal">
                      {{ m.model }}{{ m.upstreamModel && m.upstreamModel !== m.model ? ` → ${m.upstreamModel}` : '' }}
                    </Badge>
                  </span>
                </dd>
                <dt class="text-muted-foreground">
                  {{ custom ? '协议 Hook' : '请求 Hook' }}
                </dt>
                <dd>
                  <span v-if="!manifest?.hooks?.length" class="text-muted-foreground">无</span>
                  <span v-else>{{ hooksText }}</span>
                </dd>
                <template v-if="billing">
                  <dt class="text-muted-foreground">
                    计量
                  </dt>
                  <dd>
                    <span v-if="!meters.length" class="text-muted-foreground">未声明</span>
                    <ul v-else class="space-y-1" data-testid="version-meters">
                      <li v-for="m in meters" :key="m.name" class="flex flex-wrap items-baseline gap-x-2">
                        <span>{{ m.label }}<span v-if="m.unit" class="text-muted-foreground">（{{ m.unit }}）</span></span>
                        <span class="text-muted-foreground font-mono text-xs break-all">{{ customMeterId(manifest?.id ?? '', m.name) }}</span>
                      </li>
                    </ul>
                  </dd>
                </template>
                <dt class="text-muted-foreground">
                  配置项
                </dt>
                <dd>
                  <span v-if="!configFields.length" class="text-muted-foreground">无</span>
                  <span v-else class="flex flex-wrap gap-1">
                    <Badge v-for="f in configFields" :key="f.name" variant="outline" class="font-normal">
                      {{ f.prop.title || f.name }}{{ f.secret ? '（密钥）' : '' }}{{ f.required ? ' *' : '' }}
                    </Badge>
                  </span>
                </dd>
                <dt class="text-muted-foreground">
                  界面贡献
                </dt>
                <dd>
                  <span v-if="!contributions.length" class="text-muted-foreground">无</span>
                  <span v-else>{{ contributions.map(c => c.title || c.slot).join('、') }}</span>
                </dd>
              </dl>

              <Separator />

              <section class="space-y-2">
                <h3 class="text-sm font-semibold">
                  能力（{{ capabilities.length }}）
                </h3>
                <p v-if="!capabilities.length" class="text-muted-foreground text-sm">
                  未声明能力。
                </p>
                <div v-else class="overflow-x-auto rounded-lg border">
                  <table class="w-full text-sm">
                    <thead>
                      <tr class="text-muted-foreground border-b text-left text-xs">
                        <th class="px-3 py-2 font-medium">
                          能力
                        </th>
                        <th class="px-3 py-2 font-medium">
                          输出
                        </th>
                        <th class="px-3 py-2 font-medium">
                          手动触发
                        </th>
                        <th class="px-3 py-2 font-medium">
                          定时
                        </th>
                        <th class="px-3 py-2 font-medium">
                          超时 / 缓存
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="[name, c] in capabilities" :key="name" class="border-b last:border-0">
                        <td class="px-3 py-2">
                          <p>{{ c.label || CAPABILITY_LABELS[name] || name }}</p>
                          <p class="text-muted-foreground font-mono text-xs">
                            {{ name }}
                          </p>
                        </td>
                        <td class="px-3 py-2">
                          {{ CAPABILITY_OUTPUT_LABELS[c.output as keyof typeof CAPABILITY_OUTPUT_LABELS] ?? c.output }}
                        </td>
                        <td class="px-3 py-2">
                          {{ c.userTriggerable ? '允许' : '否' }}
                        </td>
                        <td class="px-3 py-2">
                          {{ c.schedule?.minInterval ? `每 ${c.schedule.minInterval}` : '—' }}
                        </td>
                        <td class="text-muted-foreground px-3 py-2 text-xs">
                          {{ c.timeout || '10s' }} / {{ c.cacheTtl || '—' }}
                        </td>
                      </tr>
                    </tbody>
                  </table>
                </div>
              </section>
            </TabsContent>

            <TabsContent value="permissions" class="space-y-3 pt-2">
              <p class="text-muted-foreground text-xs">
                <template v-if="!showDiff">
                  该版本已批准，以下为其声明的全部权限。
                </template>
                <template v-else-if="firstVersion">
                  首个版本：以下全部权限都需要审批。
                </template>
                <template v-else-if="hasDiff">
                  高亮为相对上一个已批准版本新增的权限；删除线为已移除的权限。
                </template>
                <template v-else>
                  权限与上一个已批准版本相同。
                </template>
              </p>
              <PermissionList :permissions="manifest?.permissions" :diff="hasDiff ? version.permissionDiff : null" />
            </TabsContent>

            <TabsContent value="risk" class="space-y-2 pt-2">
              <p v-if="custom" class="rounded-md border border-violet-500/40 bg-violet-500/5 px-2.5 py-1.5 text-xs" data-testid="custom-protocol-risk-note">
                插件第一个声明 protocol: "custom" 的版本总是需要审批（即使静态扫描没有发现风险、权限也未变化）；之后的自定义协议版本权限不变时自动批准。
              </p>
              <RiskList :risk="version.risk ?? []" />
            </TabsContent>
          </Tabs>
        </div>
      </div>
    </SheetContent>
  </Sheet>
</template>
