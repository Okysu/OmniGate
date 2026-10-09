<script setup lang="ts">
import type { GroupRef } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { Label } from '@/components/ui/label'
import { errorMessage } from '@/lib/api'
import { adminApi } from '@/lib/endpoints'
import { useGroupsStore } from '@/stores/groups'
import GroupSelect from './GroupSelect.vue'

/** "更改分组" of one user (phase8 §1.3 `PUT /api/admin/users/{id}/group`). */
const props = defineProps<{
  user: { id: string, displayName: string, group?: GroupRef | null } | null
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ changed: [group: GroupRef] }>()

const store = useGroupsStore()
const groupId = ref('')
const saving = ref(false)

watch(open, (v) => {
  if (!v)
    return
  groupId.value = props.user?.group?.id ?? ''
  void store.load()
})

const unchanged = computed(() => !groupId.value || groupId.value === props.user?.group?.id)

async function apply() {
  const u = props.user
  if (!u || unchanged.value)
    return
  saving.value = true
  try {
    await adminApi.setUserGroup(u.id, groupId.value)
    const g = store.byId.get(groupId.value)
    const picked: GroupRef = { id: groupId.value, name: g?.name ?? groupId.value }
    toast.success(`已把 ${u.displayName} 移到「${picked.name}」`)
    open.value = false
    emit('changed', picked)
    // Member counts changed.
    void store.load(true)
  }
  catch (err) {
    toast.error('更改分组失败', { description: errorMessage(err) })
  }
  finally {
    saving.value = false
  }
}
</script>

<template>
  <ConfirmDialog
    v-model:open="open"
    :title="`更改 ${user?.displayName ?? ''} 的分组`"
    confirm-text="确认更改"
    :loading="saving"
    :confirm-disabled="unchanged"
    @confirm="apply"
  >
    <p>新分组的价格倍率与用量限额立即生效（进行中的请求按开始时的分组计费）。用户会收到站内通知。</p>
    <div class="text-foreground space-y-1.5 pt-1">
      <Label for="ugd-group">分组</Label>
      <GroupSelect id="ugd-group" v-model="groupId" :current-id="user?.group?.id ?? null" />
    </div>
  </ConfirmDialog>
</template>
