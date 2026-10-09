<script setup lang="ts">
// Online plugin editor (IDE layout, desktop-first). Monaco is loaded lazily from
// ./monaco so it never ships with other routes. The server builds, tests and
// publishes the *saved* draft, so every toolbar action saves pending edits first.
// `?version=<vid>` opens a published version read-only instead of the draft
// (plugins.read suffices; nothing that touches the draft is offered).
import type * as Monaco from 'monaco-editor/editor/editor.api.js'
import type * as MonacoSetup from './monaco'
import type { BindableResult } from '@/lib/jsonPointer'
import type { BuildDiagnostic, BuildResult, Plugin, PluginTestResult, PluginVersion, RiskFinding } from '@/lib/types'
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import { ArrowLeft, CircleAlert, CircleCheck, FlaskConical, Hammer, Loader2, Lock, PencilLine, Rocket, Save, TriangleAlert } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ErrorState from '@/components/ErrorState.vue'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { useTheme } from '@/composables/useTheme'
import { errorMessage, isApiError, isVersionConflict } from '@/lib/api'
import { pluginsApi } from '@/lib/endpoints'
import { formatDateTime, formatRelative } from '@/lib/format'
import { formatBytes } from '@/lib/pluginPermissions'
import { isCustomProtocol } from '@/lib/pluginProtocol'
import { useAuthStore } from '@/stores/auth'
import ApprovalBadge from '../ApprovalBadge.vue'
import DiagnosticsList from '../DiagnosticsList.vue'
import RiskList from '../RiskList.vue'
import FilePathDialog from './FilePathDialog.vue'
import { byteLength, filesDiffer, MAX_FILE_BYTES, MAX_TOTAL_BYTES, starterContent } from './files'
import FileTree from './FileTree.vue'
import PublishResultDialog from './PublishResultDialog.vue'
import TestPanel from './TestPanel.vue'
import UiPreview from './UiPreview.vue'

type MonacoModule = typeof MonacoSetup

const route = useRoute()
const router = useRouter()
const { resolved: theme } = useTheme()
const auth = useAuthStore()
const canManage = computed(() => auth.can('plugins.manage'))
const pluginId = computed(() => String(route.params.id ?? ''))
/** Set in read-only version mode (`?version=<vid>`). */
const versionId = computed(() => (typeof route.query.version === 'string' && route.query.version ? route.query.version : null))
const readOnly = computed(() => versionId.value !== null)
/** The published version shown in read-only mode. */
const viewedVersion = ref<PluginVersion | null>(null)

const plugin = ref<Plugin | null>(null)
const state = ref<'loading' | 'ready' | 'error' | 'nodraft'>('loading')
const loadError = ref<unknown>(null)

// ---------------------------------------------------------------------------
// Monaco + file models
// ---------------------------------------------------------------------------
const mod = shallowRef<MonacoModule | null>(null)
const editorEl = ref<HTMLElement | null>(null)
let editor: Monaco.editor.IStandaloneCodeEditor | null = null
const models = new Map<string, Monaco.editor.ITextModel>()
const viewStates = new Map<string, Monaco.editor.ICodeEditorViewState | null>()

/** Current content by path (mirrors the Monaco models). */
const current = reactive<Record<string, string>>({})
/** Last saved snapshot (what the server's draft holds). */
const saved = ref<Record<string, string>>({})
const draftVersion = ref(0)
/** False when no draft row exists (fresh from an approved version, or after publishing). */
const draftExists = ref(false)
const baseVersionId = ref<string | null>(null)
const active = ref<string | null>(null)

const files = computed(() => Object.keys(current).sort())
const testFiles = computed(() => files.value.filter(f => /^tests\/[^/]+\.json$/.test(f)))
/** The draft's manifest declares `protocol: "custom"` (phase9 §2). */
const draftCustomProtocol = computed(() => {
  try {
    return isCustomProtocol(JSON.parse(current['manifest.json'] ?? 'null') as { protocol?: string } | null)
  }
  catch {
    return false
  }
})
const dirtyFiles = computed(() => new Set(files.value.filter(f => saved.value[f] !== current[f])))
const isDirty = computed(() => filesDiffer(current, saved.value))
const totalBytes = computed(() => files.value.reduce((n, f) => n + byteLength(current[f] ?? ''), 0))

function disposeModels() {
  for (const m of models.values())
    m.dispose()
  models.clear()
  viewStates.clear()
  for (const k of Object.keys(current))
    delete current[k]
}

function createModel(path: string, content: string) {
  const m = mod.value!
  const model = m.monaco.editor.createModel(content, m.languageForPath(path), m.uriForPath(path))
  model.onDidChangeContent(() => {
    current[path] = model.getValue()
  })
  models.set(path, model)
  current[path] = content
}

