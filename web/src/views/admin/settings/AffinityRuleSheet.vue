<script setup lang="ts">
import type { RuleForm } from '@/lib/affinity'
import type { AffinityMode, AffinityRule, AffinityRuleMode } from '@/lib/types'
import { computed, reactive, ref, watch } from 'vue'
import { Plus, Trash2 } from '@lucide/vue'
import FormField from '@/components/FormField.vue'
import MultiSelect from '@/components/MultiSelect.vue'
import TagsInput from '@/components/TagsInput.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import {
  effectiveMode,
  formToRule,
  LIMITS,
  MODE_DESCRIPTIONS,
  MODE_LABELS,
  RULE_MODE_LABELS,
  RULE_MODES,
  ruleToForm,
  validateRule,
} from '@/lib/affinity'
import { clientOptions } from '@/lib/clients'
import { useClientsStore } from '@/stores/clients'

const props = defineProps<{
  /** Rule being edited (null = new). */
  rule: AffinityRule | null
  /** Names of the other rules (unique names). */
  otherNames: string[]
  globalMode: AffinityMode
  defaultTtl: number
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ save: [rule: AffinityRule] }>()

const clients = useClientsStore()
/** Known clients, plus ids saved in the rule that this backend no longer lists. */
const clientChoices = computed(() => {
  const opts = clientOptions(clients.list)
  for (const id of form.clients) {
    if (!opts.some(o => o.value === id))
      opts.push({ value: id, label: id, hint: '未知的客户端标识' })
  }
  return opts
})

const form = reactive<RuleForm>(blank())
const errors = ref<Record<string, string>>({})

function blank(): RuleForm {
  return { name: '', modelRegex: '', pathRegex: '', userAgent: [], keySources: [{ type: 'gjson', value: '' }], valueRegex: '', ttl: '', headers: [], keepOrigin: true,
    mode: 'inherit', skipRetry: false, includeGroup: true, includeModel: false, includeRule: true, injectCacheKey: false, injectHeader: '', clients: [] }
}

watch(open, (v) => {
  if (!v)
    return
  Object.assign(form, props.rule ? ruleToForm(props.rule) : blank())
  errors.value = {}
  void clients.ensureLoaded()
})

const isEdit = computed(() => props.rule !== null)
const mode = computed(() => effectiveMode(formToRule(form), props.globalMode))
const modeKey = computed<string>({
  get: () => (form.mode === '' ? 'legacy' : form.mode),
  set: v => (form.mode = (v === 'legacy' ? '' : v) as AffinityRuleMode),
})

function addSource() {
  if (form.keySources.length < LIMITS.keySources)
    form.keySources.push({ type: 'request_header', value: '' })
}
function removeSource(i: number) {
  form.keySources.splice(i, 1)
}
function setSourceType(i: number, v: unknown) {
  if (v === 'gjson' || v === 'request_header' || v === 'anchor')
    form.keySources[i]!.type = v
}

function submit() {
  const r = formToRule(form)
  const e = validateRule(r)
  if (props.otherNames.includes(r.name))
    e.name = `规则名称「${r.name}」已存在`
  // pass_headers errors are reported on the headers field.
  const op = Object.keys(e).find(k => k.startsWith('param_override_template'))
  if (op)
    e.headers = e[op]!
  errors.value = e
  if (Object.keys(e).length)
    return
  emit('save', r)
  open.value = false
}
</script>

