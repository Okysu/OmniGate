<script setup lang="ts">
// Human-readable plugin permissions, optionally diffed against the last approved version.
import type { PermissionDiff, PluginPermissions } from '@/lib/types'
import { computed } from 'vue'
import { Clock, Database, Globe, KeyRound, ShieldAlert, Sparkles } from '@lucide/vue'
import { groupPermissions, permissionDiffItems } from '@/lib/pluginPermissions'

const props = defineProps<{
  permissions: PluginPermissions | null | undefined
  /** When given, added permissions are highlighted and removed ones listed. */
  diff?: PermissionDiff | null
}>()

const groups = computed(() => groupPermissions(permissionDiffItems(props.permissions, props.diff ?? null)))
const ICONS = { network: Globe, secret: KeyRound, schedule: Clock, storage: Database, dangerous: ShieldAlert, other: Sparkles }
</script>

<template>
  <p v-if="groups.length === 0" class="text-muted-foreground text-sm">
    不申请任何权限（无网络访问、密钥、定时执行或存储）。
  </p>
  <div v-else class="space-y-3">
    <section v-for="g in groups" :key="g.category" class="space-y-1.5">
      <h4 class="text-muted-foreground flex items-center gap-1.5 text-xs font-medium">
        <component :is="ICONS[g.category]" class="size-3.5" />
        {{ g.label }}
      </h4>
      <ul class="space-y-1">
        <li
          v-for="item in g.items"
          :key="item.key + item.status"
          class="rounded-md border px-2.5 py-1.5 text-sm"
          :class="{
            'border-amber-500/50 bg-amber-500/10': item.status === 'added' && diff,
            'text-muted-foreground border-dashed': item.status === 'removed',
          }"
        >
          <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
            <span :class="item.status === 'removed' ? 'line-through' : 'font-medium'">{{ item.label }}</span>
            <span v-if="item.status === 'added' && diff" class="text-xs font-medium text-amber-700 dark:text-amber-300">新增</span>
            <span v-else-if="item.status === 'removed'" class="text-xs">已移除</span>
            <code class="text-muted-foreground ml-auto font-mono text-[11px]">{{ item.key }}</code>
          </div>
          <p v-if="item.detail && item.status !== 'removed'" class="text-muted-foreground mt-0.5 text-xs">
            {{ item.detail }}
          </p>
        </li>
      </ul>
    </section>
  </div>
</template>
