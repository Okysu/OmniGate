<script setup lang="ts">
import type { User } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { Loader2, Search, X } from '@lucide/vue'
import { watchDebounced } from '@vueuse/core'
import UserAvatar from '@/components/UserAvatar.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/api'
import { adminApi } from '@/lib/endpoints'
import { useAuthStore } from '@/stores/auth'

/**
 * Picks user IDs. With `users.read` it offers a searchable list via
 * `/api/admin/users?q=`; otherwise the user pastes raw user IDs (UUIDs).
 */
const props = withDefaults(defineProps<{
  multiple?: boolean
  disabled?: boolean
  id?: string
  /** The parent renders the selection itself (e.g. SharePicker's combined list). */
  hideSelected?: boolean
}>(), { multiple: true, disabled: false, id: undefined, hideSelected: false })

const model = defineModel<string[]>({ required: true })
const emit = defineEmits<{
  pick: [user: User | null]
  /** Users seen in search results (lets the parent label selected ids). */
  resolve: [users: User[]]
}>()

const auth = useAuthStore()
const canSearch = computed(() => auth.can('users.read'))

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

const query = ref('')
const results = ref<User[]>([])
const searching = ref(false)
const searchError = ref<string | null>(null)
const known = ref(new Map<string, User>())
const manualError = ref<string | null>(null)

async function search(q: string) {
  if (!canSearch.value)
    return
  searching.value = true
  searchError.value = null
  try {
    const res = await adminApi.listUsers({ page: 1, pageSize: 8, q: q || undefined })
    results.value = res.items
    for (const u of res.items)
      known.value.set(u.id, u)
    emit('resolve', res.items)
  }
  catch (err) {
    searchError.value = errorMessage(err)
  }
  finally {
    searching.value = false
  }
}

watchDebounced(query, q => void search(q.trim()), { debounce: 300 })
watch(canSearch, (v) => {
  if (v)
    void search('')
}, { immediate: true })

function add(id: string, user?: User) {
  if (user)
    known.value.set(id, user)
  if (props.multiple) {
    if (!model.value.includes(id))
      model.value = [...model.value, id]
  }
  else {
    model.value = [id]
  }
  emit('pick', user ?? null)
}

function remove(id: string) {
  model.value = model.value.filter(v => v !== id)
  if (!props.multiple)
    emit('pick', null)
}

function addManual() {
  const ids = query.value.split(/[\s,，]+/).map(s => s.trim()).filter(Boolean)
  const bad = ids.filter(s => !UUID_RE.test(s))
  if (bad.length) {
    manualError.value = `不是合法的用户 ID：${bad.join('、')}`
    return
  }
  manualError.value = null
  for (const id of props.multiple ? ids : ids.slice(0, 1))
    add(id.toLowerCase())
  query.value = ''
}

function display(id: string): string {
  return known.value.get(id)?.displayName ?? id
}
</script>

<template>
  <div class="space-y-2">
    <div v-if="model.length && !hideSelected" class="flex flex-wrap gap-1.5">
      <Badge v-for="uid in model" :key="uid" variant="secondary" class="h-6 max-w-full gap-1 pr-1" :title="uid">
        <span class="truncate" :class="known.has(uid) ? '' : 'font-mono text-[11px]'">{{ display(uid) }}</span>
        <button type="button" class="hover:bg-foreground/10 rounded-sm p-0.5" :aria-label="`移除 ${display(uid)}`" :disabled="disabled" @click="remove(uid)">
          <X class="size-3" />
        </button>
      </Badge>
    </div>

    <template v-if="canSearch">
      <div class="relative">
        <Search class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
        <Input :id="id" v-model="query" placeholder="搜索用户名称或邮箱" class="pl-8" :disabled="disabled" />
        <Loader2 v-if="searching" class="text-muted-foreground absolute top-1/2 right-2.5 size-4 -translate-y-1/2 animate-spin" />
      </div>
      <p v-if="searchError" class="text-destructive text-xs">
        {{ searchError }}
      </p>
      <ul v-else class="max-h-48 divide-y overflow-y-auto rounded-md border">
        <li v-for="u in results" :key="u.id">
          <button
            type="button"
            class="hover:bg-muted flex w-full items-center gap-2 px-2.5 py-1.5 text-left disabled:opacity-50"
            :disabled="disabled || model.includes(u.id)"
            @click="add(u.id, u)"
          >
            <UserAvatar :name="u.displayName" :src="u.avatarUrl" size="sm" />
            <span class="min-w-0 flex-1">
              <span class="block truncate text-sm">{{ u.displayName }}</span>
              <span class="text-muted-foreground block truncate text-xs">{{ u.email ?? u.identities[0]?.login ?? u.id }}</span>
            </span>
            <span v-if="model.includes(u.id)" class="text-muted-foreground text-xs">已选</span>
          </button>
        </li>
        <li v-if="!searching && results.length === 0" class="text-muted-foreground px-2.5 py-3 text-center text-xs">
          没有匹配的用户
        </li>
      </ul>
    </template>
    <template v-else>
      <div class="flex gap-2">
        <Input :id="id" v-model="query" placeholder="粘贴用户 ID（UUID），多个用逗号分隔" class="font-mono text-xs" :disabled="disabled" @keydown.enter.prevent="addManual" />
        <Button type="button" variant="outline" :disabled="disabled || !query.trim()" @click="addManual">
          添加
        </Button>
      </div>
      <p v-if="manualError" class="text-destructive text-xs">
        {{ manualError }}
      </p>
      <p v-else class="text-muted-foreground text-xs">
        你没有查看用户列表的权限（users.read），请向对方索取其用户 ID。
      </p>
    </template>
  </div>
</template>
