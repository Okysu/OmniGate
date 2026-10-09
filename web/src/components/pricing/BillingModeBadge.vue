<script setup lang="ts">
import type { BillingMode } from '@/lib/billingMode'
import { Badge } from '@/components/ui/badge'
import { BILLING_MODE_BADGE_CLASSES, BILLING_MODE_META } from '@/lib/billingMode'

/** 计费方式 badge: Token / 按次 / 按张 / 按时长 / 组合. */
defineProps<{ mode: BillingMode }>()
</script>

<template>
  <Badge variant="outline" class="font-normal" :class="BILLING_MODE_BADGE_CLASSES[mode]" :title="BILLING_MODE_META[mode].description" data-testid="billing-mode-badge" :data-mode="mode">
    {{ BILLING_MODE_META[mode].short }}
  </Badge>
</template>
