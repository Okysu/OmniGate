<script setup lang="ts">
import { computed, ref } from 'vue'
import { ChevronDown, Plus, Search, X } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'

export interface MultiSelectOption {
  value: string
  label: string
  hint?: string
}

const props = withDefaults(defineProps<{
  options: MultiSelectOption[]
  placeholder?: string
  /** Shown when nothing is selected, in the trigger. */
  emptyLabel?: string
  /** Let the user add values that are not in `options`. */
  allowCustom?: boolean
  loading?: boolean
  disabled?: boolean
  id?: string
}>(), {
  placeholder: '搜索…',
  emptyLabel: '未选择',
  allowCustom: false,
  loading: false,
  disabled: false,
  id: undefined,
})

const model = defineModel<string[]>({ required: true })
const open = ref(false)
const query = ref('')

const labelOf = computed(() => new Map(props.options.map(o => [o.value, o.label])))
const selected = computed(() => new Set(model.value))

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  const list = q
    ? props.options.filter(o => o.label.toLowerCase().includes(q) || o.value.toLowerCase().includes(q) || o.hint?.toLowerCase().includes(q))
    : props.options
  return list.slice(0, 200)
})

const canAddCustom = computed(() => {
  const q = query.value.trim()
  return props.allowCustom && q !== '' && !labelOf.value.has(q) && !selected.value.has(q)
})

function toggle(value: string, on: boolean | 'indeterminate') {
  if (on === true && !selected.value.has(value))
    model.value = [...model.value, value]
  else if (on !== true)
    model.value = model.value.filter(v => v !== value)
}

function addCustom() {
  const q = query.value.trim()
  if (q && !selected.value.has(q))
    model.value = [...model.value, q]
  query.value = ''
}

function remove(value: string) {
  model.value = model.value.filter(v => v !== value)
}
</script>

<template>
  <div class="space-y-2">
    <Popover v-model:open="open">
      <PopoverTrigger as-child>
        <Button :id="id" type="button" variant="outline" class="w-full justify-between font-normal" :disabled="disabled">
          <span :class="model.length ? '' : 'text-muted-foreground'">
            {{ model.length ? `已选 ${model.length} 项` : emptyLabel }}
          </span>
          <ChevronDown class="text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" class="w-(--reka-popover-trigger-width) min-w-64 gap-2 p-2">
        <div class="relative">
          <Search class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
          <Input v-model="query" :placeholder="placeholder" class="h-8 pl-8" @keydown.enter.prevent="canAddCustom && addCustom()" />
        </div>
        <div class="max-h-64 overflow-y-auto">
          <p v-if="loading" class="text-muted-foreground px-2 py-3 text-center text-xs">
            加载中…
          </p>
          <template v-else>
            <button
              v-if="canAddCustom"
              type="button"
              class="hover:bg-muted flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm"
              @click="addCustom"
            >
              <Plus class="size-4" />
              添加「{{ query.trim() }}」
            </button>
            <label
              v-for="o in filtered"
              :key="o.value"
              class="hover:bg-muted flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5"
            >
              <Checkbox :model-value="selected.has(o.value)" @update:model-value="(v) => toggle(o.value, v)" />
              <span class="min-w-0 flex-1">
                <span class="block truncate text-sm">{{ o.label }}</span>
                <span v-if="o.hint" class="text-muted-foreground block truncate text-xs">{{ o.hint }}</span>
              </span>
            </label>
            <p v-if="filtered.length === 0 && !canAddCustom" class="text-muted-foreground px-2 py-3 text-center text-xs">
              没有可选项
            </p>
          </template>
        </div>
        <div v-if="model.length" class="flex justify-end border-t pt-2">
          <Button type="button" variant="ghost" size="xs" @click="model = []">
            清空选择
          </Button>
        </div>
      </PopoverContent>
    </Popover>
    <div v-if="model.length" class="flex flex-wrap gap-1.5">
      <Badge v-for="v in model" :key="v" variant="secondary" class="h-6 max-w-full gap-1 pr-1">
        <span class="truncate">{{ labelOf.get(v) ?? v }}</span>
        <button type="button" class="hover:bg-foreground/10 rounded-sm p-0.5" :aria-label="`移除 ${labelOf.get(v) ?? v}`" :disabled="disabled" @click="remove(v)">
          <X class="size-3" />
        </button>
      </Badge>
    </div>
  </div>
</template>
