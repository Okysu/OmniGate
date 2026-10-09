<script setup lang="ts">
import { computed } from 'vue'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { multiplierShort } from '@/lib/groups'
import { useGroupsStore } from '@/stores/groups'

/** Picks a user group (from the cached `GET /api/admin/groups`). */
defineProps<{
  id?: string
  /** Marked "当前" in the list. */
  currentId?: string | null
}>()
const model = defineModel<string>({ required: true })
const store = useGroupsStore()
const placeholder = computed(() => (store.loading && !store.items ? '加载中…' : store.error && !store.items ? '分组加载失败' : '选择分组'))

function onUpdate(v: unknown) {
  if (typeof v === 'string')
    model.value = v
}
</script>

<template>
  <Select :model-value="model || undefined" @update:model-value="onUpdate">
    <SelectTrigger :id="id" class="w-full" data-testid="group-select">
      <SelectValue :placeholder="placeholder" />
    </SelectTrigger>
    <SelectContent>
      <SelectItem v-for="g in store.sorted" :key="g.id" :value="g.id">
        {{ g.name }}
        <span class="text-muted-foreground text-xs tabular-nums">{{ multiplierShort(g.priceMultiplier) }}<template v-if="g.isDefault"> · 默认</template><template v-if="g.id === currentId"> · 当前</template></span>
      </SelectItem>
      <div v-if="store.items && store.items.length === 0" class="text-muted-foreground px-2 py-1.5 text-xs">
        没有分组
      </div>
    </SelectContent>
  </Select>
</template>
