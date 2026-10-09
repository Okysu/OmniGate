<script setup lang="ts">
import type { ModelInfoRow } from '@/lib/modelInfoForm'
import type { ModelEntry, ModelInfo } from '@/lib/types'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { Eye, EyeOff, FileText, MoreHorizontal, Pencil, Search, Trash2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import CapabilityIcons from '@/components/plaza/CapabilityIcons.vue'
import PlazaModelCard from '@/components/plaza/PlazaModelCard.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage } from '@/lib/api'
import { modelInfoApi, modelsApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { formFromModelInfo, mergeModelInfo, previewPlazaModel } from '@/lib/modelInfoForm'
import { displayNameOf, formatTokenCount, NO_CAPABILITIES } from '@/lib/plaza'
import { isAbortError } from '@/lib/query'
import ModelInfoSheet from './ModelInfoSheet.vue'

/** "模型资料": every logical model merged with its model-info row (models.manage). */
const { currency } = useCurrency()

const models = ref<ModelEntry[]>([])
const infos = ref<ModelInfo[]>([])
const loading = ref(true)
const loadError = ref<unknown>(null)
let controller: AbortController | null = null

async function load() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  loading.value = true
  loadError.value = null
  try {
    const [m, i] = await Promise.all([modelsApi.listAll(ctrl.signal), modelInfoApi.list(ctrl.signal)])
    models.value = m.items ?? []
    infos.value = i.items
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
onMounted(load)
onBeforeUnmount(() => controller?.abort())
defineExpose({ load })

const rows = computed(() => mergeModelInfo(models.value, infos.value))
const vendors = computed(() => [...new Set(infos.value.map(i => i.vendor.trim()).filter(Boolean))].sort())

type StatusFilter = 'all' | 'filled' | 'empty' | 'hidden'
const STATUS_LABELS: Record<StatusFilter, string> = { all: '全部模型', filled: '已填写资料', empty: '未填写资料', hidden: '已隐藏' }
const status = ref<StatusFilter>('all')
const search = ref('')
const visible = computed(() => {
  const q = search.value.trim().toLowerCase()
  return rows.value.filter((r) => {
    if (status.value === 'filled' && !r.info)
      return false
    if (status.value === 'empty' && r.info)
      return false
    if (status.value === 'hidden' && !r.info?.hidden)
      return false
    if (!q)
      return true
    const i = r.info
    return [r.model, i?.displayName ?? '', i?.vendor ?? '', ...(i?.tags ?? [])].some(s => s.toLowerCase().includes(q))
  })
})
const filledCount = computed(() => rows.value.filter(r => r.info).length)
function onStatus(v: unknown) {
  if (v === 'all' || v === 'filled' || v === 'empty' || v === 'hidden')
    status.value = v
}

// ---------- edit ----------
const sheetOpen = ref(false)
const editing = ref<ModelInfoRow | null>(null)
function openEdit(r: ModelInfoRow) {
  editing.value = r
  sheetOpen.value = true
}
function onSaved(info: ModelInfo) {
  const rest = infos.value.filter(i => i.model !== info.model)
  infos.value = [...rest, info]
  if (editing.value?.model === info.model)
    editing.value = rows.value.find(r => r.model === info.model) ?? editing.value
}

// ---------- preview ----------
const previewOpen = ref(false)
const previewRow = ref<ModelInfoRow | null>(null)
const previewModel = computed(() => (previewRow.value ? previewPlazaModel(previewRow.value.model, formFromModelInfo(previewRow.value.info), previewRow.value.entry) : null))
function openPreview(r: ModelInfoRow) {
  previewRow.value = r
  previewOpen.value = true
}

// ---------- delete ----------
const deleting = ref<ModelInfoRow | null>(null)
const deleteOpen = ref(false)
const deleteBusy = ref(false)
function askDelete(r: ModelInfoRow) {
  deleting.value = r
  deleteOpen.value = true
}
async function confirmDelete() {
  const r = deleting.value
  if (!r)
    return
  deleteBusy.value = true
  try {
    await modelInfoApi.remove(r.model)
    infos.value = infos.value.filter(i => i.model !== r.model)
    toast.success(`已删除「${r.model}」的模型资料`)
    deleteOpen.value = false
  }
  catch (err) {
    toast.error('删除失败', { description: errorMessage(err) })
  }
  finally {
    deleteBusy.value = false
  }
}
</script>

<template>
  <Card>
    <CardContent class="space-y-4">
      <div class="flex flex-col gap-2 sm:flex-row sm:items-center" data-testid="model-info-toolbar">
        <div class="relative w-full sm:max-w-xs">
          <Search class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
          <Input v-model="search" placeholder="搜索模型、显示名称、厂商或标签" class="pl-8" aria-label="搜索模型资料" />
        </div>
        <Select :model-value="status" @update:model-value="onStatus">
          <SelectTrigger class="w-full sm:w-36" aria-label="按资料状态过滤">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="(label, key) in STATUS_LABELS" :key="key" :value="key">
              {{ label }}
            </SelectItem>
          </SelectContent>
        </Select>
        <p v-if="!loading || rows.length" class="text-muted-foreground text-xs sm:ml-auto">
          {{ filledCount }} / {{ rows.length }} 个模型已填写资料
        </p>
      </div>

      <ErrorState v-if="loadError" :error="loadError" @retry="load" />
      <div v-else-if="loading && rows.length === 0" class="space-y-2" data-testid="model-info-skeleton">
        <Skeleton v-for="i in 5" :key="i" class="h-12 w-full" />
      </div>
      <EmptyState
        v-else-if="visible.length === 0"
        :icon="FileText"
        :title="rows.length ? '没有匹配的模型' : '暂无模型'"
        :description="rows.length ? undefined : '模型来自已启用渠道中的模型映射。先在「渠道」页面添加渠道并配置模型。'"
      />
      <div v-else class="overflow-x-auto" :class="loading ? 'opacity-60 transition-opacity' : ''">
        <Table data-testid="model-info-table">
          <TableHeader>
            <TableRow>
              <TableHead>模型</TableHead>
              <TableHead class="hidden sm:table-cell">
                厂商
              </TableHead>
              <TableHead class="hidden lg:table-cell">
                标签
              </TableHead>
              <TableHead class="hidden md:table-cell">
                能力
              </TableHead>
              <TableHead class="hidden text-right md:table-cell">
                上下文 / 输出
              </TableHead>
              <TableHead class="hidden text-right lg:table-cell">
                排序
              </TableHead>
              <TableHead>状态</TableHead>
              <TableHead class="hidden xl:table-cell">
                更新时间
              </TableHead>
              <TableHead class="w-24 text-right">
                <span class="sr-only">操作</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="r in visible" :key="r.model" :data-model="r.model">
              <TableCell class="max-w-64">
                <p v-if="r.info && r.info.displayName" class="truncate font-medium">
                  {{ displayNameOf(r.info) }}
                </p>
                <p class="truncate font-mono text-xs" :class="r.info?.displayName ? 'text-muted-foreground' : ''">
                  {{ r.model }}
                </p>
              </TableCell>
              <TableCell class="hidden sm:table-cell">
                {{ r.info?.vendor || '—' }}
              </TableCell>
              <TableCell class="hidden max-w-56 lg:table-cell">
                <div v-if="r.info?.tags.length" class="flex flex-wrap gap-1">
                  <Badge v-for="t in r.info.tags" :key="t" variant="secondary" class="font-normal">
                    {{ t }}
                  </Badge>
                </div>
                <span v-else class="text-muted-foreground">—</span>
              </TableCell>
              <TableCell class="hidden md:table-cell">
                <CapabilityIcons :capabilities="r.info?.capabilities ?? NO_CAPABILITIES" />
                <span v-if="!r.info || !Object.values(r.info.capabilities).some(Boolean)" class="text-muted-foreground">—</span>
              </TableCell>
              <TableCell class="hidden text-right whitespace-nowrap tabular-nums md:table-cell">
                {{ formatTokenCount(r.info?.contextWindow) }} / {{ formatTokenCount(r.info?.maxOutput) }}
              </TableCell>
              <TableCell class="hidden text-right tabular-nums lg:table-cell">
                {{ r.info ? r.info.sortOrder : '—' }}
              </TableCell>
              <TableCell>
                <div class="flex flex-wrap gap-1">
                  <Badge v-if="!r.info" variant="outline" class="text-muted-foreground font-normal">
                    未填写
                  </Badge>
                  <Badge v-else-if="r.info.hidden" variant="outline" class="border-amber-500/50 font-normal text-amber-700 dark:text-amber-400">
                    <EyeOff />
                    已隐藏
                  </Badge>
                  <Badge v-else variant="secondary" class="font-normal">
                    已填写
                  </Badge>
                  <Badge v-if="r.channels === 0" variant="outline" class="text-muted-foreground font-normal" title="没有已启用的渠道提供该模型">
                    无渠道
                  </Badge>
                </div>
              </TableCell>
              <TableCell class="text-muted-foreground hidden text-xs whitespace-nowrap xl:table-cell">
                {{ r.info ? formatDateTime(r.info.updatedAt) : '—' }}
              </TableCell>
              <TableCell class="text-right whitespace-nowrap">
                <Button variant="ghost" size="xs" :data-testid="`edit-${r.model}`" @click="openEdit(r)">
                  <Pencil />
                  {{ r.info ? '编辑' : '填写' }}
                </Button>
                <DropdownMenu>
                  <DropdownMenuTrigger as-child>
                    <Button variant="ghost" size="icon-xs" :aria-label="`${r.model} 的更多操作`">
                      <MoreHorizontal />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end" class="w-40">
                    <DropdownMenuItem @select="openPreview(r)">
                      <Eye />
                      在广场中预览
                    </DropdownMenuItem>
                    <template v-if="r.info">
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="destructive" @select="askDelete(r)">
                        <Trash2 />
                        删除资料
                      </DropdownMenuItem>
                    </template>
                  </DropdownMenuContent>
                </DropdownMenu>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </div>
      <p class="text-muted-foreground text-xs">
        模型资料只用于模型广场与「我的模型」中的展示，不影响路由与计费。「已隐藏」的模型不出现在模型广场中，但仍可调用。
      </p>
    </CardContent>
  </Card>

  <ModelInfoSheet v-model:open="sheetOpen" :row="editing" :vendors="vendors" @saved="onSaved" />

  <Dialog v-model:open="previewOpen">
    <DialogContent class="sm:max-w-md" data-testid="model-info-preview-dialog">
      <DialogHeader>
        <DialogTitle>在广场中预览</DialogTitle>
        <DialogDescription>
          <template v-if="previewRow?.info?.hidden">
            该模型已隐藏，不会出现在模型广场中。
          </template>
          <template v-else-if="!previewRow?.info">
            尚未填写资料：广场中只显示模型名与价格。
          </template>
          <template v-else>
            模型在广场中的卡片样式（协议与套餐以实际广场为准）。
          </template>
        </DialogDescription>
      </DialogHeader>
      <PlazaModelCard v-if="previewModel" :model="previewModel" :currency="currency" static />
    </DialogContent>
  </Dialog>

  <ConfirmDialog
    v-model:open="deleteOpen"
    title="删除模型资料"
    :description="deleting ? `删除「${deleting.model}」的显示名称、简介、标签、规格与能力？模型仍可正常调用，模型广场中只显示模型名。` : undefined"
    confirm-text="删除"
    destructive
    :loading="deleteBusy"
    @confirm="confirmDelete"
  />
</template>
