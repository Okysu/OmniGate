import type { PluginPermissions } from './types'
import { describe, expect, it } from 'vitest'
import { describePermission, flattenPermissions, formatBytes, groupPermissions, permissionDiffItems } from './pluginPermissions'

const perms: PluginPermissions = {
  network: ['api.deepseek.com', '$baseUrl'],
  secrets: ['apiKey', 'orgId'],
  storage: { maxKeys: 100, maxBytes: 65536 },
  schedule: ['balance.get'],
  dangerous: [],
}

describe('flattenPermissions', () => {
  it('matches the server flatten() format, sorted', () => {
    expect(flattenPermissions(perms)).toEqual([
      'network:$baseUrl',
      'network:api.deepseek.com',
      'schedule:balance.get',
      'secret:apiKey',
      'secret:orgId',
      'storage:100keys/65536B',
    ])
  })

  it('tolerates null lists', () => {
    expect(flattenPermissions({ network: null, secrets: null, schedule: null, dangerous: null })).toEqual([])
    expect(flattenPermissions(null)).toEqual([])
  })
})

describe('describePermission', () => {
  it('explains $baseUrl and wildcards', () => {
    expect(describePermission('network:$baseUrl').label).toBe('渠道 Base URL 所在主机')
    expect(describePermission('network:*.example.com').label).toBe('example.com 的所有子域名')
    expect(describePermission('network:api.x.com')).toMatchObject({ category: 'network', label: 'api.x.com' })
  })

  it('explains secrets, schedules and storage', () => {
    expect(describePermission('secret:apiKey').label).toBe('渠道主 API Key')
    expect(describePermission('secret:orgId').label).toBe('插件密钥「orgId」')
    expect(describePermission('schedule:balance.get').label).toBe('余额（balance.get）')
    expect(describePermission('schedule:custom.foo').label).toBe('custom.foo')
    expect(describePermission('storage:100keys/65536B').label).toBe('最多 100 个键，共 64 KiB')
    expect(describePermission('dangerous:secrets:plaintext')).toMatchObject({ category: 'dangerous', label: 'secrets:plaintext' })
    expect(describePermission('weird').category).toBe('other')
  })

  it('formats byte sizes', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1536)).toBe('1.5 KiB')
    expect(formatBytes(1048576)).toBe('1 MiB')
  })
})

describe('permissionDiffItems', () => {
  it('marks added, unchanged and removed permissions', () => {
    const items = permissionDiffItems(perms, { added: ['network:api.deepseek.com', 'secret:orgId'], removed: ['network:old.example.com'] })
    const byKey = Object.fromEntries(items.map(i => [i.key, i.status]))
    expect(byKey).toEqual({
      'network:$baseUrl': 'unchanged',
      'network:api.deepseek.com': 'added',
      'schedule:balance.get': 'unchanged',
      'secret:apiKey': 'unchanged',
      'secret:orgId': 'added',
      'storage:100keys/65536B': 'unchanged',
      'network:old.example.com': 'removed',
    })
  })

  it('handles a null diff (builtin versions)', () => {
    expect(permissionDiffItems(perms, { added: null, removed: [] }).every(i => i.status === 'unchanged')).toBe(true)
  })

  it('groups in a stable order', () => {
    const groups = groupPermissions(permissionDiffItems(perms, null))
    expect(groups.map(g => g.category)).toEqual(['network', 'secret', 'schedule', 'storage'])
  })
})
