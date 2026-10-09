<script setup lang="ts">
import type { DisableForm } from '@/lib/userAdmin'
import type { Role, User, UserPatch } from '@/lib/types'
import { computed, ref } from 'vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { errorMessage, fieldErrors, isApiError } from '@/lib/api'
import { adminApi } from '@/lib/endpoints'
import { ROLE_DESCRIPTIONS, ROLE_LABELS } from '@/lib/format'
import { disablePatch, emptyDisableForm, enablePatch, USER_ERROR_MESSAGES, validateDisableForm } from '@/lib/userAdmin'
import DisableFields from './DisableFields.vue'

/**
 * Confirmations for single-user changes (role, disable with reason / until, enable), shared
 * by the user list and the user detail sheet. Call the exposed `request*` methods.
 */
const emit = defineEmits<{
  updated: [user: User]
  /** 409: the caller should reload. */
  stale: []
}>()

type Pending
  = | { kind: 'role', user: User, role: Role }
    | { kind: 'disable', user: User }
    | { kind: 'enable', user: User }

const pending = ref<Pending | null>(null)
const open = ref(false)
const saving = ref(false)
const form = ref<DisableForm>(emptyDisableForm())
const errors = ref<Record<string, string>>({})

function requestRole(user: User, role: Role) {
  if (role === user.role)
    return
  pending.value = { kind: 'role', user, role }
  open.value = true
}
function requestDisable(user: User) {
  pending.value = { kind: 'disable', user }
  form.value = emptyDisableForm()
  errors.value = {}
  open.value = true
}
function requestEnable(user: User) {
  pending.value = { kind: 'enable', user }
  open.value = true
}
defineExpose({ requestRole, requestDisable, requestEnable })

const title = computed(() => {
  const p = pending.value
  if (!p)
    return ''
  if (p.kind === 'role')
    return `将 ${p.user.displayName} 的角色改为「${ROLE_LABELS[p.role]}」？`
  return p.kind === 'disable' ? `停用用户 ${p.user.displayName}？` : `启用用户 ${p.user.displayName}？`
})
const destructive = computed(() => {
  const p = pending.value
  return !!p && (p.kind === 'disable' || (p.kind === 'role' && p.user.role === 'system_admin'))
})
const confirmText = computed(() => {
  const k = pending.value?.kind
  return k === 'disable' ? '停用' : k === 'enable' ? '启用' : '确认修改'
})

async function apply() {
  const p = pending.value
  if (!p)
    return
  let patch: UserPatch
  if (p.kind === 'disable') {
    errors.value = validateDisableForm(form.value)
    if (Object.keys(errors.value).length)
      return
    patch = disablePatch(p.user, form.value)
  }
  else {
    patch = p.kind === 'role' ? { role: p.role, version: p.user.version } : enablePatch(p.user)
  }
  saving.value = true
  try {
    const updated = await adminApi.patchUser(p.user.id, patch)
    emit('updated', updated)
    toast.success(p.kind === 'role' ? '角色已更新' : p.kind === 'disable' ? '用户已停用' : '用户已启用', {
      description: p.kind === 'disable' ? '该用户的会话已失效，API Key 将被网关拒绝（403 account_disabled）。' : undefined,
    })
    open.value = false
  }
  catch (err) {
    if (isApiError(err) && err.status === 422 && p.kind === 'disable') {
      errors.value = fieldErrors(err)
      if (!Object.keys(errors.value).length)
        errors.value = { disabledReason: err.message }
      return
    }
    open.value = false
    if (isApiError(err) && (err.status === 409 || err.code === 'version_conflict')) {
      toast.warning('数据已被他人修改，已刷新')
      emit('stale')
    }
    else if (isApiError(err) && USER_ERROR_MESSAGES[err.code]) {
      toast.error(USER_ERROR_MESSAGES[err.code]!, { description: err.message })
    }
    else if (isApiError(err) && err.status === 403) {
      toast.error('没有权限执行此操作', { description: errorMessage(err) })
    }
    else {
      toast.error('保存失败', { description: errorMessage(err) })
    }
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
    :confirm-text="confirmText"
    :destructive="destructive"
    :loading="saving"
    @confirm="apply"
  >
    <template v-if="pending?.kind === 'role'">
      <p>
        角色将从「{{ ROLE_LABELS[pending.user.role] }}」变更为「{{ ROLE_LABELS[pending.role] }}」，权限变化在该用户下一次请求时生效。
      </p>
      <ul class="list-disc space-y-1 pl-5">
        <li><span class="text-foreground">{{ ROLE_LABELS[pending.user.role] }}（当前）</span>：{{ ROLE_DESCRIPTIONS[pending.user.role] }}</li>
        <li><span class="text-foreground">{{ ROLE_LABELS[pending.role] }}（变更后）</span>：{{ ROLE_DESCRIPTIONS[pending.role] }}</li>
      </ul>
      <p v-if="pending.user.role === 'system_admin'" class="text-destructive">
        该用户将失去系统管理员权限。请确认系统中仍有其他可用的管理员。
      </p>
      <p v-else-if="pending.role === 'system_admin'" class="text-destructive">
        系统管理员拥有全部权限（包括管理其他管理员），请仅授予可信的人员。
      </p>
    </template>
    <template v-else-if="pending?.kind === 'disable'">
      <p>
        停用后，该用户的现有会话立即失效、无法登录控制台，其 API Key 调用将被网关拒绝（403）。数据不会被删除，可随时重新启用。用户会收到站内与邮件通知。
      </p>
      <DisableFields v-model="form" :errors="errors" id-prefix="user-disable" />
    </template>
    <template v-else-if="pending?.kind === 'enable'">
      <p>启用后，该用户可以重新登录并按其角色使用控制台与网关。用户会收到通知。</p>
    </template>
  </ConfirmDialog>
</template>
