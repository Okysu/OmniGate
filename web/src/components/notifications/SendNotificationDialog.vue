<script setup lang="ts">
import AudienceFilterFields from '@/components/AudienceFilterFields.vue'
import { emptyAudience } from '@/lib/audience'
import { useAuthStore } from '@/stores/auth'
import type { NotificationSeverity, SendNotificationInput } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { Loader2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import FormField from '@/components/FormField.vue'
import MultiSelect from '@/components/MultiSelect.vue'
import UserPicker from '@/components/UserPicker.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { useDryRun } from '@/composables/useDryRun'
import { errorMessage, fieldErrors } from '@/lib/api'
import { notificationsApi } from '@/lib/endpoints'
import { useGroupsStore } from '@/stores/groups'

const props = defineProps<{ initialUserIds?: string[], initialGroupIds?: string[] }>()
const open = defineModel<boolean>('open', { required: true })
const groups = useGroupsStore()
const auth = useAuthStore()
const targetMode = ref('list')
const audience = ref(emptyAudience())
const userIds = ref<string[]>([])
const groupIds = ref<string[]>([])
const title = ref('')
const content = ref('')
const link = ref('')
const severity = ref<NotificationSeverity>('info')
const idempotencyKey = ref('')
const saving = ref(false)
const error = ref<string | null>(null)
const errors = ref<Record<string, string>>({})
watch(open, (v) => {
  if (!v)
    return
  userIds.value = [...(props.initialUserIds ?? [])]
  groupIds.value = [...(props.initialGroupIds ?? [])]
  targetMode.value = 'list'
  audience.value = emptyAudience()
  title.value = ''
  content.value = ''
  link.value = ''
  severity.value = 'info'
  idempotencyKey.value = crypto.randomUUID()
  error.value = null
  errors.value = {}
  void groups.load()
}, { flush: 'sync' })
const groupOptions = computed(() => groups.sorted.map(g => ({ value: g.id, label: g.name, hint: `${g.members} 人` })))
const body = computed<SendNotificationInput | null>(() => {
  if ((targetMode.value === 'list' && !userIds.value.length && !groupIds.value.length) || !title.value.trim() || !content.value.trim() || !idempotencyKey.value)
    return null
  return { target: targetMode.value === 'filter' ? { filter: audience.value } : { userIds: userIds.value, groupIds: groupIds.value }, title: title.value.trim(), body: content.value.trim(), link: link.value.trim(), severity: severity.value, idempotencyKey: idempotencyKey.value }
})
const dry = useDryRun(body, JSON.stringify, async (b) => {
  const res = await notificationsApi.send(b, { dryRun: true })
  return { affected: res.alreadySent ? 0 : res.recipients, subscriptions: [] }
}, open)
const canSend = computed(() => !!body.value && dry.ready.value && (dry.preview.value?.affected ?? 0) > 0 && !saving.value)
async function submit() {
  if (!body.value || !canSend.value)
    return
  saving.value = true
  error.value = null
  errors.value = {}
  try {
    const res = await notificationsApi.send(body.value)
    toast.success(res.alreadySent ? '这条通知已发送，请勿重复提交' : `已向 ${res.recipients} 位用户发送通知`)
    open.value = false
  }
  catch (err) {
    error.value = errorMessage(err)
    errors.value = fieldErrors(err)
  }
  finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-xl" data-testid="send-notification-dialog">
      <DialogHeader>
        <DialogTitle>发送通知</DialogTitle>
        <DialogDescription>向指定用户、用户列表或用户组发送站内通知。指定列表取并集；组合条件取交集。仅发送给正常状态用户，确认前预估人数；发送结果写入审计。</DialogDescription>
      </DialogHeader>
      <form id="send-notification-form" class="space-y-4" @submit.prevent="submit">
        <FormField v-if="auth.can('billing.manage')" label="接收范围">
          <Select v-model="targetMode">
            <SelectTrigger class="w-full">
              <SelectValue />
            </SelectTrigger><SelectContent>
              <SelectItem value="list">
                指定用户 / 用户组（取并集）
              </SelectItem><SelectItem value="filter">
                组合条件（全部满足）
              </SelectItem>
            </SelectContent>
          </Select>
        </FormField>
        <AudienceFilterFields v-if="targetMode === 'filter'" v-model="audience" />
        <FormField v-if="targetMode === 'list'" label="指定用户" for="notify-users" :error="errors.target">
          <UserPicker id="notify-users" v-model="userIds" :disabled="saving" />
        </FormField>
        <FormField v-if="targetMode === 'list'" label="用户组" for="notify-groups" hint="可多选；不选用户或用户组不会发送给任何人">
          <MultiSelect id="notify-groups" v-model="groupIds" :options="groupOptions" :disabled="saving" empty-label="不选择用户组" placeholder="搜索用户组" />
        </FormField>
        <FormField label="标题" for="notify-title" required :error="errors.title">
          <Input id="notify-title" v-model="title" maxlength="200" :disabled="saving" />
        </FormField>
        <FormField label="正文" for="notify-body" required :error="errors.body">
          <Textarea id="notify-body" v-model="content" rows="6" maxlength="10000" :disabled="saving" />
        </FormField>
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField label="通知级别" for="notify-severity">
            <Select v-model="severity" :disabled="saving">
              <SelectTrigger id="notify-severity" class="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="info">
                  普通
                </SelectItem>
                <SelectItem value="warning">
                  提醒
                </SelectItem>
                <SelectItem value="critical">
                  重要
                </SelectItem>
              </SelectContent>
            </Select>
          </FormField>
          <FormField label="站内链接（可选）" for="notify-link" :error="errors.link">
            <Input id="notify-link" v-model="link" placeholder="/console/billing" :disabled="saving" />
          </FormField>
        </div>
        <div v-if="title.trim() || content.trim()" class="bg-muted/40 space-y-2 rounded-lg border p-3" data-testid="notification-preview">
          <p class="font-medium">
            {{ title }}
          </p>
          <p class="text-sm whitespace-pre-wrap">
            {{ content }}
          </p>
        </div>
        <p class="text-muted-foreground text-sm" aria-live="polite">
          {{ dry.loading.value || dry.stale.value ? '正在计算接收人数…' : dry.ready.value ? `将发送给 ${dry.preview.value?.affected ?? 0} 位用户` : '请选择接收人并填写通知内容' }}
        </p>
        <p v-if="error || dry.error.value" class="text-destructive text-sm" role="alert">
          {{ error || dry.error.value }}
        </p>
      </form>
      <DialogFooter>
        <Button variant="outline" :disabled="saving" @click="open = false">
          取消
        </Button>
        <Button type="submit" form="send-notification-form" :disabled="!canSend">
          <Loader2 v-if="saving" class="animate-spin" />发送通知
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
