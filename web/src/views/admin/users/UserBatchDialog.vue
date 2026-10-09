<script setup lang="ts">
import type { BatchSummary, DisableForm } from '@/lib/userAdmin'
import type { User, UserBatchAction } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { CircleAlert } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { errorMessage, fieldErrors, isApiError } from '@/lib/api'
import { adminApi } from '@/lib/endpoints'
import { BATCH_ACTION_LABELS, batchResultText, buildBatchInput, emptyDisableForm, summarizeBatch, USER_BATCH_LIMIT, validateDisableForm } from '@/lib/userAdmin'
import { useGroupsStore } from '@/stores/groups'
import DisableFields from './DisableFields.vue'
import GroupSelect from './GroupSelect.vue'

/** Bulk disable / enable / force logout / set group of the selected users (phase7 §2.3, phase8 §1.3). */
const props = defineProps<{
  users: User[]
  action: UserBatchAction
  /** Current user id: excluded from disable / logout (protected on the server as well). */
  selfId: string | null
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ done: [summary: BatchSummary] }>()

const form = ref<DisableForm>(emptyDisableForm())
const errors = ref<Record<string, string>>({})
const saving = ref(false)
const result = ref<BatchSummary | null>(null)
const resultOpen = ref(false)
const groups = useGroupsStore()
const groupId = ref('')

watch(open, (v) => {
  if (!v)
    return
  form.value = emptyDisableForm()
  errors.value = {}
  groupId.value = ''
  if (props.action === 'set_group')
    void groups.load()
})

// Moving yourself to another group is allowed; disable / logout exclude yourself.
const excludesSelf = computed(() => (props.action === 'disable' || props.action === 'logout') && !!props.selfId && props.users.some(u => u.id === props.selfId))
const targets = computed(() => {
  let list = props.users
  if (excludesSelf.value)
    list = list.filter(u => u.id !== props.selfId)
  // Enabling an active user / disabling a disabled one is a no-op: skip it.
  if (props.action === 'enable')
    list = list.filter(u => u.status === 'disabled')
  else if (props.action === 'disable')
    list = list.filter(u => u.status === 'active')
  // Users already in the chosen group are skipped (when the list knows their group).
  else if (props.action === 'set_group' && groupId.value)
    list = list.filter(u => u.group?.id !== groupId.value)
  return list
})
const groupName = computed(() => groups.nameOf(groupId.value) ?? '')
const skipped = computed(() => props.users.length - targets.value.length - (excludesSelf.value ? 1 : 0))
const preview = computed(() => targets.value.slice(0, 8).map(u => u.displayName).join('、') + (targets.value.length > 8 ? ` 等 ${targets.value.length} 位` : ''))

const label = computed(() => BATCH_ACTION_LABELS[props.action])
const title = computed(() => (props.action === 'set_group' ? `设置 ${props.users.length} 位用户的分组` : `${label.value} ${targets.value.length} 位用户？`))
const needsGroup = computed(() => props.action === 'set_group' && !groupId.value)

async function apply() {
  if (targets.value.length === 0 || targets.value.length > USER_BATCH_LIMIT || needsGroup.value)
    return
  if (props.action === 'disable') {
    errors.value = validateDisableForm(form.value)
    if (Object.keys(errors.value).length)
      return
  }
  saving.value = true
  try {
    const res = await adminApi.batchUsers(buildBatchInput(targets.value.map(u => u.id), props.action, form.value, groupId.value))
    const names = new Map(props.users.map(u => [u.id, u.displayName]))
    const summary = summarizeBatch(res, names)
    open.value = false
    emit('done', summary)
    if (props.action === 'set_group')
      void groups.load(true)
    const text = batchResultText(props.action, summary) + (props.action === 'set_group' && groupName.value ? `（→「${groupName.value}」）` : '')
    if (summary.failed.length) {
      toast.warning(text)
      result.value = summary
      resultOpen.value = true
    }
    else {
      toast.success(text)
    }
  }
  catch (err) {
    if (isApiError(err) && err.status === 422 && props.action === 'disable' && Object.keys(fieldErrors(err)).length) {
      errors.value = fieldErrors(err)
      return
    }
    open.value = false
    toast.error(`批量${label.value}失败`, { description: errorMessage(err) })
  }
  finally {
    saving.value = false
  }
}
</script>

<template>
  <ConfirmDialog
    v-model:open="open"
    :title="title"
    :confirm-text="targets.length ? `${label}（${targets.length}）` : label"
    :destructive="action === 'disable' || action === 'logout'"
    :loading="saving"
    :confirm-disabled="targets.length === 0 || targets.length > USER_BATCH_LIMIT || needsGroup"
    @confirm="apply"
  >
    <div v-if="action === 'set_group'" class="text-foreground space-y-1.5" data-testid="batch-group">
      <Label for="batch-group">移到分组</Label>
      <GroupSelect id="batch-group" v-model="groupId" />
    </div>
    <p v-if="action === 'set_group' && groupId && targets.length === 0">
      所选用户都已在「{{ groupName }}」中。
    </p>
    <p v-else-if="targets.length === 0">
      所选用户中没有需要{{ label }}的用户{{ action === 'enable' ? '（都已是启用状态）' : action === 'disable' ? '（都已停用）' : '' }}。
    </p>
    <template v-else>
      <p class="text-foreground break-all" data-testid="batch-targets">
        {{ preview }}
      </p>
      <p v-if="targets.length > USER_BATCH_LIMIT" class="text-destructive">
        一次最多处理 {{ USER_BATCH_LIMIT }} 位用户，请减少选择。
      </p>
      <p v-if="action === 'disable'">
        停用后这些用户的会话立即失效，API Key 调用将被网关拒绝（403）。用户会收到站内与邮件通知。
      </p>
      <p v-else-if="action === 'enable'">
        启用后这些用户可以重新登录并使用网关。
      </p>
      <p v-else-if="action === 'set_group'">
        新分组的价格倍率与用量限额立即生效，用户会收到站内通知。
      </p>
      <p v-else>
        撤销这些用户的全部登录会话（需要重新登录），不影响 API Key。
      </p>
    </template>
    <p v-if="excludesSelf" class="text-xs">
      已排除你自己（不能对自己执行此操作）。
    </p>
    <p v-if="skipped > 0" class="text-xs">
      已跳过 {{ skipped }} 位{{ action === 'set_group' ? '已在该分组' : action === 'enable' ? '已启用' : '已停用' }}的用户。
    </p>
    <DisableFields v-if="action === 'disable' && targets.length" v-model="form" :errors="errors" id-prefix="batch-disable" />
  </ConfirmDialog>

  <Dialog v-model:open="resultOpen">
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>批量{{ label }}：部分失败</DialogTitle>
        <DialogDescription>
          成功 {{ result?.succeeded ?? 0 }} 位，失败 {{ result?.failed.length ?? 0 }} 位。失败的用户未做任何修改。
        </DialogDescription>
      </DialogHeader>
      <ul class="divide-y rounded-lg border text-sm" data-testid="batch-failures">
        <li v-for="f in result?.failed ?? []" :key="f.id" class="flex items-start gap-2 px-3 py-2">
          <CircleAlert class="text-destructive mt-0.5 size-4 shrink-0" />
          <div class="min-w-0">
            <p class="truncate font-medium">
              {{ f.name }}
            </p>
            <p class="text-muted-foreground text-xs break-all">
              {{ f.message }}
            </p>
          </div>
        </li>
      </ul>
      <DialogFooter>
        <Button @click="resultOpen = false">
          知道了
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