function setFiles(next: Record<string, string>, snapshot: Record<string, string>) {
  disposeModels()
  for (const [p, c] of Object.entries(next))
    createModel(p, c)
  saved.value = { ...snapshot }
  const first = next['src/index.ts'] !== undefined ? 'src/index.ts' : next['manifest.json'] !== undefined ? 'manifest.json' : files.value[0] ?? null
  active.value = null
  if (first)
    openFile(first)
}

function openFile(path: string, line?: number, column?: number) {
  const model = models.get(path)
  if (!editor || !model)
    return
  if (active.value && active.value !== path)
    viewStates.set(active.value, editor.saveViewState())
  if (editor.getModel() !== model) {
    editor.setModel(model)
    const vs = viewStates.get(path)
    if (vs)
      editor.restoreViewState(vs)
  }
  active.value = path
  if (line && line > 0) {
    editor.revealLineInCenter(line)
    editor.setPosition({ lineNumber: line, column: column && column > 0 ? column : 1 })
  }
  editor.focus()
}

function snapshot(): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [p, m] of models)
    out[p] = m.getValue()
  return out
}

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------
async function loadDraft(): Promise<void> {
  try {
    const d = await pluginsApi.getDraft(pluginId.value)
    draftVersion.value = d.version
    draftExists.value = d.version > 0
    baseVersionId.value = d.baseVersionId
    setFiles(d.files ?? {}, d.files ?? {})
    state.value = 'ready'
  }
  catch (err) {
    if (isApiError(err) && err.status === 404 && (plugin.value?.versions?.length ?? 0) > 0) {
      state.value = 'nodraft'
      return
    }
    loadError.value = err
    state.value = 'error'
  }
}

async function loadVersion(vid: string, seq: number): Promise<void> {
  try {
    const v = await pluginsApi.version(pluginId.value, vid)
    if (seq !== initSeq)
      return
    viewedVersion.value = v
    draftExists.value = false
    draftVersion.value = 0
    baseVersionId.value = v.id
    // Snapshot = content: never dirty, so no unsaved-changes prompts.
    setFiles(v.files ?? {}, v.files ?? {})
    state.value = 'ready'
  }
  catch (err) {
    if (seq !== initSeq)
      return
    loadError.value = err
    state.value = 'error'
  }
}

/** Bumped on every (re)initialisation so stale loads are ignored. */
let initSeq = 0

async function init() {
  const seq = ++initSeq
  state.value = 'loading'
  loadError.value = null
  viewedVersion.value = null
  build.value = null
  builtAt.value = null
  for (const k of Object.keys(samples))
    delete samples[k]
  bottomTab.value = readOnly.value ? 'risk' : 'problems'
  try {
    const [m, p] = await Promise.all([import('./monaco'), pluginsApi.get(pluginId.value)])
    if (seq !== initSeq)
      return
    mod.value = m
    plugin.value = p
    if (p.source === 'builtin') {
      loadError.value = new Error('内置插件由网关原生实现，不能在编辑器中修改。')
      state.value = 'error'
      return
    }
    m.loadSdkTypes().catch((err: unknown) => {
      toast.warning('未能加载插件 SDK 类型声明', { description: `编辑器将缺少 @omnigate/plugin-sdk 的类型提示：${errorMessage(err)}` })
    })
    await nextTick()
    createEditor()
    if (versionId.value)
      await loadVersion(versionId.value, seq)
    else
      await loadDraft()
  }
  catch (err) {
    if (seq !== initSeq)
      return
    loadError.value = err
    state.value = 'error'
  }
}

function createEditor() {
  const m = mod.value
  if (!m || !editorEl.value)
    return
  // The container is re-created when the view leaves and re-enters the ready
  // state (e.g. "no draft" → start from a version): rebuild the editor then.
  if (editor && editor.getContainerDomNode() === editorEl.value) {
    editor.updateOptions({ readOnly: readOnly.value })
    return
  }
  editor?.dispose()
  editor = m.monaco.editor.create(editorEl.value, {
    model: null,
    theme: theme.value === 'dark' ? 'vs-dark' : 'vs',
    automaticLayout: true,
    fontSize: 13,
    tabSize: 2,
    minimap: { enabled: false },
    scrollBeyondLastLine: false,
    fixedOverflowWidgets: true,
    renderWhitespace: 'selection',
    readOnly: readOnly.value,
    readOnlyMessage: { value: '这是已发布的版本，只读。修改请返回草稿。' },
  })
}

watch(theme, (t) => {
  mod.value?.monaco.editor.setTheme(t === 'dark' ? 'vs-dark' : 'vs')
})

