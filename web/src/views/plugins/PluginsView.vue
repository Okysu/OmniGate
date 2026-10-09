<script setup lang="ts">
import type { Plugin } from '@/lib/types'
import { computed, onBeforeUnmount, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Blocks, Code2, Eye, Hourglass, MoreHorizontal, Plus, RefreshCw, Trash2, Upload } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { errorMessage, isVersionConflict } from '@/lib/api'
import { pluginsApi } from '@/lib/endpoints'
import { formatRelative } from '@/lib/format'
import { isBillingPlugin } from '@/lib/customMeters'
import { isCustomProtocol } from '@/lib/pluginProtocol'
import { isAbortError } from '@/lib/query'
import { useAuthStore } from '@/stores/auth'
import CreatePluginDialog from './CreatePluginDialog.vue'
import DeletePluginDialog from './DeletePluginDialog.vue'
import ImportPluginDialog from './ImportPluginDialog.vue'
import KindBadges from './KindBadges.vue'
import ProtocolBadge from './ProtocolBadge.vue'
import SourceBadge from './SourceBadge.vue'

const auth = useAuthStore()
const router = useRouter()
const canManage = computed(() => auth.can('plugins.manage'))
const canTrust = computed(() => auth.can('plugins.trust'))

const items = ref<Plugin[]>([])
const loading = ref(false)
const loadError = ref<unknown>(null)
let controller: AbortController | null = null

async function load() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  loading.value = true
  loadError.value = null
  try {
    items.value = (await pluginsApi.list(ctrl.signal)).items
  }
  catch (err) {
    if (!isAbortError(err))
      loadError.value = err
  }
  finally {
    if (controller === ctrl)
      loading.value = false
  }
}
void load()
onBeforeUnmount(() => controller?.abort())

const pendingCount = computed(() => items.value.filter(p => p.pending).length)

// ---------- status toggle ----------
const toggleTarget = ref<Plugin | null>(null)
const toggleOpen = ref(false)
const toggleBusy = ref(false)
function askToggle(p: Plugin, enabled: boolean) {
  if ((p.status === 'enabled') === enabled)
    return
  toggleTarget.value = p
  toggleOpen.value = true
}
async function confirmToggle() {
  const p = toggleTarget.value
  if (!p)
    return
  const next = p.status === 'enabled' ? 'disabled' : 'enabled'
  toggleBusy.value = true
  try {
    const updated = await pluginsApi.setStatus(p.id, next, p.version)
    items.value = items.value.map(i => (i.id === p.id ? { ...updated, versions: undefined } : i))
    toast.success(next === 'enabled' ? `已启用「${p.name}」` : `已停用「${p.name}」`)
    toggleOpen.value = false
  }
  catch (err) {
    toggleOpen.value = false
    if (isVersionConflict(err)) {
      toast.warning('插件已被他人修改，已刷新列表')
      await load()
    }
    else {
      toast.error('操作失败', { description: errorMessage(err) })
    }
  }
  finally {
    toggleBusy.value = false
  }
}

const createOpen = ref(false)
const importOpen = ref(false)

// ---------- delete (only upload / editor plugins; builtin & bundled ship with the gateway) ----------
const deleteTarget = ref<Plugin | null>(null)
const deleteOpen = ref(false)
const deletable = (p: Plugin) => canManage.value && (p.source === 'upload' || p.source === 'editor')
function askDelete(p: Plugin) {
  deleteTarget.value = p
  deleteOpen.value = true
}
function onDeleted(p: Plugin) {
  items.value = items.value.filter(i => i.id !== p.id)
}

