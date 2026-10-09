<script setup lang="ts">
import { Monitor, Moon, Sun } from '@lucide/vue'
import type { ThemePreference } from '@/composables/useTheme'
import { useTheme } from '@/composables/useTheme'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

const { preference, resolved, setTheme } = useTheme()

const options: { value: ThemePreference, label: string }[] = [
  { value: 'light', label: '浅色' },
  { value: 'dark', label: '深色' },
  { value: 'system', label: '跟随系统' },
]

function onSelect(value: unknown) {
  if (value === 'light' || value === 'dark' || value === 'system')
    setTheme(value)
}
</script>

<template>
  <DropdownMenu>
    <DropdownMenuTrigger as-child>
      <Button variant="ghost" size="icon-sm" aria-label="切换主题">
        <Moon v-if="resolved === 'dark'" />
        <Sun v-else />
      </Button>
    </DropdownMenuTrigger>
    <DropdownMenuContent align="end" class="w-36">
      <DropdownMenuRadioGroup :model-value="preference" @update:model-value="onSelect">
        <DropdownMenuRadioItem v-for="o in options" :key="o.value" :value="o.value">
          <Sun v-if="o.value === 'light'" />
          <Moon v-else-if="o.value === 'dark'" />
          <Monitor v-else />
          {{ o.label }}
        </DropdownMenuRadioItem>
      </DropdownMenuRadioGroup>
    </DropdownMenuContent>
  </DropdownMenu>
</template>
