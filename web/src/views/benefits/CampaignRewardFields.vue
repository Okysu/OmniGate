<script setup lang="ts">
import type { CampaignRewardItem } from '@/lib/benefits'
import type { Plan } from '@/lib/types'
import { computed } from 'vue'
import { Check, ChevronsUpDown } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Combobox, ComboboxAnchor, ComboboxEmpty, ComboboxGroup, ComboboxInput, ComboboxItem, ComboboxItemIndicator, ComboboxList, ComboboxTrigger, ComboboxViewport } from '@/components/ui/combobox'
import { emptyReward } from '@/lib/benefits'
const props = defineProps<{ plans: Plan[] }>()
const reward = defineModel<CampaignRewardItem>({ required: true })
const emit = defineEmits<{ createPlan: [] }>()
const prefix = 'reward-' + crypto.randomUUID()
const hiddenPlans = computed(() => props.plans.filter(p => p.hidden && p.stackable && p.status === 'active'))
const cardPlans = computed({
  get: () => props.plans.filter(p => reward.value.planIds?.includes(p.id)),
  set: (value: Plan[]) => { reward.value = { ...reward.value, planIds: value.map(p => p.id) } },
})
function setType(value: unknown) {
  if (['balance', 'subscription', 'reset_card', 'lottery'].includes(String(value))) reward.value = emptyReward(value as CampaignRewardItem['type'])
}
</script>
<template>
  <div class="space-y-4">
    <div class="space-y-2">
      <Label :for="prefix + 'type'">奖励类型</Label><Select :model-value="reward.type" @update:model-value="setType">
        <SelectTrigger :id="prefix + 'type'" class="w-full">
          <SelectValue />
        </SelectTrigger><SelectContent>
          <SelectItem value="balance">
            余额奖励
          </SelectItem><SelectItem value="subscription">
            限时套餐
          </SelectItem><SelectItem value="reset_card">
            重置卡
          </SelectItem><SelectItem value="lottery">
            抽奖次数
          </SelectItem>
        </SelectContent>
      </Select>
    </div>
    <div v-if="reward.type === 'balance'" class="space-y-2">
      <Label :for="prefix + 'amount'">赠送余额</Label><Input :id="prefix + 'amount'" v-model="reward.amount" inputmode="decimal" required />
    </div>
    <template v-else-if="reward.type === 'subscription'">
      <div class="space-y-2">
        <Label :for="prefix + 'plan'">活动套餐</Label><Select :model-value="reward.planId ?? undefined" @update:model-value="value => reward.planId = String(value)">
          <SelectTrigger :id="prefix + 'plan'" class="w-full">
            <SelectValue placeholder="选择活动套餐" />
          </SelectTrigger><SelectContent>
            <SelectItem v-for="p in hiddenPlans" :key="p.id" :value="p.id">
              {{ p.name }} · {{ p.duration }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
      <Button type="button" variant="outline" @click="emit('createPlan')">
        新建活动套餐
      </Button><p class="text-muted-foreground text-xs">
        须为有效、隐藏且可叠加的活动套餐，每次领取独立额度。
      </p>
    </template>
    <template v-else>
      <div class="space-y-2">
        <Label :for="prefix + 'qty'">每次领取数量</Label><Input :id="prefix + 'qty'" :model-value="reward.quantity" type="number" min="1" max="100" required @update:model-value="value => reward.quantity = Number(value)" />
      </div>
      <template v-if="reward.type === 'reset_card'">
        <div class="space-y-2">
          <Label :for="prefix + 'kind'">重置卡类型</Label><Select v-model="reward.kind">
            <SelectTrigger :id="prefix + 'kind'" class="w-full">
              <SelectValue />
            </SelectTrigger><SelectContent>
              <SelectItem value="weekly">
                周重置卡
              </SelectItem><SelectItem value="5h">
                5 小时重置卡
              </SelectItem><SelectItem value="both">
                双重置卡
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div class="space-y-2">
          <Label :for="prefix + 'days'">领取后有效天数（0 为长期有效）</Label><Input :id="prefix + 'days'" :model-value="reward.validDays ?? 0" type="number" min="0" max="366" required @update:model-value="value => reward.validDays = Number(value)" />
        </div>
        <div class="space-y-2">
          <Label :for="prefix + 'plans'">卡可用于的套餐（不选表示不限）</Label><Combobox v-model="cardPlans" multiple by="id">
            <ComboboxAnchor as-child>
              <ComboboxTrigger as-child aria-label="卡可用于的套餐">
                <Button :id="prefix + 'plans'" type="button" variant="outline" class="w-full justify-between font-normal">
                  <span class="truncate">{{ cardPlans.map(p => p.name).join('、') || '不限套餐' }}</span><ChevronsUpDown class="size-4" />
                </Button>
              </ComboboxTrigger>
            </ComboboxAnchor><ComboboxList class="w-(--reka-combobox-trigger-width)">
              <ComboboxInput placeholder="搜索套餐…" /><ComboboxViewport>
                <ComboboxEmpty>没有匹配项</ComboboxEmpty><ComboboxGroup>
                  <ComboboxItem v-for="p in plans" :key="p.id" :value="p">
                    {{ p.name }}<ComboboxItemIndicator><Check class="size-4" /></ComboboxItemIndicator>
                  </ComboboxItem>
                </ComboboxGroup>
              </ComboboxViewport>
            </ComboboxList>
          </Combobox><Button v-if="cardPlans.length" type="button" variant="ghost" size="sm" @click="cardPlans = []">
            清空限定
          </Button>
        </div>
        <p class="text-muted-foreground text-xs">
          领取后由用户自行使用，保持额度窗口原刷新时间。卡的使用范围与领取条件分别设置。
        </p>
      </template>
      <p v-else class="text-muted-foreground text-xs">
        领取后计入可用抽奖次数，可累积并用于连抽。
      </p>
    </template>
  </div>
</template>
