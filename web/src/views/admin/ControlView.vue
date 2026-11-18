<script setup lang="ts">
import type { ControlEvent, ControlRule, ControlSettings, ControlSnapshot } from '@/lib/control'
import { computed, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'
import PageHeader from '@/components/PageHeader.vue'
import FormField from '@/components/FormField.vue'
import MultiSelect from '@/components/MultiSelect.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { errorMessage } from '@/lib/api'
import { controlApi, controlLines, controlPercent } from '@/lib/control'
import { channelsApi } from '@/lib/endpoints'
import ControlReviewDialog from './ControlReviewDialog.vue'

const settings = ref<ControlSettings>({ enabled: false, baseUrl: 'https://api.typesafe.ai/v1', model: 'jev-latest', threshold: 0.7, categories: [], failClosed: false, storeExcerpt: true, excerptChars: 1000, storeSnapshots: true, snapshotWindowSeconds: 300, keep: 1000, minChars: 1, timeoutMs: 10000, rules: [], version: 0, apiKeySet: false })
const loaded = ref(false)
const apiKey = ref('')
const saving = ref(false)
const error = ref('')
const channelOptions = ref<{ value: string, label: string }[]>([])
const events = ref<ControlEvent[]>([])
const total = ref(0)
const page = ref(1)
const loadingEvents = ref(false)
const userId = ref('')
const requestId = ref('')
const selected = ref<ControlEvent | null>(null)
const checked = ref(new Set<string>())
const snapshot = ref<ControlSnapshot | null>(null)
const snapshotLoading = ref(false)
const snapshotError = ref('')
const reviewOpen = ref(false)
const reviewEvents = ref<ControlEvent[]>([])
const reviewable = computed(() => events.value.filter(event => event.snapshotId))
const checkedEvents = computed(() => reviewable.value.filter(event => checked.value.has(event.id)))
let snapshotVersion = 0
const deleteOpen = ref(false)
const deleteTarget = ref<string | null>(null)
const deleting = ref(false)
const detailOpen = computed({ get: () => selected.value !== null, set: (v: boolean) => { if (!v) selected.value = null } })
const actions: Record<ControlRule['action'], string> = { audit: '仅审计', block: '拦截请求', notify: '通知用户', ban: '自动封禁账号' }
async function load() {
  try {
    const [s, channels] = await Promise.all([controlApi.settings(), channelsApi.list({ page: 1, pageSize: 200 })])
    settings.value = s
    loaded.value = true
    channelOptions.value = channels.items.map(c => ({ value: c.id, label: c.name }))
  }
  catch (e) { error.value = errorMessage(e) }
}
async function save() {
  if (!loaded.value) return
  saving.value = true
  error.value = ''
  try {
    const { apiKeySet: _apiKeySet, ...body } = settings.value
    settings.value = await controlApi.save({ ...body, ...(apiKey.value.trim() ? { apiKey: apiKey.value.trim() } : {}) })
    apiKey.value = ''
    toast.success('管控配置已保存')
  }
  catch (e) { error.value = errorMessage(e) }
  finally { saving.value = false }
}
function addRule() {
  settings.value?.rules.push({ id: crypto.randomUUID(), name: '新规则', enabled: false, channelIds: [], models: [], action: 'audit', notifyAdmins: false, notifyUser: false })
}
function moveRule(index: number, delta: number) {
  const rules = settings.value.rules
  const other = index + delta
  if (other < 0 || other >= rules.length) return
  const rule = rules[index]!
  rules[index] = rules[other]!
  rules[other] = rule
}
async function deleteRecords() {
  deleting.value = true
  try {
    if (deleteTarget.value) await controlApi.remove(deleteTarget.value)
    else await controlApi.clear()
    deleteOpen.value = false
    selected.value = null
    page.value = 1
    await loadEvents()
  }
  catch (e) { error.value = errorMessage(e) }
  finally { deleting.value = false }
}
async function loadEvents() {
  loadingEvents.value = true
  error.value = ''
  try {
    const r = await controlApi.events({ page: page.value, pageSize: 20, userId: userId.value.trim() || undefined, requestId: requestId.value.trim() || undefined })
    events.value = r.items
    total.value = r.total
    checked.value = new Set()
  }
  catch (e) { error.value = errorMessage(e) }
  finally { loadingEvents.value = false }
}
function showDetail(event: ControlEvent) {
  snapshotVersion++
  snapshot.value = null
  snapshotLoading.value = false
  snapshotError.value = ''
  selected.value = event
}
async function loadSnapshot() {
  if (!selected.value?.snapshotId) return
  const version = ++snapshotVersion
  snapshotLoading.value = true
  snapshotError.value = ''
  try {
    const data = await controlApi.snapshot(selected.value.id)
    if (version === snapshotVersion) snapshot.value = data
  }
  catch (err) {
    if (version === snapshotVersion) snapshotError.value = errorMessage(err)
  }
  finally {
    if (version === snapshotVersion) snapshotLoading.value = false
  }
}
function openReview(items: ControlEvent[]) {
  selected.value = null
  reviewEvents.value = items
  reviewOpen.value = true
}
function applyDecision(id: string, decision: Pick<ControlEvent, 'reviewProbability' | 'reviewThreshold' | 'effectiveHit' | 'reviewedAt'>) {
  const event = events.value.find(item => item.id === id)
  if (event) Object.assign(event, decision)
}
function when(value: string) { return new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Shanghai', dateStyle: 'short', timeStyle: 'medium', hour12: false }).format(new Date(value)) }
onMounted(() => { void load(); void loadEvents() })
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="管控系统" description="按渠道与模型审查请求，记录准确的用户、Key 和请求 ID，并按规则处理。">
      <template #actions>
        <Button :disabled="saving || !loaded" @click="save">
          {{ saving ? '保存中…' : '保存配置' }}
        </Button>
      </template>
    </PageHeader>
    <p v-if="error" class="text-destructive text-sm" role="alert">
      {{ error }}
    </p>
    <Tabs default-value="settings">
      <TabsList>
        <TabsTrigger value="settings">
          管控配置
        </TabsTrigger><TabsTrigger value="events">
          审查记录
        </TabsTrigger>
      </TabsList>
      <TabsContent v-if="loaded" value="settings" class="space-y-6 pt-4">
        <Card>
          <CardHeader><CardTitle>Jev 服务</CardTitle></CardHeader>
          <CardContent class="grid gap-5 sm:grid-cols-2">
            <FormField label="启用管控" for="control-enabled" hint="默认关闭。保存并启用后，仅检查命中规则的请求。">
              <Switch id="control-enabled" v-model="settings.enabled" />
            </FormField>
            <FormField label="检查失败时拦截" for="control-fail-closed" hint="关闭时检查异常会放行并记录；异常不会触发自动封禁。">
              <Switch id="control-fail-closed" v-model="settings.failClosed" />
            </FormField>
            <FormField label="Jev Base URL" for="control-base" hint="默认为官方 API，亦可填写兼容服务的 API 根地址，例如 https://example.com/v1。">
              <Input id="control-base" v-model="settings.baseUrl" />
            </FormField>
            <FormField label="Jev API Key" for="control-key" :hint="settings.apiKeySet ? '已配置。留空保留原 Key，新 Key 保存后加密存储。' : '启用前请填写 API Key。'">
              <Input id="control-key" v-model="apiKey" type="password" autocomplete="new-password" />
            </FormField>
            <FormField label="审查模型" for="control-model">
              <Input id="control-model" v-model="settings.model" />
            </FormField>
            <FormField label="命中阈值（0—1）" for="control-threshold">
              <Input id="control-threshold" v-model.number="settings.threshold" type="number" min="0" max="1" step="0.01" />
            </FormField>
            <FormField label="超时（毫秒）" for="control-timeout">
              <Input id="control-timeout" v-model.number="settings.timeoutMs" type="number" min="1000" max="30000" />
            </FormField>
            <FormField label="最短文本（字符）" for="control-min">
              <Input id="control-min" v-model.number="settings.minChars" type="number" min="0" max="500" />
            </FormField>
            <FormField label="保留最新记录条数" for="control-keep">
              <Input id="control-keep" v-model.number="settings.keep" type="number" min="1" max="10000" />
            </FormField>
            <FormField label="保存文本摘要" for="control-excerpt">
              <Switch id="control-excerpt" v-model="settings.storeExcerpt" />
            </FormField>
            <FormField label="摘要长度（字符）" for="control-excerpt-length">
              <Input id="control-excerpt-length" v-model.number="settings.excerptChars" type="number" min="180" max="10000" :disabled="!settings.storeExcerpt" />
            </FormField>
            <FormField label="保存请求快照" for="control-snapshots" hint="仅为命中或检查异常的请求保存过滤后的文本，单份上限 1 MB。">
              <Switch id="control-snapshots" v-model="settings.storeSnapshots" />
            </FormField>
            <FormField label="快照去重窗口（秒）" for="control-snapshot-window" hint="同一用户在窗口内共用首份快照。">
              <Input id="control-snapshot-window" v-model.number="settings.snapshotWindowSeconds" type="number" min="60" max="86400" :disabled="!settings.storeSnapshots" />
            </FormField>
            <FormField label="审查分类" for="control-categories" hint="每行一类，最多 32 类。格式：名称 | 判断问题 | 命中条件 | 非命中条件；也可仅填写一句问题。清空后不再审查。" class="sm:col-span-2">
              <Textarea id="control-categories" :model-value="settings.categories.join('\n')" rows="9" @update:model-value="v => { if (settings) settings.categories = controlLines(String(v)) }" />
            </FormField>
          </CardContent>
        </Card>
        <div class="flex items-center justify-between">
          <h2 class="text-lg font-semibold">
            管控规则
          </h2><Button variant="outline" @click="addRule">
            新增规则
          </Button>
        </div>
        <p class="text-muted-foreground text-sm">
          按顺序采用第一条匹配的启用规则；渠道和模型条件需同时满足。同一次请求不会因重试重复处理。仅命中和检查异常落库，正常放行不保存内容。
        </p>
        <Card v-for="(rule, index) in settings.rules" :key="rule.id">
          <CardHeader class="flex-row items-center justify-between">
            <CardTitle>规则 {{ index + 1 }}</CardTitle><div class="flex gap-1">
              <Button variant="ghost" size="sm" :disabled="index === 0" @click="moveRule(index, -1)">
                上移
              </Button><Button variant="ghost" size="sm" :disabled="index === settings.rules.length - 1" @click="moveRule(index, 1)">
                下移
              </Button><Button variant="ghost" size="sm" @click="settings.rules.splice(index, 1)">
                移除规则
              </Button>
            </div>
          </CardHeader>
          <CardContent class="grid gap-5 sm:grid-cols-2">
            <FormField :for="`control-name-${rule.id}`" label="规则名称">
              <Input :id="`control-name-${rule.id}`" v-model="rule.name" />
            </FormField>
            <FormField :for="`control-rule-${rule.id}`" label="启用规则">
              <Switch :id="`control-rule-${rule.id}`" v-model="rule.enabled" />
            </FormField>
            <FormField label="适用渠道" hint="未选择则匹配全部渠道。">
              <MultiSelect v-model="rule.channelIds" :options="channelOptions" empty-label="全部渠道" />
            </FormField>
            <FormField :for="`control-models-${rule.id}`" label="适用模型" hint="每行一个模型 ID 或通配符，如 gpt-*；正则以 re: 开头，如 re:^gpt-6\.[12]-(sol|astra)$。留空匹配全部模型。">
              <Textarea :id="`control-models-${rule.id}`" :model-value="rule.models.join('\n')" rows="3" @update:model-value="v => rule.models = controlLines(String(v))" />
            </FormField>
            <FormField label="命中后的动作">
              <Select v-model="rule.action">
                <SelectTrigger class="w-full">
                  <SelectValue />
                </SelectTrigger><SelectContent>
                  <SelectItem v-for="(label, value) in actions" :key="value" :value="value">
                    {{ label }}
                  </SelectItem>
                </SelectContent>
              </Select>
            </FormField>
            <FormField :for="`control-admins-${rule.id}`" label="通知管理员" hint="向启用的系统管理员发送站内通知，并在 SMTP 可用时发送邮件。">
              <Switch :id="`control-admins-${rule.id}`" v-model="rule.notifyAdmins" />
            </FormField>
            <FormField :for="`control-user-${rule.id}`" label="通知触发用户" hint="通知用户动作始终发送；其他动作可额外开启。">
              <Switch :id="`control-user-${rule.id}`" v-model="rule.notifyUser" />
            </FormField>
            <p v-if="rule.action === 'ban'" class="text-muted-foreground text-sm">
              命中后拦截当前请求并停用账号，使其所有 Key 的新请求失效；无自动解封期限，可由管理员复核后恢复。系统管理员账号不会被自动停用。
            </p>
          </CardContent>
        </Card>
      </TabsContent>
      <TabsContent value="events" class="space-y-4 pt-4">
        <div class="flex flex-wrap gap-2">
          <Input v-model="userId" placeholder="按用户 UUID 筛选" class="max-w-sm" /><Input v-model="requestId" placeholder="按请求 ID 筛选" class="max-w-sm" /><Button :disabled="loadingEvents" variant="outline" @click="page = 1; loadEvents()">
            查询 / 刷新
          </Button><Button variant="outline" :disabled="!total || loadingEvents" @click="deleteTarget = null; deleteOpen = true">
            清空记录
          </Button>
          <Button :disabled="!checkedEvents.length || loadingEvents" @click="openReview(checkedEvents)">
            批量复核（{{ checkedEvents.length }}）
          </Button>
        </div>
        <Card>
          <CardContent class="overflow-x-auto pt-4">
            <Table>
              <TableHeader><TableRow><TableHead><Checkbox aria-label="选择本页可复核记录" :disabled="!reviewable.length" :model-value="reviewable.length > 0 && checkedEvents.length === reviewable.length ? true : checkedEvents.length ? 'indeterminate' : false" @update:model-value="value => checked = new Set(value === true ? reviewable.map(event => event.id) : [])" /></TableHead><TableHead>时间（北京时间）</TableHead><TableHead>用户 / Key</TableHead><TableHead>渠道 / 模型</TableHead><TableHead>分类</TableHead><TableHead>有效判定</TableHead><TableHead>处理</TableHead><TableHead /></TableRow></TableHeader>
              <TableBody>
                <TableRow v-for="event in events" :key="event.id">
                  <TableCell><Checkbox :aria-label="`选择请求 ${event.requestId}`" :disabled="!event.snapshotId" :model-value="checked.has(event.id)" @update:model-value="value => value === true ? checked.add(event.id) : checked.delete(event.id)" /></TableCell>
                  <TableCell>{{ when(event.createdAt) }}</TableCell><TableCell>
                    {{ event.userName }}<div class="text-muted-foreground text-xs">
                      {{ event.keyName }}
                    </div>
                  </TableCell><TableCell>
                    {{ event.channelName }}<div class="text-muted-foreground text-xs">
                      {{ event.model }}
                    </div>
                  </TableCell><TableCell>{{ event.failure ? '检查异常：' : '' }}{{ event.reason }}</TableCell><TableCell>
                    {{ controlPercent(event.reviewProbability ?? event.probability) }}<div v-if="event.reviewedAt" class="text-muted-foreground text-xs">
                      {{ event.effectiveHit ? '复核命中' : '复核未命中' }}
                    </div>
                  </TableCell><TableCell>
                    {{ actions[event.action] }}<div v-if="event.actionState === 'pending'" class="text-muted-foreground text-xs">
                      等待处理 / 待复核
                    </div><div v-if="event.actionError" class="text-destructive text-xs">
                      处理未完成
                    </div>
                  </TableCell><TableCell>
                    <Button size="sm" variant="ghost" @click="showDetail(event)">
                      详情
                    </Button>
                  </TableCell>
                </TableRow>
                <TableRow v-if="!events.length">
                  <TableCell :colspan="8" class="text-muted-foreground py-8 text-center">
                    {{ loadingEvents ? '正在读取…' : '暂无记录' }}
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </CardContent>
        </Card>
        <div class="flex items-center justify-end gap-3 text-sm">
          <span>共 {{ total }} 条，第 {{ page }} 页</span><Button variant="outline" size="sm" :disabled="page <= 1 || loadingEvents" @click="page--; loadEvents()">
            上一页
          </Button><Button variant="outline" size="sm" :disabled="page * 20 >= total || loadingEvents" @click="page++; loadEvents()">
            下一页
          </Button>
        </div>
      </TabsContent>
    </Tabs>
    <Dialog v-model:open="detailOpen">
      <DialogContent v-if="selected" class="max-h-[85svh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader><DialogTitle>管控记录详情</DialogTitle><DialogDescription>分类概率是自动判断结果，记录可用于人工复核。</DialogDescription></DialogHeader>
        <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-3 text-sm">
          <dt>请求 ID</dt><dd class="break-all font-mono">
            {{ selected.requestId }}
          </dd><dt>用户</dt><dd class="break-all">
            {{ selected.userName }} · {{ selected.userId }}
          </dd><dt>Key</dt><dd class="break-all">
            {{ selected.keyName }} · {{ selected.keyId }}
          </dd><dt>渠道</dt><dd class="break-all">
            {{ selected.channelName }} · {{ selected.channelId }}
          </dd><dt>协议 / 模型</dt><dd>{{ selected.dialect }} · {{ selected.model }}</dd><dt>处理错误</dt><dd class="break-all">
            {{ selected.actionState === 'pending' ? '等待处理 / 待复核' : selected.actionError || '处理完成' }}
          </dd>
        </dl>
        <div v-for="score in selected.scores ?? []" :key="score.label" class="flex justify-between gap-4 text-sm">
          <span>{{ score.label }}</span><span>{{ (score.probability * 100).toFixed(1) }}%</span>
        </div>
        <p class="text-sm">
          Jev 阈值 {{ controlPercent(selected.threshold) }}<span v-if="selected.reviewedAt"> · 已应用复核 {{ controlPercent(selected.reviewProbability) }} · 复核阈值 {{ controlPercent(selected.reviewThreshold) }} · {{ selected.effectiveHit ? '命中' : '未命中' }}</span>
        </p>
        <p class="bg-muted rounded-md p-3 text-sm whitespace-pre-wrap break-words">
          {{ selected.excerpt || '未保存文本摘要' }}
        </p>
        <div v-if="snapshot" class="space-y-2 text-sm">
          <p class="break-all">
            快照来源 {{ snapshot.requestId }} · {{ when(snapshot.createdAt) }}{{ snapshot.truncated ? ' · 已截取' : '' }}
          </p>
          <p v-if="snapshot.requestId !== selected.requestId" class="text-muted-foreground">
            此记录引用同一用户窗口内的首份快照。
          </p>
          <pre class="bg-muted max-h-72 overflow-auto rounded-md p-3 text-xs whitespace-pre-wrap break-all">{{ JSON.stringify(snapshot.payload, null, 2) }}</pre>
        </div>
        <p v-if="snapshotError" class="text-destructive text-sm" role="alert">
          {{ snapshotError }}
        </p>
        <div class="flex flex-wrap gap-2">
          <Button v-if="selected.snapshotId && !snapshot" variant="outline" :disabled="snapshotLoading" @click="loadSnapshot">
            {{ snapshotLoading ? '读取中…' : '查看请求快照' }}
          </Button>
          <Button :disabled="!selected.snapshotId" @click="openReview([selected])">
            模型复核
          </Button>
          <Button variant="destructive" @click="deleteTarget = selected.id; deleteOpen = true">
            删除这条记录
          </Button>
        </div>
      </DialogContent>
    </Dialog>
    <ControlReviewDialog v-model:open="reviewOpen" :events="reviewEvents" @applied="applyDecision" />
    <ConfirmDialog v-model:open="deleteOpen" :title="deleteTarget ? '删除这条管控记录？' : '清空全部管控记录？'" description="删除后无法恢复；已执行的账号处理不会撤销，删除操作会写入审计日志。" destructive :loading="deleting" @confirm="deleteRecords" />
  </div>
</template>
