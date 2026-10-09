<script setup lang="ts">
import { RefreshCw } from '@lucide/vue'
import PageHeader from '@/components/PageHeader.vue'
import PlazaCatalog from '@/components/plaza/PlazaCatalog.vue'
import { Button } from '@/components/ui/button'
import { usePlazaData } from '@/composables/usePlazaData'
import { plazaApi } from '@/lib/endpoints'

// Console "模型广场": the platform's global models (same data as the public `/models`).
const { items, loading, error, currency, load } = usePlazaData(signal => plazaApi.models({ signal }))
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="模型广场" description="平台为所有用户提供的模型、能力与价格。token 价格按每 100 万 tokens 计，部分模型按次、按张或按音频时长计费；点击模型查看详情、费用估算与调用示例。">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
          刷新
        </Button>
      </template>
    </PageHeader>

    <PlazaCatalog
      :items="items"
      :loading="loading"
      :error="error"
      :currency="currency"
      empty-title="平台暂未提供模型"
      empty-description="管理员还没有配置对所有用户开放的平台渠道。你也可以在「我的模型」中使用自己的渠道。"
      @retry="load"
    />
  </div>
</template>
