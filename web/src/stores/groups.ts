import type { UserGroup } from '@/lib/types'
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { groupsApi } from '@/lib/endpoints'
import { sortGroups } from '@/lib/groups'

/**
 * User groups (phase8 §1, `GET /api/admin/groups`, users.read), shared by the groups page,
 * the user list / detail and the channel share picker. Loaded on demand and cached.
 */
export const useGroupsStore = defineStore('groups', () => {
  const items = ref<UserGroup[] | null>(null)
  const loading = ref(false)
  const error = ref<unknown>(null)
  let inflight: Promise<void> | null = null

  const sorted = computed(() => sortGroups(items.value ?? []))
  const byId = computed(() => new Map((items.value ?? []).map(g => [g.id, g])))
  const defaultGroup = computed(() => (items.value ?? []).find(g => g.isDefault) ?? null)

  async function fetch(): Promise<void> {
    loading.value = true
    try {
      items.value = (await groupsApi.list()).items
      error.value = null
    }
    catch (err) {
      error.value = err
    }
    finally {
      loading.value = false
    }
  }

  /** Loads once (concurrent callers share the request); `force` refetches. */
  function load(force = false): Promise<void> {
    if (items.value && !force)
      return Promise.resolve()
    inflight ??= fetch().finally(() => {
      inflight = null
    })
    return inflight
  }

  function upsert(g: UserGroup) {
    const list = items.value ?? []
    const next = list.some(x => x.id === g.id) ? list.map(x => (x.id === g.id ? g : x)) : [...list, g]
    // Setting a group as default unsets the previous one (server does the same).
    items.value = g.isDefault ? next.map(x => (x.id === g.id ? x : { ...x, isDefault: false })) : next
  }

  function removeLocal(id: string) {
    items.value = (items.value ?? []).filter(g => g.id !== id)
  }

  function nameOf(id: string): string | null {
    return byId.value.get(id)?.name ?? null
  }

  return { items, sorted, byId, defaultGroup, loading, error, load, upsert, removeLocal, nameOf }
})
