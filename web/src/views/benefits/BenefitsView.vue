<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { benefitsApi, type PublicCampaign } from '@/lib/benefits'
import { errorMessage } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { Button } from '@/components/ui/button'
import CampaignCard from './CampaignCard.vue'
import PageHeader from '@/components/PageHeader.vue'
import LotteryCard from './LotteryCard.vue'
const auth = useAuthStore()
const campaigns = ref<PublicCampaign[]>([])
const error = ref('')
onMounted(async () => { try { campaigns.value = (await benefitsApi.list()).items } catch (err) { error.value = errorMessage(err) } })
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="福利">
      <template #actions>
        <Button v-if="auth.can('billing.manage')" as-child variant="outline">
          <RouterLink to="/console/admin/benefits">
            管理活动
          </RouterLink>
        </Button>
      </template>
    </PageHeader>
    <p v-if="error" role="alert" class="text-destructive">
      {{ error }}
    </p>
    <div v-if="campaigns.length" class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <CampaignCard v-for="campaign in campaigns" :key="campaign.id" :campaign="campaign" />
    </div>
    <LotteryCard />
  </div>
</template>
