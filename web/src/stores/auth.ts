import type { MyGroup, User } from '@/lib/types'
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { ApiError, isApiError } from '@/lib/api'
import { authApi, meApi } from '@/lib/endpoints'
import { myGroupOf } from '@/lib/groups'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const permissions = ref<string[]>([])
  /** phase8 §1.3: own group (multiplier and limits); null on older backends. */
  const group = ref<MyGroup | null>(null)
  /** True once `/api/me` has been answered (200 or 401) at least once. */
  const loaded = ref(false)
  /** Non-auth failure while probing the session (backend unreachable, 5xx, ...). */
  const error = ref<ApiError | null>(null)

  let inflight: Promise<void> | null = null

  const isAuthenticated = computed(() => user.value !== null)
  const permissionSet = computed(() => new Set(permissions.value))

  function can(permission: string | undefined | null): boolean {
    if (!permission)
      return true
    return permissionSet.value.has(permission)
  }

  async function fetchMe(): Promise<void> {
    try {
      const me = await meApi.get()
      user.value = me.user
      permissions.value = me.permissions
      group.value = myGroupOf(me)
      error.value = null
    }
    catch (err) {
      if (isApiError(err) && err.status === 401) {
        user.value = null
        permissions.value = []
        group.value = null
        error.value = null
      }
      else {
        error.value = isApiError(err) ? err : new ApiError(0, 'unknown', String(err))
      }
    }
    finally {
      loaded.value = true
    }
  }

  /** Probe `/api/me` once; concurrent callers share the same request. */
  function ensureLoaded(): Promise<void> {
    if (loaded.value && !error.value)
      return Promise.resolve()
    inflight ??= fetchMe().finally(() => {
      inflight = null
    })
    return inflight
  }

  /** Force a re-fetch (e.g. after the profile changed). */
  function refresh(): Promise<void> {
    inflight = null
    loaded.value = false
    return ensureLoaded()
  }

  function clear(): void {
    user.value = null
    permissions.value = []
    group.value = null
    loaded.value = true
    error.value = null
  }

  async function logout(): Promise<void> {
    try {
      await authApi.logout()
    }
    finally {
      clear()
    }
  }

  return { user, permissions, group, loaded, error, isAuthenticated, can, ensureLoaded, refresh, clear, logout }
})