/** Start a new draft from the newest published version (when no draft can be loaded). */
const startingFrom = ref(false)
async function startFromVersion() {
  const v = plugin.value?.versions?.[0]
  if (!v)
    return
  startingFrom.value = true
  try {
    const full = await pluginsApi.version(pluginId.value, v.id)
    state.value = 'ready'
    await nextTick()
    createEditor()
    draftVersion.value = 0
    draftExists.value = false
    baseVersionId.value = v.id
    // Unsaved until the first save creates the draft.
    setFiles(full.files ?? {}, {})
  }
  catch (err) {
    toast.error('无法加载版本文件', { description: errorMessage(err) })
  }
  finally {
    startingFrom.value = false
  }
}

// Switching between the draft and a read-only version reuses this component.
watch([pluginId, versionId], () => {
  if (route.name === 'plugin-editor')
    void init()
})

onMounted(() => {
  void init()
  window.addEventListener('keydown', onKeydown, true)
  window.addEventListener('beforeunload', onBeforeUnload)
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown, true)
  window.removeEventListener('beforeunload', onBeforeUnload)
  editor?.dispose()
  editor = null
  disposeModels()
})

/** Unsaved draft edits (never in read-only mode). */
const hasUnsaved = computed(() => !readOnly.value && state.value === 'ready' && isDirty.value)
function onBeforeUnload(e: BeforeUnloadEvent) {
  if (hasUnsaved.value)
    e.preventDefault()
}
function confirmLeave(): boolean {
  return !hasUnsaved.value || window.confirm('有未保存的修改，确定离开编辑器吗？')
}
onBeforeRouteLeave(confirmLeave)
// Same route, other mode (e.g. back/forward between the draft and a version).
onBeforeRouteUpdate((to, from) => to.query.version === from.query.version && to.params.id === from.params.id ? true : confirmLeave())

// ---------------------------------------------------------------------------
// Save (Ctrl/Cmd+S) with optimistic locking
// ---------------------------------------------------------------------------
const saving = ref(false)
const conflictOpen = ref(false)
const conflictBusy = ref(false)

function sizeError(files: Record<string, string>): string | null {
  for (const [p, c] of Object.entries(files)) {
    if (byteLength(c) > MAX_FILE_BYTES)
      return `${p} 超过 256 KiB`
  }
  if (Object.values(files).reduce((n, c) => n + byteLength(c), 0) > MAX_TOTAL_BYTES)
    return '插件总大小超过 1 MiB'
  if (files['manifest.json'] === undefined)
    return '缺少 manifest.json'
  return null
}

/** Saves if needed; resolves false when the save did not happen. */
async function save(opts: { quiet?: boolean, version?: number } = {}): Promise<boolean> {
  if (saving.value || readOnly.value)
    return false
  if (!isDirty.value && draftExists.value && opts.version === undefined)
    return true
  const body = snapshot()
  const err = sizeError(body)
  if (err) {
    toast.error('无法保存', { description: err })
    return false
  }
  saving.value = true
  try {
    const d = await pluginsApi.saveDraft(pluginId.value, body, opts.version ?? draftVersion.value)
    draftVersion.value = d.version
    draftExists.value = true
    baseVersionId.value = d.baseVersionId
    saved.value = body
    if (!opts.quiet)
      toast.success('草稿已保存')
    return true
  }
  catch (e) {
    if (isVersionConflict(e))
      conflictOpen.value = true
    else
      toast.error('保存失败', { description: errorMessage(e) })
    return false
  }
  finally {
    saving.value = false
  }
}

function onKeydown(e: KeyboardEvent) {
  if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 's') {
    e.preventDefault()
    if (state.value === 'ready' && !readOnly.value)
      void save()
  }
}

async function conflictReload() {
  conflictBusy.value = true
  try {
    await loadDraft()
    conflictOpen.value = false
    toast.info('已加载最新草稿，你的修改已丢弃')
  }
  finally {
    conflictBusy.value = false
  }
}
async function conflictOverwrite() {
  conflictBusy.value = true
  try {
    let latest = 0
    try {
      latest = (await pluginsApi.getDraft(pluginId.value)).version
    }
    catch (err) {
      if (!(isApiError(err) && err.status === 404))
        throw err
    }
    conflictOpen.value = false
    if (await save({ version: latest, quiet: true }))
      toast.success('已用你的修改覆盖草稿')
  }
  catch (err) {
    toast.error('覆盖失败', { description: errorMessage(err) })
  }
  finally {
    conflictBusy.value = false
  }
}

// ---------------------------------------------------------------------------
// Build / diagnostics / markers
// ---------------------------------------------------------------------------
type BottomTab = 'problems' | 'test' | 'risk' | 'output' | 'preview'
const bottomTab = ref<BottomTab>('problems')
const build = ref<BuildResult | null>(null)
const builtAt = ref<string | null>(null)
const building = ref(false)

