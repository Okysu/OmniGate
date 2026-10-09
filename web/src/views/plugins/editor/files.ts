// Pure helpers for the editor's file tree (limits from phase2-api.md §1 / compile.go).

export const MAX_FILES = 64
export const MAX_FILE_BYTES = 256 << 10
export const MAX_TOTAL_BYTES = 1 << 20

const PATH_RE = /^[A-Za-z0-9._-]+(?:\/[A-Za-z0-9._-]+)*$/

/** Validates a package path; returns an error message or "". */
export function validatePath(path: string, existing: readonly string[], current?: string): string {
  const p = path.trim()
  if (!p)
    return '请输入文件路径'
  if (!PATH_RE.test(p))
    return '路径只能包含字母、数字、点、下划线、连字符和 /，不能以 / 开头或结尾'
  if (p.split('/').some(seg => seg === '..' || seg === '.') || p.includes('..'))
    return '路径不能包含 . 或 .. 段'
  if (p !== current && existing.includes(p))
    return '已存在同名文件'
  if (p !== current && existing.some(f => f.startsWith(`${p}/`)))
    return '已存在同名目录'
  if (p !== current && existing.some(f => p.startsWith(`${f}/`)))
    return `「${existing.find(f => p.startsWith(`${f}/`))}」是文件，不能作为目录`
  if (current === undefined && existing.length >= MAX_FILES)
    return `最多 ${MAX_FILES} 个文件`
  return ''
}

export interface TreeEntry {
  /** Full path for files; folder path for folders. */
  path: string
  name: string
  depth: number
  kind: 'file' | 'folder'
}

/** Flattened tree (folders first, alphabetical) for rendering with indentation. */
export function buildTree(paths: readonly string[]): TreeEntry[] {
  interface Node { folders: Map<string, Node>, files: string[] }
  const root: Node = { folders: new Map(), files: [] }
  for (const p of paths) {
    const parts = p.split('/')
    let node = root
    for (const seg of parts.slice(0, -1)) {
      let next = node.folders.get(seg)
      if (!next) {
        next = { folders: new Map(), files: [] }
        node.folders.set(seg, next)
      }
      node = next
    }
    node.files.push(parts[parts.length - 1]!)
  }
  const out: TreeEntry[] = []
  const walk = (node: Node, prefix: string, depth: number) => {
    for (const name of [...node.folders.keys()].sort()) {
      const path = prefix ? `${prefix}/${name}` : name
      out.push({ path, name, depth, kind: 'folder' })
      walk(node.folders.get(name)!, path, depth + 1)
    }
    for (const name of [...node.files].sort())
      out.push({ path: prefix ? `${prefix}/${name}` : name, name, depth, kind: 'file' })
  }
  walk(root, '', 0)
  return out
}

/** UTF-8 size of a string. */
export function byteLength(s: string): number {
  return new TextEncoder().encode(s).length
}

/** Whether two file maps differ (paths or contents). */
export function filesDiffer(a: Record<string, string>, b: Record<string, string>): boolean {
  const ka = Object.keys(a)
  if (ka.length !== Object.keys(b).length)
    return true
  return ka.some(k => !Object.prototype.hasOwnProperty.call(b, k) || a[k] !== b[k])
}

/** Starter content for new files. */
export function starterContent(path: string): string {
  if (/^tests\/.+\.json$/.test(path)) {
    return `${JSON.stringify({
      name: '新用例',
      capability: 'models.list',
      secrets: { apiKey: 'sk-test' },
      fetch: [{ match: { method: 'GET', url: 'https://api.example.com/v1/models' }, response: { status: 200, json: { data: [] } } }],
      expect: { output: { models: [] } },
    }, null, 2)}\n`
  }
  if (path.endsWith('.json'))
    return '{}\n'
  if (/\.(?:ts|js)$/.test(path))
    return 'export {}\n'
  return ''
}
