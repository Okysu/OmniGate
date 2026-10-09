<script setup lang="ts">
// 插件 section of the channel form. Create: pick an enabled plugin with an
// approved version and a version (newest first). Edit: show the pinned plugin
// and allow switching to another approved version of the same plugin
// (upgrade / rollback; the server runs migrateConfig).
import type { PluginFormState } from './pluginForm'
import type { Channel, Plugin, PluginManifest, PluginVersionSummary } from '@/lib/types'
import { computed, onMounted, ref } from 'vue'
import { Blocks, Loader2, Workflow } from '@lucide/vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FormField from '@/components/FormField.vue'
import { Badge } from '@/components/ui/badge'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { initConfigValues, initSecretValues } from '@/lib/configSchema'
import { errorMessage } from '@/lib/api'
import { pluginsApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { isChannelPlugin } from '@/lib/customMeters'
import { CUSTOM_PROTOCOL_REJECTS, isCustomProtocol, protocolInfo } from '@/lib/pluginProtocol'
import { compareSemver } from '@/lib/semver'
import PluginConfigFields from './PluginConfigFields.vue'
import { pluginVersionChanged } from './pluginForm'

const state = defineModel<PluginFormState>('state', { required: true })
const props = defineProps<{
  /** Channel being edited; null = create. */
  channel: Channel | null
  errors: Record<string, string>
}>()
const emit = defineEmits<{ manifest: [m: PluginManifest] }>()

const plugins = ref<Plugin[]>([])
const versions = ref<PluginVersionSummary[]>([])
const listError = ref<string | null>(null)

const isEdit = computed(() => props.channel !== null)
const selectedPlugin = computed(() => plugins.value.find(p => p.id === state.value.pluginId) ?? null)
const approvedVersions = computed(() => versions.value.filter(v => v.approval === 'approved'))
/** Edit: approved versions plus the pinned one (should it no longer be approved). */
const versionOptions = computed(() => {
  const list = [...approvedVersions.value]
  const pinned = state.value.originalVersionId
  if (pinned && !list.some(v => v.id === pinned)) {
    const v = versions.value.find(x => x.id === pinned)
    if (v)
      list.push(v)
  }
  return list.sort((a, b) => compareSemver(b.version, a.version))
})

async function loadManifest(versionId: string, fromChannel: boolean) {
  const pid = state.value.pluginId
  if (!pid)
    return
  const s = state.value
  s.loading = true
  s.error = null
  try {
    const v = await pluginsApi.version(pid, versionId)
    if (s.versionId !== versionId)
      return // a newer selection won
    const m = v.manifest
    if (!m) {
      s.error = '无法读取该版本的 manifest'
      return
    }
    s.manifest = m
    s.config = initConfigValues(m.configSchema, fromChannel ? props.channel?.pluginConfig : null)
    s.secrets = initSecretValues(m.configSchema, props.channel?.secretFields)
    s.configTouched = false
    emit('manifest', m)
  }
  catch (err) {
    s.error = errorMessage(err)
  }
  finally {
    s.loading = false
  }
}

async function loadVersions(pluginId: string): Promise<void> {
  versions.value = (await pluginsApi.get(pluginId)).versions ?? []
}

async function selectPlugin(id: string) {
  const s = state.value
  s.pluginId = id
  s.versionId = null
  s.manifest = null
  s.error = null
  s.loading = true
  try {
    await loadVersions(id)
    const latest = approvedVersions.value[0]
    if (!latest) {
      s.error = '该插件没有已批准的版本'
      return
    }
    s.versionId = latest.id
    await loadManifest(latest.id, false)
  }
  catch (err) {
    s.error = errorMessage(err)
  }
  finally {
    s.loading = false
  }
}

function selectVersionCreate(id: string) {
  state.value.versionId = id
  void loadManifest(id, false)
}

onMounted(async () => {
  const s = state.value
  if (props.channel) {
    const ref = props.channel.plugin
    if (!ref)
      return
    s.pluginId = ref.id
    s.versionId = ref.versionId
    s.originalVersionId = ref.versionId
    try {
      await loadVersions(ref.id)
    }
    catch (err) {
      listError.value = errorMessage(err)
    }
    await loadManifest(ref.versionId, true)
    return
  }
  try {
    const all = (await pluginsApi.list()).items
    // Billing-only plugins (phase9 §3) cannot back a channel.
    plugins.value = all.filter(p => p.status === 'enabled' && p.latest !== null && isChannelPlugin(p))
    const preferred = plugins.value.find(p => p.key === 'builtin.openai') ?? plugins.value[0]
    if (preferred)
      await selectPlugin(preferred.id)
    else
      listError.value = '没有可用的插件（需要已启用且有已批准版本的插件）'
  }
  catch (err) {
    listError.value = errorMessage(err)
  }
})

// ---- edit: version switch with confirmation ----
const switchTarget = ref<PluginVersionSummary | null>(null)
const switchOpen = ref(false)
const pinnedVersion = computed(() => versions.value.find(v => v.id === state.value.originalVersionId) ?? null)
const switchKind = computed<'upgrade' | 'rollback' | 'same'>(() => {
  const from = pinnedVersion.value?.version ?? props.channel?.plugin?.version ?? ''
  const to = switchTarget.value?.version ?? ''
  const c = compareSemver(to, from)
  return c > 0 ? 'upgrade' : c < 0 ? 'rollback' : 'same'
})

function requestVersion(id: string) {
  if (id === state.value.versionId)
    return
  if (id === state.value.originalVersionId) {
    // Back to the pinned version: no confirmation needed.
    state.value.versionId = id
    void loadManifest(id, true)
    return
  }
  switchTarget.value = versions.value.find(v => v.id === id) ?? null
  switchOpen.value = true
}
function confirmSwitch() {
  const t = switchTarget.value
  switchOpen.value = false
  if (!t)
    return
  state.value.versionId = t.id
  void loadManifest(t.id, true)
}

const switching = computed(() => pluginVersionChanged(state.value))
/** phase9 §2: the selected version implements a custom upstream protocol. */
const customProtocol = computed(() => isCustomProtocol(state.value.manifest) || (!state.value.manifest && isCustomProtocol(selectedPlugin.value)))
const currentVersionLabel = computed(() => versions.value.find(v => v.id === state.value.versionId)?.version ?? props.channel?.plugin?.version ?? '')
</script>

<template>
  <section class="space-y-4">
    <h3 class="flex items-center gap-1.5 text-sm font-semibold">
      <Blocks class="size-4" />
      插件
    </h3>

    <p v-if="listError" class="text-destructive text-sm" role="alert">
      {{ listError }}
    </p>

    <!-- create: plugin + version -->
    <template v-if="!isEdit">
      <div class="grid gap-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,10rem)]">
        <FormField label="插件" for="ch-plugin" required :error="errors.pluginVersionId" class="min-w-0">
          <Select :model-value="state.pluginId ?? undefined" @update:model-value="(v) => v && selectPlugin(String(v))">
            <SelectTrigger id="ch-plugin" class="w-full">
              <SelectValue placeholder="选择插件" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="p in plugins" :key="p.id" :value="p.id">
                {{ p.name }}
                <span class="text-muted-foreground text-xs">· {{ protocolInfo(p).label }}</span>
              </SelectItem>
            </SelectContent>
          </Select>
        </FormField>
        <FormField label="版本" for="ch-plugin-version" required class="min-w-0">
          <Select :model-value="state.versionId ?? undefined" :disabled="!approvedVersions.length" @update:model-value="(v) => v && selectVersionCreate(String(v))">
            <SelectTrigger id="ch-plugin-version" class="w-full font-mono">
              <SelectValue placeholder="—" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="(v, i) in approvedVersions" :key="v.id" :value="v.id" class="font-mono">
                v{{ v.version }}{{ i === 0 ? ' · 最新' : '' }}
              </SelectItem>
            </SelectContent>
          </Select>
        </FormField>
      </div>
      <p v-if="selectedPlugin" class="text-muted-foreground -mt-2 text-xs">
        {{ protocolInfo(state.manifest ?? selectedPlugin).label }}<template v-if="selectedPlugin.description">
          · {{ selectedPlugin.description }}
        </template>
      </p>
    </template>

    <!-- edit -->
    <template v-else-if="channel?.plugin">
      <div class="flex flex-wrap items-center gap-2 rounded-lg border p-3">
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium">
            {{ channel.plugin.name }}
          </p>
          <p class="text-muted-foreground font-mono text-xs">
            {{ channel.plugin.key }} · 固定在 v{{ channel.plugin.version }}
          </p>
        </div>
        <FormField label="版本" for="ch-plugin-version" class="w-40" :error="errors.pluginVersionId">
          <Select :model-value="state.versionId ?? undefined" :disabled="versionOptions.length < 2" @update:model-value="(v) => v && requestVersion(String(v))">
            <SelectTrigger id="ch-plugin-version" class="w-full font-mono">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="v in versionOptions" :key="v.id" :value="v.id" class="font-mono">
                v{{ v.version }}{{ v.id === state.originalVersionId ? '（当前）' : '' }}{{ v.approval !== 'approved' ? '（未批准）' : '' }}
              </SelectItem>
            </SelectContent>
          </Select>
        </FormField>
      </div>
      <div v-if="switching" class="rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 text-sm">
        保存后切换到 <span class="font-mono">v{{ currentVersionLabel }}</span>。未修改下方配置时，由服务端用新版本的 migrateConfig（如有）迁移现有配置；修改配置后则以表单内容整体替换。
      </div>
    </template>
    <p v-else class="text-muted-foreground rounded-lg border border-dashed p-3 text-sm">
      该渠道创建于插件系统之前，使用「{{ channel?.type }}」类型的内置协议。保存后会自动绑定对应的内置插件。
    </p>

    <div v-if="customProtocol" class="space-y-1 rounded-lg border border-violet-500/40 bg-violet-500/5 p-3 text-sm" data-testid="custom-protocol-channel-note">
      <p class="flex items-center gap-1.5 font-medium">
        <Workflow class="size-4" />
        自定义协议插件
      </p>
      <p class="text-muted-foreground">
        该渠道接受 OpenAI Chat、Anthropic Messages 与 OpenAI Responses 客户端（网关先转换为 Chat Completions，再由插件构造上游请求并解析响应）；{{ CUSTOM_PROTOCOL_REJECTS.join('、') }}接口不会路由到此渠道。
      </p>
    </div>

    <p v-if="state.error" class="text-destructive text-sm" role="alert">
      {{ state.error }}
    </p>
    <div v-if="state.loading" class="text-muted-foreground flex items-center gap-2 text-sm">
      <Loader2 class="size-4 animate-spin" />
      正在加载插件信息…
    </div>
    <template v-else-if="state.manifest">
      <div v-if="state.manifest.configSchema?.properties && Object.keys(state.manifest.configSchema.properties).length" class="space-y-2">
        <p class="text-sm font-medium">
          插件配置
          <Badge variant="outline" class="ml-1 font-mono font-normal">
            v{{ state.manifest.version }}
          </Badge>
        </p>
        <PluginConfigFields
          v-model:values="state.config"
          v-model:secrets="state.secrets"
          :schema="state.manifest.configSchema"
          :secret-state="channel?.secretFields"
          :errors="errors"
          @touch="state.configTouched = true"
        />
      </div>
    </template>

    <ConfirmDialog
      v-model:open="switchOpen"
      :title="switchKind === 'rollback' ? `回滚到 v${switchTarget?.version}？` : `升级到 v${switchTarget?.version}？`"
      :confirm-text="switchKind === 'rollback' ? '回滚' : '升级'"
      @confirm="confirmSwitch"
    >
      <p>
        {{ `当前固定在 v${pinnedVersion?.version ?? channel?.plugin?.version ?? ''}，保存渠道后切换到 v${switchTarget?.version ?? ''}${switchTarget ? `（发布于 ${formatDateTime(switchTarget.publishedAt)}）` : ''}。` }}
      </p>
      <p v-if="switchKind === 'rollback'">
        回滚是安全的：已发布的版本不可变，回滚只是把渠道重新固定到旧版本，随时可以再升级回来。
      </p>
      <p>
        保存时，如果目标版本导出了 <code class="font-mono">migrateConfig</code>，网关会先用它迁移现有插件配置，再按目标版本的配置项校验；目标版本不再声明的插件密钥会被删除。迁移失败时保存会被拒绝，渠道保持原版本。
      </p>
    </ConfirmDialog>
  </section>
</template>
