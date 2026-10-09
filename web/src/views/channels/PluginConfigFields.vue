<script setup lang="ts">
// Dynamic form for a plugin's configSchema (string / number / integer / boolean,
// enum → select) plus the write-only 插件密钥 group for `x-secret` fields.
import type { ConfigField, ConfigValues, SecretValues } from '@/lib/configSchema'
import type { ConfigSchema, SecretState } from '@/lib/types'
import { computed } from 'vue'
import { ExternalLink, KeyRound } from '@lucide/vue'
import FormField from '@/components/FormField.vue'
import { safeHttpsUrl } from '@/components/plugin-ui/bindings'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { groupFields, schemaFields } from '@/lib/configSchema'

const values = defineModel<ConfigValues>('values', { required: true })
const secrets = defineModel<SecretValues>('secrets', { required: true })
const props = defineProps<{
  schema: ConfigSchema | null | undefined
  /** Server-side secret state (edit). */
  secretState?: Record<string, SecretState> | null
  errors: Record<string, string>
  /** Hide the non-secret config (e.g. while a version switch lets the server migrate it). */
  hideConfig?: boolean
}>()
const emit = defineEmits<{ touch: [] }>()

const fields = computed(() => schemaFields(props.schema))
const configGroups = computed(() => groupFields(fields.value.filter(f => !f.secret)))
const secretFields = computed(() => fields.value.filter(f => f.secret))

function fid(f: ConfigField): string {
  return `plugin-cfg-${f.name}`
}
function label(f: ConfigField): string {
  return f.prop.title || f.name
}
function placeholder(f: ConfigField): string | undefined {
  if (f.prop.default !== undefined && f.prop.default !== null)
    return `默认：${String(f.prop.default)}`
  return undefined
}
function rangeHint(f: ConfigField): string {
  const p = f.prop
  const parts: string[] = []
  if (p.minimum != null || p.maximum != null)
    parts.push(p.minimum != null && p.maximum != null ? `范围 ${p.minimum}–${p.maximum}` : p.minimum != null ? `≥ ${p.minimum}` : `≤ ${p.maximum}`)
  if (p.type === 'integer')
    parts.push('整数')
  if (p.minLength != null || p.maxLength != null)
    parts.push(p.minLength != null && p.maxLength != null ? `${p.minLength}–${p.maxLength} 个字符` : p.minLength != null ? `至少 ${p.minLength} 个字符` : `最多 ${p.maxLength} 个字符`)
  return parts.join('，')
}
function hint(f: ConfigField): string | undefined {
  const text = [f.prop.description, rangeHint(f)].filter(Boolean).join('；')
  return text || undefined
}
function helpLink(f: ConfigField): string | null {
  return safeHttpsUrl(f.prop['x-help'])
}
function helpText(f: ConfigField): string | null {
  const h = f.prop['x-help']
  return h && !safeHttpsUrl(h) ? h : null
}

function setValue(name: string, v: string | boolean) {
  values.value[name] = v
  emit('touch')
}
function setReplace(name: string, v: boolean) {
  const s = secrets.value[name]
  if (!s)
    return
  s.replace = v
  if (!v)
    s.value = ''
}
</script>