const diagnostics = computed<BuildDiagnostic[]>(() => build.value?.diagnostics ?? [])
const errorCount = computed(() => diagnostics.value.filter(d => d.severity === 'error').length)
const warningCount = computed(() => diagnostics.value.length - errorCount.value)
const problemsByFile = computed(() => {
  const out: Record<string, number> = {}
  for (const d of diagnostics.value) {
    if (d.file && d.severity === 'error')
      out[d.file] = (out[d.file] ?? 0) + 1
  }
  return out
})

function applyMarkers(ds: BuildDiagnostic[]) {
  const m = mod.value
  if (!m)
    return
  const byFile = new Map<string, Monaco.editor.IMarkerData[]>()
  for (const d of ds) {
    const model = models.get(d.file)
    if (!model)
      continue
    const line = Math.min(Math.max(d.line, 1), model.getLineCount())
    const col = d.column > 0 ? d.column : 1
    const list = byFile.get(d.file) ?? []
    list.push({
      severity: d.severity === 'error' ? m.monaco.MarkerSeverity.Error : m.monaco.MarkerSeverity.Warning,
      message: d.message,
      source: 'OmniGate 编译检查',
      startLineNumber: line,
      startColumn: d.line > 0 ? col : 1,
      endLineNumber: line,
      endColumn: model.getLineMaxColumn(line),
    })
    byFile.set(d.file, list)
  }
  for (const [path, model] of models)
    m.monaco.editor.setModelMarkers(model, 'omnigate-build', byFile.get(path) ?? [])
}

function setBuild(b: BuildResult) {
  build.value = b
  builtAt.value = new Date().toISOString()
  applyMarkers(b.diagnostics ?? [])
}

async function runBuild(opts: { quiet?: boolean } = {}): Promise<BuildResult | null> {
  if (!(await save({ quiet: true })))
    return null
  building.value = true
  try {
    const b = await pluginsApi.build(pluginId.value)
    setBuild(b)
    if (!b.ok) {
      bottomTab.value = 'problems'
      if (!opts.quiet)
        toast.error('编译检查未通过', { description: `${errorCount.value} 个错误，详见「问题」` })
    }
    else if (!opts.quiet) {
      toast.success('编译检查通过', { description: `产物 ${formatBytes(b.bundleBytes)}${(b.risk?.length ?? 0) > 0 ? `，${b.risk!.length} 项风险提示` : ''}` })
    }
    return b
  }
  catch (err) {
    toast.error('编译检查失败', { description: errorMessage(err) })
    return null
  }
  finally {
    building.value = false
  }
}

function jumpTo(d: { file: string, line: number, column?: number }) {
  if (models.has(d.file))
    openFile(d.file, d.line, d.column)
  else if (d.file)
    toast.info(`文件 ${d.file} 不在${readOnly.value ? '该版本' : '草稿'}中`)
}
function jumpToRisk(r: RiskFinding) {
  jumpTo({ file: r.file, line: r.line })
}

// ---------------------------------------------------------------------------
// Tests + UI preview samples
// ---------------------------------------------------------------------------
const testPanel = ref<InstanceType<typeof TestPanel> | null>(null)
const testing = ref(false)
const samples = reactive<Record<string, BindableResult>>({})

function onTestResult(res: PluginTestResult, capability: string | null) {
  if (!capability)
    return
  const out = res.output
  const unsupported = out !== null && typeof out === 'object' && (out as Record<string, unknown>).unsupported === true
  samples[capability] = { ok: res.error == null, unsupported, output: out, error: res.error }
}
function onTestBuildFailed(b: BuildResult) {
  setBuild(b)
  bottomTab.value = 'problems'
  toast.error('草稿未通过编译检查，测试未执行')
}
async function runTests() {
  bottomTab.value = 'test'
  await nextTick()
  testing.value = true
  try {
    await testPanel.value?.run()
  }
  finally {
    testing.value = false
  }
}
const prepareForTest = () => save({ quiet: true })

// ---------------------------------------------------------------------------
// Publish
// ---------------------------------------------------------------------------
const publishing = ref(false)
const publishConfirmOpen = ref(false)
const publishCandidate = ref<BuildResult | null>(null)
const published = ref<PluginVersion | null>(null)
const publishedOpen = ref(false)
const noApprovedBaseline = ref(false)
/** The published version is the plugin's first custom-protocol version (phase9 §4 #22). */
const firstCustomPublish = ref(false)

async function startPublish() {
  publishing.value = true
  try {
    const b = await runBuild({ quiet: true })
    if (!b)
      return
    if (!b.ok) {
      toast.error('编译检查未通过，不能发布', { description: `${errorCount.value} 个错误，详见「问题」` })
      return
    }
    publishCandidate.value = b
    publishConfirmOpen.value = true
  }
  finally {
    publishing.value = false
  }
}

