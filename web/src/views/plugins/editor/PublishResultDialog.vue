<script setup lang="ts">
import type { PluginVersion } from '@/lib/types'
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { CircleCheck, Hourglass } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { isCustomProtocol } from '@/lib/pluginProtocol'
import ApprovalBadge from '../ApprovalBadge.vue'
import PermissionList from '../PermissionList.vue'

const props = defineProps<{
  version: PluginVersion | null
  pluginId: string
  /** The plugin had no approved version before this publish. */
  noApprovedBaseline: boolean
  /** First version declaring `protocol: "custom"` (always pending, phase9 §4 #22). */
  firstCustom?: boolean
}>()
const open = defineModel<boolean>('open', { required: true })
const router = useRouter()

const pending = computed(() => props.version?.approval === 'pending')
const autoNote = computed(() => {
  const n = props.version?.approvalNote?.trim() || '权限与上一个已批准版本相同，已自动批准'
  return /[。.!！]$/.test(n) ? n : `${n}。`
})
const firstVersion = computed(() => pending.value && props.noApprovedBaseline)
const custom = computed(() => pending.value && (props.firstCustom ?? (isCustomProtocol(props.version?.manifest) && props.noApprovedBaseline)))

function goDetail() {
  if (!props.version)
    return
  open.value = false
  void router.push({ name: 'plugin-detail', params: { id: props.pluginId }, query: { version: props.version.id } })
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-lg">
      <DialogHeader>
        <DialogTitle class="flex items-center gap-2">
          <Hourglass v-if="pending" class="size-5 text-amber-600 dark:text-amber-400" />
          <CircleCheck v-else class="size-5 text-emerald-600 dark:text-emerald-400" />
          已发布 v{{ version?.version }}
          <ApprovalBadge v-if="version" :approval="version.approval" />
        </DialogTitle>
        <DialogDescription v-if="version">
          <template v-if="!pending">
            {{ autoNote }}渠道现在可以升级到此版本。
          </template>
          <template v-else-if="custom">
            这是该插件第一个自定义协议版本，必须经过系统管理员审批（即使权限未变化；插件将处理全部上游请求与响应），批准前渠道继续使用已批准的旧版本。
          </template>
          <template v-else-if="firstVersion">
            这是该插件第一个需要审批的版本：系统管理员批准全部权限后，渠道才能使用它。
          </template>
          <template v-else>
            权限与上一个已批准版本不同，需要系统管理员审批。批准前渠道继续使用已批准的旧版本。
          </template>
        </DialogDescription>
      </DialogHeader>
      <section v-if="version && pending" class="space-y-2">
        <h3 class="text-sm font-semibold">
          {{ firstVersion ? '申请的权限' : '权限变化' }}
        </h3>
        <PermissionList :permissions="version.manifest?.permissions" :diff="version.permissionDiff" />
      </section>
      <p class="text-muted-foreground text-xs">
        草稿已转为不可变版本。继续编辑会创建新的草稿，下次发布需要更大的版本号。
      </p>
      <DialogFooter>
        <Button variant="outline" @click="open = false">
          继续编辑
        </Button>
        <Button @click="goDetail">
          查看版本详情
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
