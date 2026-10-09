<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import PlaceholderPage from '@/components/PlaceholderPage.vue'

const route = useRoute()
const info = computed(() => route.meta.placeholder)
</script>

<template>
  <PlaceholderPage
    v-if="info"
    :key="route.path"
    :title="route.meta.title ?? '未命名页面'"
    :phase="info.phase"
    :description="info.description"
    :features="info.features"
  />
</template>