<template>
  <Sheet v-model:open="open">
    <SheetContent class="w-full gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-2xl" data-testid="affinity-rule-sheet">
      <SheetHeader class="border-b">
        <SheetTitle>{{ isEdit ? `编辑规则：${rule?.name}` : '添加规则' }}</SheetTitle>
        <SheetDescription>
          规则按顺序匹配，第一条命中且取到会话标识的规则生效。保存规则后还需要在卡片底部点击「保存」才会生效。
        </SheetDescription>
      </SheetHeader>

      <form id="affinity-rule-form" class="flex-1 space-y-6 overflow-y-auto p-4" novalidate @submit.prevent="submit">
        <section class="space-y-4">
          <h3 class="text-sm font-semibold">
            匹配条件
          </h3>
          <FormField label="名称" for="aff-name" required :error="errors.name" hint="用于统计与清空缓存；勾选「规则」作用域时也是绑定键的一部分。">
            <Input id="aff-name" v-model="form.name" :maxlength="LIMITS.name" placeholder="例如：codex cli trace" :aria-invalid="!!errors.name" />
          </FormField>
          <div class="grid gap-4 sm:grid-cols-2">
            <FormField label="模型正则" for="aff-model" :error="errors.model_regex" hint="客户端请求的逻辑模型，每行一个，任一匹配即可；留空 = 任意模型。">
              <Textarea id="aff-model" v-model="form.modelRegex" rows="3" class="font-mono text-xs" placeholder="^gpt-.*$" :aria-invalid="!!errors.model_regex" />
            </FormField>
            <FormField label="路径正则" for="aff-path" :error="errors.path_regex" hint="请求路径，如 /v1/responses、/v1/messages、/v1/chat/completions；留空 = 任意路径。">
              <Textarea id="aff-path" v-model="form.pathRegex" rows="3" class="font-mono text-xs" placeholder="^/v1/responses" :aria-invalid="!!errors.path_regex" />
            </FormField>
          </div>
          <FormField label="User-Agent 包含" for="aff-ua" :error="errors.user_agent_include" hint="可选，不区分大小写，包含任一项即可；留空 = 不限制。">
            <TagsInput id="aff-ua" v-model="form.userAgent" placeholder="例如 codex，回车添加" />
          </FormField>
          <FormField label="客户端" for="aff-clients" :error="errors.client_include" data-testid="aff-clients">
            <MultiSelect id="aff-clients" v-model="form.clients" :options="clientChoices" :loading="!clients.items" placeholder="搜索客户端…" empty-label="任意客户端" />
            <template #hint>
              OmniGate 扩展：只对网关识别出的这些客户端生效（按请求头与 User-Agent 识别，见请求日志的「客户端」列）；不选 = 任意客户端。
              「未知」表示未能识别的请求。new-api 不支持此字段。
            </template>
          </FormField>
        </section>

        <Separator />

        <section class="space-y-3">
          <div class="flex items-center justify-between gap-2">
            <h3 class="text-sm font-semibold">
              Key 来源
            </h3>
            <Button type="button" variant="outline" size="xs" :disabled="form.keySources.length >= LIMITS.keySources" @click="addSource">
              <Plus />
              添加来源
            </Button>
          </div>
          <p class="text-muted-foreground text-xs">
            按顺序取第一个非空值作为会话标识（去除首尾空白）；都取不到时本规则不生效，继续匹配下一条规则。原始值只在内存中参与哈希，不写入日志。
          </p>
          <p class="text-muted-foreground text-xs">
            「对话锚点」（OmniGate 扩展）适合放在最后兜底：客户端不发送任何会话标识时，用开头的 system / developer 指令加第一条用户消息计算指纹。
            同一段对话后续轮次只是追加消息，指纹不变；不同对话的第一条用户消息不同，指纹也不同。支持 Chat、Responses 与 Anthropic Messages，multipart 请求取不到。
          </p>
          <p v-if="errors.key_sources" class="text-destructive text-xs" role="alert">
            {{ errors.key_sources }}
          </p>
          <div v-for="(ks, i) in form.keySources" :key="i" class="space-y-1" data-testid="aff-key-source">
            <div class="flex gap-2">
              <Select :model-value="ks.type" @update:model-value="(v) => setSourceType(i, v)">
                <SelectTrigger class="w-36 shrink-0" :aria-label="`第 ${i + 1} 个来源的类型`">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="gjson">
                    gjson（请求体）
                  </SelectItem>
                  <SelectItem value="request_header">
                    请求头
                  </SelectItem>
                  <SelectItem value="anchor">
                    对话锚点
                  </SelectItem>
                </SelectContent>
              </Select>
              <p v-if="ks.type === 'anchor'" class="text-muted-foreground bg-muted/40 flex min-h-9 min-w-0 flex-1 items-center rounded-md border border-dashed px-3 text-xs">
                由请求体自动计算，无需填写
              </p>
              <Input
                v-else
                v-model="ks.value"
                class="min-w-0 font-mono text-xs"
                :placeholder="ks.type === 'gjson' ? 'prompt_cache_key / metadata.user_id' : 'Session_id'"
                :aria-label="`第 ${i + 1} 个来源的${ks.type === 'gjson' ? ' JSON 路径' : '请求头名称'}`"
                :aria-invalid="!!(errors[`key_sources[${i}].path`] || errors[`key_sources[${i}].key`] || errors[`key_sources[${i}].type`])"
              />
              <Button type="button" variant="ghost" size="icon" :disabled="form.keySources.length <= 1" :aria-label="`删除第 ${i + 1} 个来源`" @click="removeSource(i)">
                <Trash2 />
              </Button>
            </div>
            <p v-if="errors[`key_sources[${i}].path`] || errors[`key_sources[${i}].key`] || errors[`key_sources[${i}].type`]" class="text-destructive text-xs" role="alert">
              {{ errors[`key_sources[${i}].path`] || errors[`key_sources[${i}].key`] || errors[`key_sources[${i}].type`] }}
            </p>
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            <FormField label="值正则" for="aff-value" :error="errors.value_regex" hint="可选：会话标识必须匹配，否则本规则不生效。">
              <Input id="aff-value" v-model="form.valueRegex" class="font-mono text-xs" placeholder="留空 = 不限制" :aria-invalid="!!errors.value_regex" />
            </FormField>
            <FormField label="TTL（秒）" for="aff-ttl" :error="errors.ttl_seconds" :hint="`绑定闲置多久后失效，每次命中都会续期；留空或 0 = 全局默认（${defaultTtl} 秒）。`">
              <Input id="aff-ttl" v-model="form.ttl" type="number" min="0" :max="LIMITS.ttl" step="1" class="tabular-nums" :placeholder="`全局默认（${defaultTtl}）`" :aria-invalid="!!errors.ttl_seconds" />
            </FormField>
          </div>
        </section>

        <Separator />

        <section class="space-y-4">
          <h3 class="text-sm font-semibold">
            会话保持
          </h3>
          <FormField label="模式" for="aff-mode" :error="errors.session_mode">
            <Select v-model="modeKey">
              <SelectTrigger id="aff-mode" class="w-full sm:w-80" data-testid="aff-mode">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem v-for="m in RULE_MODES" :key="m || 'legacy'" :value="m || 'legacy'">
                  {{ RULE_MODE_LABELS[m] }}<template v-if="m === 'inherit'">
                    （当前：{{ MODE_LABELS[globalMode] }}）
                  </template>
                </SelectItem>
              </SelectContent>
            </Select>
            <template #hint>
              生效：<strong class="text-foreground">{{ MODE_LABELS[mode] }}</strong>。{{ MODE_DESCRIPTIONS[mode] }}
            </template>
          </FormField>
          <div v-if="form.mode === ''" class="flex items-start justify-between gap-4 rounded-lg border p-3">
            <div class="space-y-1">
              <Label for="aff-skip">失败不重试（skip_retry_on_failure）</Label>
              <p class="text-muted-foreground text-xs">
                new-api 的旧字段：开启 = 严格保持；关闭 = 继承全局模式。
              </p>
            </div>
            <Switch id="aff-skip" v-model="form.skipRetry" />
          </div>
        </section>

        <Separator />

        <section class="space-y-4">
          <h3 class="text-sm font-semibold">
            透传请求头（pass_headers）
          </h3>
          <FormField for="aff-headers" :error="errors.headers">
            <TagsInput id="aff-headers" v-model="form.headers" placeholder="例如 Session_id，回车或逗号添加" />
            <template #hint>
              规则生效时（任何模式，包括「不保持」），把这些客户端请求头原样转发给上游，每次尝试、每个渠道都会带上，最多 {{ LIMITS.passHeaders }} 个。
              Authorization、x-api-key、Cookie、Host、Content-Length 等由网关管理的请求头不能透传。
            </template>
          </FormField>
          <div class="flex items-start justify-between gap-4 rounded-lg border p-3">
            <div class="space-y-1">
              <Label for="aff-keep">保留渠道配置的值（keep_origin）</Label>
              <p class="text-muted-foreground text-xs">
                开启：渠道（或插件）已显式设置的同名请求头保持渠道的值；网关默认的 User-Agent 不算渠道配置，会被客户端的值替换。关闭：一律使用客户端的值。
              </p>
            </div>
            <Switch id="aff-keep" v-model="form.keepOrigin" />
          </div>
        </section>

        <Separator />

        <section class="space-y-4">
          <div class="space-y-1">
            <h3 class="text-sm font-semibold">
              上游会话标识
            </h3>
            <p class="text-muted-foreground text-xs">
              OmniGate 扩展：让上游号池也能认出同一段对话，把它留在同一个上游账号上。只作用于 OpenAI 格式（Chat / Responses，含协议转换后）的上游请求；
              值由绑定键派生（按用户、按对话哈希），从不发送原始会话标识；客户端已带上的值永远不会被覆盖。
            </p>
          </div>
          <div class="flex items-start justify-between gap-4 rounded-lg border p-3">
            <div class="space-y-1">
              <Label for="aff-inject-pck">补全 prompt_cache_key</Label>
              <p class="text-muted-foreground text-xs">
                请求体没有非空的 prompt_cache_key 时加入 <code class="font-mono">og-</code> 开头的稳定值（OpenAI 标准字段，上游据此复用提示词缓存）。
              </p>
            </div>
            <Switch id="aff-inject-pck" v-model="form.injectCacheKey" data-testid="aff-inject-pck" />
          </div>
          <FormField label="补全会话请求头" for="aff-inject-header" :error="errors.inject_session_header">
            <Input id="aff-inject-header" v-model="form.injectHeader" class="font-mono text-xs sm:w-80" placeholder="Session_id" :maxlength="64" :aria-invalid="!!errors.inject_session_header" data-testid="aff-inject-header" />
            <template #hint>
              上游请求没有这个请求头（客户端未透传、渠道未配置）时，设置为该对话稳定的 UUID。Codex 的 <code class="font-mono">Session_id</code>
              是 OpenAI 格式号池默认识别的会话标识，填它即可让普通 Chat 客户端、Claude Code 调用 GPT 等请求在上游也保持会话；留空 = 不补全。
            </template>
          </FormField>
        </section>

        <Separator />

        <section class="space-y-3">
          <h3 class="text-sm font-semibold">
            绑定作用域
          </h3>
          <p class="text-muted-foreground text-xs">
            绑定键总是包含用户本身（会话永远不会影响其他用户的路由），再按下面的开关加入其他部分。
          </p>
          <div class="grid gap-2 sm:grid-cols-3">
            <label class="flex items-start justify-between gap-3 rounded-lg border p-3">
              <span class="space-y-0.5">
                <span class="block text-sm font-medium">分组</span>
                <span class="text-muted-foreground block text-xs">用户组</span>
              </span>
              <Switch v-model="form.includeGroup" aria-label="绑定键包含用户组" />
            </label>
            <label class="flex items-start justify-between gap-3 rounded-lg border p-3">
              <span class="space-y-0.5">
                <span class="block text-sm font-medium">模型</span>
                <span class="text-muted-foreground block text-xs">请求的模型</span>
              </span>
              <Switch v-model="form.includeModel" aria-label="绑定键包含模型" />
            </label>
            <label class="flex items-start justify-between gap-3 rounded-lg border p-3">
              <span class="space-y-0.5">
                <span class="block text-sm font-medium">规则</span>
                <span class="text-muted-foreground block text-xs">规则名称</span>
              </span>
              <Switch v-model="form.includeRule" aria-label="绑定键包含规则名称" />
            </label>
          </div>
        </section>
      </form>

      <SheetFooter class="flex-row justify-end gap-2 border-t">
        <Button type="button" variant="outline" @click="open = false">
          取消
        </Button>
        <Button type="submit" form="affinity-rule-form" data-testid="aff-rule-submit">
          {{ isEdit ? '更新规则' : '添加规则' }}
        </Button>
      </SheetFooter>
    </SheetContent>
  </Sheet>
</template>
