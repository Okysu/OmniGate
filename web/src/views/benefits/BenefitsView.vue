<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { benefitsApi, type PublicCampaign } from '@/lib/benefits'
import { errorMessage } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { Button } from '@/components/ui/button'
import CampaignManager from './CampaignManager.vue'
import CampaignCard from './CampaignCard.vue'
import PageHeader from '@/components/PageHeader.vue'
import LotteryCard from './LotteryCard.vue'
const auth = useAuthStore()
const campaigns = ref<PublicCampaign[]>([])
const error = ref('')
const managerOpen = ref(false)
const startWithCreate = ref(false)
async function loadCampaigns() { try { campaigns.value = (await benefitsApi.list()).items } catch (err) { error.value = errorMessage(err) } }
onMounted(loadCampaigns)
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="福利">
      <template #actions>
        <template v-if="auth.can('billing.manage')">
          <Button variant="outline" @click="startWithCreate = false; managerOpen = true">
            管理活动
          </Button><Button @click="startWithCreate = true; managerOpen = true">
            新建活动
          </Button>
        </template>
      </template>
    </PageHeader>
    <p v-if="error" role="alert" class="text-destructive">
      {{ error }}
    </p>
    <div v-if="campaigns.length" class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <CampaignCard v-for="campaign in campaigns" :key="campaign.id" :campaign="campaign" />
    </div>
    <LotteryCard />
    <CampaignManager v-if="auth.can('billing.manage')" v-model:open="managerOpen" :start-with-create="startWithCreate" @saved="loadCampaigns" />
  </div>
</template>