function open(p: Plugin) {
  void router.push({ name: 'plugin-detail', params: { id: p.id } })
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="插件" description="渠道插件在内置协议之上扩展厂商能力（余额、模型、健康检查等）与请求 Hook，或实现完整的自定义上游协议；计费插件为套餐提供自定义计量。发布的版本不可变，新增权限需要审批。">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
          刷新
        </Button>
        <template v-if="canManage">
          <Button variant="outline" size="sm" @click="importOpen = true">
            <Upload />
            导入 ZIP
          </Button>
          <Button size="sm" @click="createOpen = true">
            <Plus />
            新建插件
          </Button>
        </template>
      </template>
    </PageHeader>

    <div
      v-if="canTrust && pendingCount > 0"
      class="flex items-start gap-2 rounded-lg border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-sm"
      role="status"
    >
      <Hourglass class="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" />
      <p>
        有 <span class="font-medium tabular-nums">{{ pendingCount }}</span> 个插件的新版本等待权限审批。批准前，渠道仍使用已批准的旧版本。
      </p>
    </div>

    <Card>
      <CardContent>
        <ErrorState v-if="loadError" :error="loadError" @retry="load" />

        <div v-else-if="loading && items.length === 0" class="space-y-2">
          <Skeleton v-for="i in 4" :key="i" class="h-12 w-full" />
        </div>

        <EmptyState v-else-if="items.length === 0" :icon="Blocks" title="还没有插件" description="内置插件会在服务启动时自动注册。可以新建插件或导入 ZIP 包。" />

        <div v-else class="overflow-x-auto" :class="loading ? 'opacity-60 transition-opacity' : ''">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>插件</TableHead>
                <TableHead>来源</TableHead>
                <TableHead class="hidden md:table-cell">
                  协议
                </TableHead>
                <TableHead>版本</TableHead>
                <TableHead class="hidden sm:table-cell">
                  渠道
                </TableHead>
                <TableHead>启用</TableHead>
                <TableHead class="w-10">
                  <span class="sr-only">操作</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="p in items" :key="p.id">
                <TableCell>
                  <div class="max-w-72 min-w-40">
                    <RouterLink :to="{ name: 'plugin-detail', params: { id: p.id } }" class="font-medium hover:underline">
                      {{ p.name }}
                    </RouterLink>
                    <p class="text-muted-foreground truncate font-mono text-xs">
                      {{ p.key }}
                    </p>
                    <p v-if="p.description" class="text-muted-foreground hidden truncate text-xs lg:block" :title="p.description">
                      {{ p.description }}
                    </p>
                    <div v-if="isCustomProtocol(p) || isBillingPlugin(p)" class="mt-1 flex flex-wrap gap-1 md:hidden">
                      <ProtocolBadge :source="p" />
                      <KindBadges :source="p" />
                    </div>
                  </div>
                </TableCell>
                <TableCell>
                  <SourceBadge :source="p.source" />
                </TableCell>
                <TableCell class="hidden md:table-cell">
                  <div class="flex flex-wrap items-center gap-1">
                    <ProtocolBadge :source="p" />
                    <KindBadges :source="p" />
                  </div>
                </TableCell>
                <TableCell>
                  <div class="flex flex-wrap items-center gap-1.5">
                    <span v-if="p.latest" class="font-mono text-sm" :title="`发布于 ${formatRelative(p.latest.publishedAt)}`">v{{ p.latest.version }}</span>
                    <span v-else class="text-muted-foreground text-xs">尚无已批准版本</span>
                    <Badge
                      v-if="p.pending"
                      variant="outline"
                      class="border-amber-500/50 bg-amber-500/10 text-amber-800 dark:text-amber-300"
                      :title="`v${p.pending.version} 等待权限审批`"
                    >
                      v{{ p.pending.version }} 待审批
                    </Badge>
                    <Badge v-if="p.hasDraft && p.source !== 'builtin'" variant="outline" class="text-muted-foreground font-normal">
                      草稿
                    </Badge>
                  </div>
                </TableCell>
                <TableCell class="hidden tabular-nums sm:table-cell">
                  {{ p.channels }}
                </TableCell>
                <TableCell>
                  <div class="flex items-center gap-2">
                    <Switch
                      v-if="canManage && p.source !== 'builtin'"
                      :model-value="p.status === 'enabled'"
                      :aria-label="`${p.name} 是否启用`"
                      @update:model-value="(v: boolean) => askToggle(p, v)"
                    />
                    <span class="text-xs" :class="p.status === 'enabled' ? 'text-muted-foreground' : 'text-destructive'">
                      {{ p.status === 'enabled' ? '启用' : '停用' }}
                    </span>
                  </div>
                </TableCell>
                <TableCell>
                  <DropdownMenu>
                    <DropdownMenuTrigger as-child>
                      <Button variant="ghost" size="icon-sm" :aria-label="`${p.name} 的操作`">
                        <MoreHorizontal />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem @select="open(p)">
                        <Eye />
                        查看详情
                      </DropdownMenuItem>
                      <DropdownMenuItem v-if="canManage && p.source !== 'builtin'" @select="router.push({ name: 'plugin-editor', params: { id: p.id } })">
                        <Code2 />
                        打开编辑器
                      </DropdownMenuItem>
                      <template v-if="deletable(p)">
                        <DropdownMenuSeparator />
                        <DropdownMenuItem variant="destructive" @select="askDelete(p)">
                          <Trash2 />
                          删除插件
                        </DropdownMenuItem>
                      </template>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>

    <CreatePluginDialog v-model:open="createOpen" />
    <ImportPluginDialog v-model:open="importOpen" @imported="load" />
    <DeletePluginDialog v-model:open="deleteOpen" :plugin="deleteTarget" @deleted="onDeleted" />

    <ConfirmDialog
      v-model:open="toggleOpen"
      :title="toggleTarget?.status === 'enabled' ? `停用插件「${toggleTarget?.name ?? ''}」？` : `启用插件「${toggleTarget?.name ?? ''}」？`"
      :confirm-text="toggleTarget?.status === 'enabled' ? '停用' : '启用'"
      :destructive="toggleTarget?.status === 'enabled'"
      :loading="toggleBusy"
      @confirm="confirmToggle"
    >
      <template v-if="toggleTarget?.status === 'enabled'">
        <p>
          停用后，使用该插件的渠道<template v-if="toggleTarget.channels > 0">
            （当前 {{ toggleTarget.channels }} 个）
          </template>会在数据面上被跳过，不再参与路由；定时能力与手动执行也会暂停。
        </p>
        <p>渠道配置与已发布版本会保留，重新启用后立即恢复。</p>
      </template>
      <p v-else>
        启用后，使用该插件的渠道恢复参与路由。
      </p>
    </ConfirmDialog>
  </div>
</template>
