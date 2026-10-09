import { computed, ref, watch } from 'vue'

export type ThemePreference = 'light' | 'dark' | 'system'

/** Keep in sync with the inline bootstrap script in index.html. */
export const THEME_STORAGE_KEY = 'omnigate-theme'

function readStored(): ThemePreference {
  try {
    const v = window.localStorage.getItem(THEME_STORAGE_KEY)
    if (v === 'light' || v === 'dark' || v === 'system')
      return v
  }
  catch {
    // storage unavailable (private mode, blocked cookies, ...)
  }
  return 'system'
}

function writeStored(value: ThemePreference): void {
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, value)
  }
  catch {
    // ignore
  }
}

const media = typeof window !== 'undefined' && typeof window.matchMedia === 'function'
  ? window.matchMedia('(prefers-color-scheme: dark)')
  : null

// Module-level singleton state: one theme for the whole app.
const preference = ref<ThemePreference>(typeof window === 'undefined' ? 'system' : readStored())
const systemDark = ref<boolean>(media?.matches ?? false)
media?.addEventListener('change', (e) => {
  systemDark.value = e.matches
})

const resolved = computed<'light' | 'dark'>(() => {
  if (preference.value === 'system')
    return systemDark.value ? 'dark' : 'light'
  return preference.value
})

function apply(mode: 'light' | 'dark'): void {
  if (typeof document === 'undefined')
    return
  document.documentElement.classList.toggle('dark', mode === 'dark')
}

watch(resolved, apply, { immediate: true })
watch(preference, writeStored)

export function useTheme() {
  return {
    preference,
    resolved,
    setTheme: (value: ThemePreference) => {
      preference.value = value
    },
  }
}
