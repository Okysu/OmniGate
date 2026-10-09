<script setup lang="ts">
import type { User, Wallet } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { Loader2, RefreshCw } from '@lucide/vue'
import { toast } from 'vue-sonner'
import FormField from '@/components/FormField.vue'
import UserPicker from '@/components/UserPicker.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, fieldErrors, isApiError, isVersionConflict } from '@/lib/api'
import { adminBillingApi } from '@/lib/endpoints'
import { addAmounts, amountSign, isValidAmount } from '@/lib/money'

const props = defineProps<{
  /** Fixed user (e.g. from the user detail sheet): no picker is shown. */
  user?: { id: string, displayName: string } | null
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ adjusted: [wallet: Wallet] }>()
const { money, currency } = useCurrency()

const userIds = ref<string[]>([])
const userName = ref<string | null>(null)
const wallet = ref<Wallet | null>(null)
const walletLoading = ref(false)
const walletError = ref<string | null>(null)
const amount = ref('')
const note = ref('')
const errors = ref<Record<string, string>>({})
const formError = ref<string | null>(null)
const saving = ref(false)

watch(open, (v) => {
  if (!v)
    return
  userIds.value = props.user ? [props.user.id] : []
  userName.value = props.user?.displayName ?? null
  wallet.value = null
  walletError.value = null
  amount.value = ''
  note.value = ''
  errors.value = {}
  formError.value = null
})

const userId = computed(() => userIds.value[0] ?? null)

async function loadWallet() {
  const id = userId.value
  wallet.value = null
  walletError.value = null
  if (!id)
    return
  walletLoading.value = true
  try {
    wallet.value = await adminBillingApi.wallet(id)
  }
  catch (err) {
    walletError.value = isApiError(err) && err.status === 404 ? '用户不存在' : errorMessage(err)
  }
  finally {
    walletLoading.value = false
  }
}
watch(userId, loadWallet)

function onPick(u: User | null) {
  userName.value = u?.displayName ?? null
}

const preview = computed(() => {
  if (!wallet.value || !isValidAmount(amount.value, { allowNegative: true }))
    return null
  return addAmounts(wallet.value.balance, amount.value.trim())
})

async function submit() {
  formError.value = null
  const e: Record<string, string> = {}
  if (!userId.value || !wallet.value)
    e.user = '请先选择用户并加载钱包'
  if (!isValidAmount(amount.value, { allowNegative: true }) || amountSign(amount.value) === 0)
    e.amount = '请输入非 0 金额，可为负数（最多 9 位小数）'
  if (!note.value.trim())
    e.note = '请填写调整原因'
  else if (note.value.trim().length > 200)
    e.note = '最多 200 个字符'
  errors.value = e
  if (Object.keys(e).length || !wallet.value || !userId.value)
    return
  saving.value = true
  try {
    const res = await adminBillingApi.adjust(userId.value, { amount: amount.value.trim(), note: note.value.trim(), version: wallet.value.version })
    wallet.value = res.wallet
    emit('adjusted', res.wallet)
    toast.success('余额已调整', { description: `${userName.value ?? '用户'}：${money(res.entry.amount, { signed: true })}，调整后余额 ${money(res.wallet.balance)}` })
    amount.value = ''
    note.value = ''
  }
  catch (err) {
    if (isVersionConflict(err)) {
      formError.value = '钱包余额刚刚发生了变化，已重新加载，请确认后再提交'
      await loadWallet()
    }
    else if (isApiError(err) && err.status === 422) {
      errors.value = fieldErrors(err)
      formError.value = err.message
    }
    else {
      formError.value = errorMessage(err)
    }
  }
  finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>调整用户余额</DialogTitle>
        <DialogDescription>手工增减用户钱包余额（如补偿、纠错）。每次调整都会记入用户流水与审计日志。</DialogDescription>
      </DialogHeader>

      <form id="adjust-form" class="space-y-4" novalidate @submit.prevent="submit">
        <FormField v-if="!user" label="用户" for="adj-user" required :error="errors.user">
          <UserPicker id="adj-user" v-model="userIds" :multiple="false" @pick="onPick" />
        </FormField>
        <p v-else-if="errors.user" class="text-destructive text-xs" role="alert">
          {{ errors.user }}
        </p>

        <div v-if="userId" class="rounded-lg border p-3">
          <div class="mb-2 flex items-center justify-between">
            <p class="text-sm font-medium">
              当前钱包{{ userName ? `：${userName}` : '' }}
            </p>
            <Button type="button" variant="ghost" size="icon-xs" aria-label="重新加载钱包" :disabled="walletLoading" @click="loadWallet">
              <RefreshCw :class="walletLoading ? 'animate-spin' : ''" />
            </Button>
          </div>
          <p v-if="walletError" class="text-destructive text-xs">
            {{ walletError }}
          </p>
          <div v-else-if="walletLoading || !wallet" class="grid grid-cols-3 gap-2">
            <Skeleton v-for="i in 3" :key="i" class="h-10" />
          </div>
          <dl v-else class="grid grid-cols-3 gap-2 text-sm">
            <div>
              <dt class="text-muted-foreground text-xs">
                余额
              </dt>
              <dd class="font-medium tabular-nums">
                {{ money(wallet.balance) }}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground text-xs">
                预留中
              </dt>
              <dd class="tabular-nums">
                {{ money(wallet.reserved) }}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground text-xs">
                可用
              </dt>
              <dd class="tabular-nums">
                {{ money(wallet.available) }}
              </dd>
            </div>
          </dl>
        </div>

        <FormField :label="`调整金额（${currency?.code ?? '结算币种'}）`" for="adj-amount" required :error="errors.amount" hint="正数为增加，负数为扣减。">
          <Input id="adj-amount" v-model="amount" inputmode="decimal" placeholder="例如 10 或 -5.5" class="font-mono tabular-nums" />
        </FormField>
        <p v-if="preview !== null && wallet" class="text-muted-foreground text-xs">
          调整后余额：<span class="tabular-nums" :class="amountSign(preview) < 0 ? 'text-destructive' : 'text-foreground'">{{ money(preview) }}</span>
        </p>
        <FormField label="备注" for="adj-note" required :error="errors.note" hint="用户可在流水中看到此备注。">
          <Input id="adj-note" v-model="note" maxlength="200" placeholder="例如：故障补偿" />
        </FormField>
      </form>

      <DialogFooter class="items-center">
        <p v-if="formError" class="text-destructive mr-auto text-xs" role="alert">
          {{ formError }}
        </p>
        <Button variant="outline" :disabled="saving" @click="open = false">
          关闭
        </Button>
        <Button type="submit" form="adjust-form" :disabled="saving || !wallet">
          <Loader2 v-if="saving" class="animate-spin" />
          确认调整
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