async function confirmPublish() {
  publishing.value = true
  noApprovedBaseline.value = !plugin.value?.latest
  firstCustomPublish.value = isCustomProtocol(publishCandidate.value?.manifest) && !isCustomProtocol(plugin.value)
  try {
    const v = await pluginsApi.publish(pluginId.value)
    publishConfirmOpen.value = false
    published.value = v
    publishedOpen.value = true
    // The server turned the draft into a version and deleted the draft row:
    // the editor content now equals the published files.
    draftExists.value = false
    draftVersion.value = 0
    baseVersionId.value = v.id
    saved.value = snapshot()
    try {
      plugin.value = await pluginsApi.get(pluginId.value)
    }
    catch { /* header refresh is best-effort */ }
  }
  catch (err) {
    publishConfirmOpen.value = false
    if (isApiError(err) && err.code === 'plugin_build_failed') {
      const ds = Array.isArray(err.rawDetails.diagnostics) ? err.rawDetails.diagnostics as BuildDiagnostic[] : []
      setBuild({ ok: false, manifest: null, diagnostics: ds, risk: build.value?.risk ?? [], bundleBytes: 0 })
      bottomTab.value = 'problems'
      toast.error('发布失败：编译或校验未通过')
    }
    else if (isApiError(err) && err.code === 'plugin_version_exists') {
      toast.error('版本号冲突', { description: `${err.message}。请修改 manifest.json 中的 version 后再发布。` })
      jumpTo({ file: 'manifest.json', line: 1 })
    }
    else {
      toast.error('发布失败', { description: errorMessage(err) })
    }
  }
  finally {
    publishing.value = false
  }
}

const busy = computed(() => saving.value || building.value || publishing.value || testing.value)

// ---------------------------------------------------------------------------
// File operations
// ---------------------------------------------------------------------------
const pathDialogOpen = ref(false)
const renaming = ref<string | null>(null)
const deleting = ref<string | null>(null)
const deleteOpen = ref(false)

function askCreate() {
  renaming.value = null
  pathDialogOpen.value = true
}
function askRename(path: string) {
  renaming.value = path
  pathDialogOpen.value = true
}
function onPathSubmit(path: string) {
  const from = renaming.value
  if (from) {
    const model = models.get(from)
    const content = model?.getValue() ?? ''
    model?.dispose()
    models.delete(from)
    viewStates.delete(from)
    delete current[from]
    createModel(path, content)
    if (active.value === from) {
      active.value = null
      openFile(path)
    }
  }
  else {
    createModel(path, starterContent(path))
    openFile(path)
  }
}
function askDelete(path: string) {
  deleting.value = path
  deleteOpen.value = true
}
function confirmDelete() {
  const p = deleting.value
  deleteOpen.value = false
  if (!p)
    return
  models.get(p)?.dispose()
  models.delete(p)
  viewStates.delete(p)
  delete current[p]
  if (active.value === p) {
    active.value = null
    const next = current['manifest.json'] !== undefined ? 'manifest.json' : files.value[0]
    if (next)
      openFile(next)
    else
      editor?.setModel(null)
  }
}

const readFile = (path: string) => current[path]

const statusText = computed(() => {
  if (isDirty.value)
    return '有未保存的修改'
  if (!draftExists.value)
    return baseVersionId.value ? '与已发布版本一致（尚无草稿）' : '尚无草稿'
  return `草稿已保存（第 ${draftVersion.value} 版）`
})

const TABS: Array<{ id: BottomTab, label: string }> = [
  { id: 'problems', label: '问题' },
  { id: 'test', label: '测试' },
  { id: 'risk', label: '风险' },
  { id: 'output', label: '输出' },
  { id: 'preview', label: 'UI 预览' },
]
/** Read-only mode has no build or tests (they operate on the draft); risk comes from the version. */
const READONLY_TABS: Array<{ id: BottomTab, label: string }> = [
  { id: 'risk', label: '风险' },
  { id: 'output', label: '版本信息' },
  { id: 'preview', label: 'UI 预览' },
]
const tabs = computed(() => (readOnly.value ? READONLY_TABS : TABS))
const riskList = computed(() => (readOnly.value ? viewedVersion.value?.risk ?? [] : build.value?.risk ?? []))
const hasRiskResult = computed(() => (readOnly.value ? viewedVersion.value !== null : build.value !== null))

function goBack() {
  if (versionId.value)
    void router.push({ name: 'plugin-detail', params: { id: pluginId.value }, query: { version: versionId.value } })
  else
    void router.push({ name: 'plugin-detail', params: { id: pluginId.value } })
}
function backToDraft() {
  void router.push({ name: 'plugin-editor', params: { id: pluginId.value } })
}
</script>

