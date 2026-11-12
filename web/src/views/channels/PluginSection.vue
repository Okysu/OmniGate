<script setup lang="ts">
// 插件 section of the channel form. Create: pick an enabled plugin with an
// approved version. Edit also supports selecting a different plugin; only
// version changes within the original plugin reuse stored config / secrets.
import type { PluginFormState } from './pluginForm'
import type { Channel, Plugin, PluginManifest, PluginVersionSummary } from '@/lib/types'
import { computed, onMounted, ref } from 'vue'
import { Blocks, Loader2, Workflow } from '@lucide/vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FormField from '@/components/FormField.vue'
import { Badge } from '@/components/ui/badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { initConfigValues, initSecretValues } from '@/lib/configSchema'
import { errorMessage } from '@/lib/api'
import { pluginsApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { isChannelPlugin } from '@/lib/customMeters'
import { CUSTOM_PROTOCOL_REJECTS, isCustomProtocol, protocolInfo } from '@/lib/pluginProtocol'
import { compareSemver } from '@/lib/semver'
import PluginConfigFields from './PluginConfigFields.vue'
import { pluginIdentityChanged, pluginVersionChanged } from './pluginForm'

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
const samePlugin = computed(() => state.value.pluginId === state.value.originalPluginId)
const approvedVersions = computed(() => versions.value.filter(v => v.approval === 'approved'))
/** Edit: approved versions plus the pinned one (should it no longer be approved). */
const versionOptions = computed(() => {
  const list = [...approvedVersions.value]
  const pinned = state.value.originalVersionId
  if (samePlugin.value && pinned && !list.some(v => v.id === pinned)) {
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
    if (s.versionId !== versionId || s.pluginId !== pid)
      return // a newer selection won
    const m = v.manifest
    if (!m) {
      s.error = '无法读取该版本的 manifest'
      return
    }
    s.manifest = m
    s.config = initConfigValues(m.configSchema, fromChannel ? props.channel?.pluginConfig : null)
    s.secrets = initSecretValues(m.configSchema, fromChannel ? props.channel?.secretFields : null)
    s.configTouched = false
    emit('manifest', m)
  }
  catch (err) {
    if (s.pluginId === pid && s.versionId === versionId)
      s.error = errorMessage(err)
  }
  finally {
    if (s.pluginId === pid && s.versionId === versionId)
      s.loading = false
  }
}

async function loadVersions(pluginId: string): Promise<void> {
  const list = (await pluginsApi.get(pluginId)).versions ?? []
  if (state.value.pluginId === pluginId)
    versions.value = list.sort((a, b) => compareSemver(b.version, a.version))
}

async function selectPlugin(id: string) {
  const s = state.value
  s.pluginId = id
  s.versionId = null
  s.manifest = null
  s.config = {}
  s.secrets = {}
  s.error = null
  s.loading = true
  try {
    await loadVersions(id)
    if (s.pluginId !== id)
      return
    const latest = samePlugin.value && s.originalVersionId
      ? versions.value.find(v => v.id === s.originalVersionId)
      : approvedVersions.value[0]
    if (!latest) {
      s.error = '该插件没有已批准的版本'
      return
    }
    s.versionId = latest.id
    await loadManifest(latest.id, samePlugin.value)
  }
  catch (err) {
    if (s.pluginId === id)
      s.error = errorMessage(err)
  }
  finally {
    if (s.pluginId === id)
      s.loading = false
  }
}

onMounted(async () => {
  const s = state.value
  if (props.channel?.plugin) {
    s.originalPluginId = props.channel.plugin.id
    s.originalVersionId = props.channel.plugin.versionId
  }
  try {
    const all = (await pluginsApi.list()).items
    plugins.value = all.filter(p => isChannelPlugin(p) && ((p.status === 'enabled' && p.latest !== null) || p.id === s.originalPluginId))
  }
  catch (err) {
    listError.value = errorMessage(err)
  }
  if (props.channel) {
    const ref = props.channel.plugin
    if (!ref) {
      const builtin = plugins.value.find(p => p.key === `builtin.${props.channel?.type}`)
      if (builtin)
        await selectPlugin(builtin.id)
      return
    }
    s.pluginId = ref.id
    s.versionId = ref.versionId
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
  if (!isEdit.value || !samePlugin.value || id === state.value.originalVersionId) {
    // Back to the pinned version: no confirmation needed.
    state.value.versionId = id
    void loadManifest(id, isEdit.value && samePlugin.value)
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
const changingPlugin = computed(() => pluginIdentityChanged(state.value))
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

    <!-- The same official selectors are available for create and edit. -->
    <div class="space-y-4">
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
          <Select :model-value="state.versionId ?? undefined" :disabled="!versionOptions.length || state.loading" @update:model-value="(v) => v && requestVersion(String(v))">
            <SelectTrigger id="ch-plugin-version" class="w-full font-mono">
              <SelectValue placeholder="—" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="(v, i) in versionOptions" :key="v.id" :value="v.id" class="font-mono">
                v{{ v.version }}{{ samePlugin && v.id === state.originalVersionId ? '（当前）' : i === 0 ? ' · 最新' : '' }}{{ v.approval !== 'approved' ? '（未批准）' : '' }}
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
    </div>
    <p v-if="channel?.plugin" class="text-muted-foreground text-xs">
      当前：{{ channel.plugin.name }} · v{{ channel.plugin.version }}
    </p>
    <Alert v-if="changingPlugin || switching">
      <AlertDescription v-if="changingPlugin">
        保存后切换到 {{ selectedPlugin?.name }} v{{ currentVersionLabel }}。渠道类型随插件更新；原插件的配置和专属密钥会清除，请填写新插件所需字段。API Key、地址和模型映射沿用当前表单。
      </AlertDescription>
      <AlertDescription v-else>
        保存后切换到 v{{ currentVersionLabel }}。未修改下方配置时，由服务端用新版本的 migrateConfig（如有）迁移现有配置；修改配置后则以表单内容整体替换。
      </AlertDescription>
    </Alert>

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
          :secret-state="samePlugin ? channel?.secretFields : null"
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
