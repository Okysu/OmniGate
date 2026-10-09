<script setup lang="ts">
import { computed, ref } from 'vue'
import { Asterisk, Plus, X } from '@lucide/vue'
import SuggestInput from '@/components/SuggestInput.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { isGlob, MAX_MATCH_MODELS, modelsMatching } from '@/lib/routeForm'

/**
 * Model names or `*` globs for a rule's `match.models`. Suggests current logical
 * models and shows which of them each glob matches.
 */
const props = defineProps<{
  id?: string
  /** Current logical models (for suggestions and glob previews). */
  models: string[]
  invalid?: boolean
}>()
const model = defineModel<string[]>({ required: true })

const draft = ref('')

function add(raw = draft.value) {
  const parts = raw.split(/[\s,，]+/).map(s => s.trim()).filter(Boolean)
  const next = [...model.value]
  for (const p of parts) {
    if (!next.includes(p) && next.length < MAX_MATCH_MODELS)
      next.push(p)
  }
  model.value = next
  draft.value = ''
}

function remove(v: string) {
  model.value = model.value.filter(x => x !== v)
}

interface ChipInfo {
  value: string
  glob: boolean
  matches: string[]
  /** Exact name that is not a current model. */
  unknown: boolean
}

const chips = computed<ChipInfo[]>(() => model.value.map((v) => {
  const glob = isGlob(v)
  const matches = glob ? modelsMatching(v, props.models) : props.models.filter(m => m === v)
  return { value: v, glob, matches, unknown: !glob && matches.length === 0 && props.models.length > 0 }
}))

/** Every current model matched by any entry. */
const matchedAll = computed(() => {
  const set = new Set<string>()
  for (const c of chips.value) {
    for (const m of c.matches)
      set.add(m)
  }
  return props.models.filter(m => set.has(m))
})

const QUICK = ['*', 'gpt-*', 'claude-*']
const quick = computed(() => QUICK.filter(q => !model.value.includes(q)))
</script>

<template>
  <div class="space-y-2">
    <div class="flex gap-2">
      <SuggestInput
        :id="id"
        v-model="draft"
        :options="models"
        :exclude="model"
        placeholder="输入模型名或通配（如 gpt-*），回车添加"
        create-verb="添加"
        mono
        class="flex-1"
        :invalid="invalid"
        @select="add()"
        @enter="(e) => { e.preventDefault(); add() }"
      />
      <Button type="button" variant="outline" :disabled="!draft.trim()" @click="add()">
        <Plus />
        添加
      </Button>
    </div>
    <div v-if="quick.length" class="text-muted-foreground flex flex-wrap items-center gap-1.5 text-xs">
      快速添加：
      <button v-for="q in quick" :key="q" type="button" class="hover:bg-muted hover:text-foreground rounded border px-1.5 py-0.5 font-mono" @click="add(q)">
        {{ q }}
      </button>
    </div>
    <ul v-if="chips.length" class="space-y-1.5" aria-label="已添加的模型匹配">
      <li v-for="c in chips" :key="c.value" class="flex min-w-0 flex-wrap items-center gap-2 rounded-md border px-2 py-1.5">
        <Badge variant="secondary" class="h-6 max-w-full gap-1 pr-1 font-mono">
          <Asterisk v-if="c.glob" class="text-violet-600 dark:text-violet-400" aria-label="通配" />
          <span class="truncate">{{ c.value }}</span>
          <button type="button" class="hover:bg-foreground/10 rounded-sm p-0.5" :aria-label="`移除 ${c.value}`" @click="remove(c.value)">
            <X class="size-3" />
          </button>
        </Badge>
        <span v-if="c.glob" class="text-muted-foreground min-w-0 flex-1 truncate text-xs" :title="c.matches.join('\n')">
          <template v-if="c.matches.length">匹配当前 {{ c.matches.length }} 个模型：<span class="font-mono">{{ c.matches.slice(0, 6).join('、') }}</span><template v-if="c.matches.length > 6"> 等</template></template>
          <template v-else>当前没有匹配的模型（以后新增的模型仍会匹配）</template>
        </span>
        <span v-else-if="c.unknown" class="text-xs text-amber-700 dark:text-amber-400">当前没有渠道提供该模型</span>
      </li>
    </ul>
    <p v-if="chips.length && models.length" class="text-muted-foreground text-xs">
      共匹配当前 {{ matchedAll.length }} / {{ models.length }} 个模型。
    </p>
  </div>
</template>