<template>
  <div class="flex h-[calc(100svh-var(--header-height))] min-h-0 flex-col">
    <!-- toolbar -->
    <div class="flex shrink-0 flex-wrap items-center gap-2 border-b px-3 py-2">
      <Button variant="ghost" size="icon-sm" aria-label="返回插件详情" @click="goBack">
        <ArrowLeft />
      </Button>
      <div class="min-w-0">
        <p class="truncate text-sm leading-tight font-semibold">
          {{ plugin?.name ?? '插件编辑器' }}
        </p>
        <p class="text-muted-foreground flex items-center gap-1.5 truncate text-xs leading-tight">
          <span class="font-mono">{{ plugin?.key }}</span>
          <span v-if="readOnly && viewedVersion" class="truncate">
            · 发布于 {{ formatDateTime(viewedVersion.publishedAt) }}
          </span>
          <span v-else-if="state === 'ready' && !readOnly" class="inline-flex items-center gap-1">
            ·
            <span v-if="isDirty" class="bg-amber-500 size-1.5 rounded-full" aria-hidden="true" />
            {{ statusText }}
          </span>
        </p>
      </div>
      <div
        v-if="readOnly"
        role="status"
        class="flex min-w-0 items-center gap-1.5 rounded-md border border-sky-500/40 bg-sky-500/10 px-2 py-1 text-xs text-sky-900 dark:text-sky-200"
        data-testid="readonly-banner"
      >
        <Lock class="size-3.5 shrink-0" aria-hidden="true" />
        <span class="truncate font-medium">只读：v{{ viewedVersion?.version ?? '…' }}（已发布版本）</span>
        <ApprovalBadge v-if="viewedVersion" :approval="viewedVersion.approval" class="h-4 px-1 text-[10px]" />
      </div>
      <div v-if="readOnly" class="ml-auto flex items-center gap-1.5">
        <Button v-if="canManage && plugin && plugin.source !== 'builtin'" variant="outline" size="sm" @click="backToDraft">
          <PencilLine />
          返回草稿
        </Button>
      </div>
      <div v-else-if="state === 'ready'" class="ml-auto flex flex-wrap items-center gap-1.5">
        <Button variant="outline" size="sm" :disabled="busy || (!isDirty && draftExists)" title="保存草稿（Ctrl/⌘+S）" @click="save()">
          <Loader2 v-if="saving" class="animate-spin" />
          <Save v-else />
          保存
        </Button>
        <Button variant="outline" size="sm" :disabled="busy" @click="runBuild()">
          <Loader2 v-if="building && !publishing" class="animate-spin" />
          <Hammer v-else />
          编译检查
        </Button>
        <Button variant="outline" size="sm" :disabled="busy" @click="runTests">
          <Loader2 v-if="testing" class="animate-spin" />
          <FlaskConical v-else />
          运行测试
        </Button>
        <Button size="sm" :disabled="busy" @click="startPublish">
          <Loader2 v-if="publishing" class="animate-spin" />
          <Rocket v-else />
          发布
        </Button>
      </div>
    </div>

    <div v-if="state === 'error'" class="flex flex-1 items-center justify-center p-6">
      <ErrorState :error="loadError" @retry="init" />
    </div>

    <div v-else-if="state === 'nodraft'" class="flex flex-1 items-center justify-center p-6">
      <div class="max-w-md space-y-3 text-center">
        <p class="font-medium">
          该插件当前没有可编辑的草稿
        </p>
        <p class="text-muted-foreground text-sm">
          草稿已在发布后转为版本，且还没有已批准的版本可以作为起点。可以从最新发布的 v{{ plugin?.versions?.[0]?.version }}（{{ plugin?.versions?.[0]?.approval === 'pending' ? '待审批' : plugin?.versions?.[0]?.approval === 'rejected' ? '已拒绝' : '已批准' }}）的文件开始新的草稿。
        </p>
        <Button :disabled="startingFrom" @click="startFromVersion">
          <Loader2 v-if="startingFrom" class="animate-spin" />
          从 v{{ plugin?.versions?.[0]?.version }} 开始编辑
        </Button>
      </div>
    </div>

    <template v-else>
      <!-- main: tree + editor -->
      <div class="flex min-h-0 flex-1">
        <aside class="hidden w-56 shrink-0 border-r md:block">
          <Skeleton v-if="state === 'loading'" class="m-2 h-40" />
          <FileTree
            v-else
            :files="files"
            :active="active"
            :dirty="dirtyFiles"
            :problems="problemsByFile"
            :readonly="readOnly"
            @open="(p) => openFile(p)"
            @create="askCreate"
            @rename="askRename"
            @remove="askDelete"
          />
        </aside>
        <div class="flex min-w-0 flex-1 flex-col">
          <!-- narrow screens: file picker instead of the tree -->
          <div v-if="state === 'ready'" class="flex items-center gap-2 border-b px-2 py-1.5 md:hidden">
            <Select :model-value="active ?? undefined" @update:model-value="(v) => v && openFile(String(v))">
              <SelectTrigger class="h-8 min-w-0 flex-1 font-mono text-xs" aria-label="打开文件">
                <SelectValue placeholder="选择文件" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem v-for="f in files" :key="f" :value="f" class="font-mono text-xs">
                  {{ f }}{{ dirtyFiles.has(f) ? ' •' : '' }}
                </SelectItem>
              </SelectContent>
            </Select>
            <Button v-if="!readOnly" variant="outline" size="sm" @click="askCreate">
              新建
            </Button>
          </div>
          <div v-if="active" class="text-muted-foreground flex h-8 shrink-0 items-center gap-1.5 border-b px-3 font-mono text-xs">
            <span class="truncate">{{ active }}</span>
            <span v-if="readOnly" class="ml-auto shrink-0 font-sans">只读</span>
            <span v-if="dirtyFiles.has(active)" class="bg-foreground/70 size-1.5 rounded-full" title="未保存" />
          </div>
          <div class="relative min-h-0 flex-1">
            <div ref="editorEl" class="absolute inset-0" />
            <div v-if="state === 'loading'" class="bg-background absolute inset-0 flex items-center justify-center">
              <Loader2 class="text-muted-foreground size-6 animate-spin" />
              <span class="sr-only">正在加载编辑器</span>
            </div>
          </div>
        </div>
      </div>

      <!-- bottom panel -->
      <div class="flex h-72 shrink-0 flex-col border-t">
        <div class="flex shrink-0 items-center gap-1 overflow-x-auto border-b px-2" role="tablist" aria-label="底部面板">
          <button
            v-for="t in tabs"
            :key="t.id"
            type="button"
            role="tab"
            :aria-selected="bottomTab === t.id"
            class="relative flex h-9 shrink-0 items-center gap-1.5 px-2.5 text-xs font-medium transition-colors"
            :class="bottomTab === t.id ? 'text-foreground after:bg-foreground after:absolute after:inset-x-1 after:bottom-0 after:h-0.5' : 'text-muted-foreground hover:text-foreground'"
            @click="bottomTab = t.id"
          >
            {{ t.label }}
            <template v-if="t.id === 'problems' && build">
              <span v-if="errorCount" class="text-destructive inline-flex items-center gap-0.5 tabular-nums"><CircleAlert class="size-3" />{{ errorCount }}</span>
              <span v-if="warningCount" class="inline-flex items-center gap-0.5 text-amber-600 tabular-nums dark:text-amber-400"><TriangleAlert class="size-3" />{{ warningCount }}</span>
              <CircleCheck v-if="!errorCount && !warningCount" class="size-3 text-emerald-600 dark:text-emerald-400" />
            </template>
            <span v-if="t.id === 'risk' && riskList.length" class="text-amber-600 tabular-nums dark:text-amber-400">{{ riskList.length }}</span>
          </button>
        </div>
        <div class="min-h-0 flex-1">
          <div v-show="bottomTab === 'problems'" class="h-full overflow-y-auto">
            <p v-if="!build" class="text-muted-foreground p-3 text-sm">
              点击「编译检查」查看 manifest 校验、TypeScript 编译与导出检查的结果。编辑器中的红色波浪线来自 TypeScript 语言服务，可能比服务端编译更严格。
            </p>
            <p v-else-if="!diagnostics.length" class="flex items-center gap-1.5 p-3 text-sm">
              <CircleCheck class="size-4 text-emerald-600 dark:text-emerald-400" />
              没有问题（{{ formatRelative(builtAt) }}）
            </p>
            <DiagnosticsList v-else :diagnostics="diagnostics" interactive @select="jumpTo" />
          </div>
          <div v-show="bottomTab === 'test'" class="h-full">
            <TestPanel
              v-if="state === 'ready' && !readOnly"
              ref="testPanel"
              :plugin-id="pluginId"
              :test-files="testFiles"
              :custom-protocol="draftCustomProtocol"
              :read-file="readFile"
              :prepare="prepareForTest"
              @result="onTestResult"
              @build-failed="onTestBuildFailed"
            />
          </div>
          <div v-show="bottomTab === 'risk'" class="h-full overflow-y-auto p-3">
            <p v-if="!hasRiskResult" class="text-muted-foreground text-sm">
              {{ readOnly ? '正在加载…' : '编译检查时会同时进行静态风险扫描。' }}
            </p>
            <RiskList v-else :risk="riskList" interactive @select="jumpToRisk" />
          </div>
          <div v-show="bottomTab === 'output'" class="h-full overflow-y-auto p-3 text-sm">
            <dl v-if="readOnly" class="grid max-w-xl grid-cols-[8rem_1fr] gap-x-4 gap-y-1.5">
              <dt class="text-muted-foreground">
                版本
              </dt>
              <dd class="flex items-center gap-1.5 font-mono">
                v{{ viewedVersion?.version ?? '—' }}
                <ApprovalBadge v-if="viewedVersion" :approval="viewedVersion.approval" class="font-sans" />
              </dd>
              <dt class="text-muted-foreground">
                发布时间
              </dt>
              <dd>{{ viewedVersion ? formatDateTime(viewedVersion.publishedAt) : '—' }}</dd>
              <dt class="text-muted-foreground">
                内容哈希
              </dt>
              <dd class="font-mono text-xs break-all">
                {{ viewedVersion?.contentHash ?? '—' }}
              </dd>
              <dt class="text-muted-foreground">
                源文件
              </dt>
              <dd class="tabular-nums">
                {{ files.length }} 个，{{ formatBytes(totalBytes) }}
              </dd>
              <dt class="text-muted-foreground">
                说明
              </dt>
              <dd class="text-muted-foreground">
                已发布的版本不可修改。{{ canManage ? '修改请点击「返回草稿」，在草稿中编辑后发布新版本。' : '' }}
              </dd>
            </dl>
            <dl v-else class="grid max-w-md grid-cols-[8rem_1fr] gap-x-4 gap-y-1.5">
              <dt class="text-muted-foreground">
                编译产物
              </dt>
              <dd class="tabular-nums">
                <template v-if="build?.ok">
                  {{ formatBytes(build.bundleBytes) }}（{{ build.bundleBytes.toLocaleString('zh-CN') }} 字节，ES2017 IIFE）
                </template>
                <span v-else-if="build" class="text-destructive">编译未通过</span>
                <span v-else class="text-muted-foreground">尚未编译</span>
              </dd>
              <dt class="text-muted-foreground">
                源文件
              </dt>
              <dd class="tabular-nums">
                {{ files.length }} / 64 个，{{ formatBytes(totalBytes) }} / 1 MiB
              </dd>
              <dt class="text-muted-foreground">
                manifest 版本
              </dt>
              <dd class="font-mono">
                {{ build?.manifest?.version ?? '—' }}
              </dd>
              <dt class="text-muted-foreground">
                最近编译
              </dt>
              <dd>{{ builtAt ? formatRelative(builtAt) : '—' }}</dd>
            </dl>
          </div>
          <div v-show="bottomTab === 'preview'" class="h-full">
            <UiPreview :manifest-text="current['manifest.json']" :samples="samples" />
          </div>
        </div>
      </div>
    </template>

    <FilePathDialog v-model:open="pathDialogOpen" :current="renaming" :existing="files" @submit="onPathSubmit" />

    <ConfirmDialog
      v-model:open="deleteOpen"
      :title="`删除文件 ${deleting ?? ''}？`"
      confirm-text="删除"
      destructive
      @confirm="confirmDelete"
    >
      <p>删除会在下次保存时生效；保存前可以通过重新加载草稿恢复。</p>
    </ConfirmDialog>

    <ConfirmDialog
      v-model:open="conflictOpen"
      title="草稿已在其他地方被修改"
      confirm-text="用我的修改覆盖"
      cancel-text="稍后处理"
      :loading="conflictBusy"
      @confirm="conflictOverwrite"
    >
      <p>保存时发现服务端的草稿版本已变化（可能在另一个窗口中编辑或导入了 ZIP）。</p>
      <p>你可以用当前编辑器中的内容覆盖它，或者放弃修改并重新加载最新草稿。</p>
      <Button variant="outline" size="sm" :disabled="conflictBusy" @click="conflictReload">
        放弃我的修改并重新加载
      </Button>
    </ConfirmDialog>

    <ConfirmDialog
      v-model:open="publishConfirmOpen"
      :title="`发布 v${publishCandidate?.manifest?.version ?? ''}？`"
      confirm-text="发布"
      :loading="publishing"
      @confirm="confirmPublish"
    >
      <p>编译检查已通过。发布会把当前草稿转为不可变版本，之后不能再修改该版本号的内容。</p>
      <p>权限与上一个已批准版本相同时自动批准；否则需要系统管理员审批后渠道才能使用。</p>
      <p v-if="isCustomProtocol(publishCandidate?.manifest) && !isCustomProtocol(plugin)" class="text-violet-700 dark:text-violet-300">
        这是该插件第一个自定义协议版本，发布后必须经过系统管理员审批（即使权限未变化）。
      </p>
      <p v-if="(publishCandidate?.risk?.length ?? 0) > 0" class="text-amber-700 dark:text-amber-300">
        静态扫描有 {{ publishCandidate?.risk?.length }} 项风险提示，见「风险」。
      </p>
    </ConfirmDialog>

    <PublishResultDialog v-model:open="publishedOpen" :version="published" :plugin-id="pluginId" :no-approved-baseline="noApprovedBaseline" :first-custom="firstCustomPublish" />
  </div>
</template>