<template>
  <div class="space-y-4">
    <template v-if="!hideConfig">
      <div v-for="g in configGroups" :key="g.group" class="space-y-4">
        <p v-if="g.group" class="text-muted-foreground text-xs font-medium">
          {{ g.group }}
        </p>
        <template v-for="f in g.fields" :key="f.name">
          <!-- boolean -->
          <div v-if="f.prop.type === 'boolean'" class="flex items-start justify-between gap-4 rounded-lg border p-3">
            <div class="space-y-1">
              <Label :for="fid(f)">{{ label(f) }}<span v-if="f.required" class="text-destructive" aria-hidden="true">*</span></Label>
              <p v-if="hint(f)" class="text-muted-foreground text-xs">
                {{ hint(f) }}
              </p>
              <p v-if="errors[`pluginConfig.${f.name}`]" class="text-destructive text-xs" role="alert">
                {{ errors[`pluginConfig.${f.name}`] }}
              </p>
            </div>
            <Switch :id="fid(f)" :model-value="values[f.name] === true" @update:model-value="(v: boolean) => setValue(f.name, v)" />
          </div>

          <FormField v-else :label="label(f)" :for="fid(f)" :required="f.required" :error="errors[`pluginConfig.${f.name}`]" :hint="hint(f)">
            <!-- enum -->
            <Select
              v-if="f.prop.enum?.length"
              :model-value="String(values[f.name] ?? '')"
              @update:model-value="(v) => setValue(f.name, String(v ?? ''))"
            >
              <SelectTrigger :id="fid(f)" class="w-full" :aria-invalid="!!errors[`pluginConfig.${f.name}`]">
                <SelectValue :placeholder="placeholder(f) ?? '请选择'" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem v-for="opt in f.prop.enum" :key="String(opt)" :value="String(opt)">
                  {{ String(opt) }}
                </SelectItem>
              </SelectContent>
            </Select>
            <!-- number / integer / string -->
            <Input
              v-else
              :id="fid(f)"
              :model-value="String(values[f.name] ?? '')"
              :inputmode="f.prop.type === 'string' ? undefined : f.prop.type === 'integer' ? 'numeric' : 'decimal'"
              :placeholder="placeholder(f)"
              :maxlength="f.prop.maxLength"
              :class="f.prop.type === 'string' ? '' : 'tabular-nums'"
              :aria-invalid="!!errors[`pluginConfig.${f.name}`]"
              @update:model-value="(v) => setValue(f.name, String(v))"
            />
            <template v-if="helpLink(f) || helpText(f)" #hint>
              {{ hint(f) }}
              <a v-if="helpLink(f)" :href="helpLink(f)!" target="_blank" rel="noopener noreferrer" class="text-primary inline-flex items-center gap-0.5 hover:underline">
                帮助<ExternalLink class="size-3" />
              </a>
              <span v-else>{{ helpText(f) }}</span>
            </template>
          </FormField>
        </template>
      </div>
    </template>

    <!-- secrets -->
    <div v-if="secretFields.length" class="space-y-3">
      <p class="text-muted-foreground flex items-center gap-1.5 text-xs font-medium">
        <KeyRound class="size-3.5" />
        插件密钥
      </p>
      <template v-for="f in secretFields" :key="f.name">
        <div v-if="secretState?.[f.name]?.set && !secrets[f.name]?.replace" class="flex flex-wrap items-center justify-between gap-2 rounded-lg border p-3">
          <div class="min-w-0 text-sm">
            <p class="font-medium">
              {{ label(f) }}
            </p>
            <p class="text-muted-foreground text-xs">
              已设置 <span class="font-mono">{{ secretState?.[f.name]?.hint ?? '' }}</span>
            </p>
          </div>
          <label class="flex items-center gap-2 text-sm">
            <Switch :model-value="false" @update:model-value="(v: boolean) => setReplace(f.name, v)" />
            更换
          </label>
        </div>
        <FormField
          v-else-if="secrets[f.name]"
          :label="label(f)"
          :for="fid(f)"
          :required="f.required && !secretState?.[f.name]?.set"
          :error="errors[`secrets.${f.name}`]"
          :hint="[f.prop.description, secretState?.[f.name]?.set ? '留空表示保留原值' : '加密存储，保存后不再显示明文'].filter(Boolean).join('；')"
        >
          <div class="flex items-center gap-2">
            <Input
              :id="fid(f)"
              v-model="secrets[f.name]!.value"
              type="password"
              autocomplete="new-password"
              class="font-mono text-xs"
              :aria-invalid="!!errors[`secrets.${f.name}`]"
            />
            <button
              v-if="secretState?.[f.name]?.set"
              type="button"
              class="text-muted-foreground shrink-0 text-xs hover:underline"
              @click="setReplace(f.name, false)"
            >
              取消更换
            </button>
          </div>
        </FormField>
      </template>
    </div>
  </div>
</template>
