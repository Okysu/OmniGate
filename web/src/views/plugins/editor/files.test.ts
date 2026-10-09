import { describe, expect, it } from 'vitest'
import { buildTree, byteLength, filesDiffer, validatePath } from './files'

describe('validatePath', () => {
  const existing = ['manifest.json', 'src/index.ts', 'tests/a.json']
  it('accepts valid new paths', () => {
    expect(validatePath('src/util.ts', existing)).toBe('')
    expect(validatePath('README.md', existing)).toBe('')
  })
  it('rejects bad characters, absolute paths and dot segments', () => {
    expect(validatePath('/abs.ts', existing)).not.toBe('')
    expect(validatePath('src/../x.ts', existing)).not.toBe('')
    expect(validatePath('src/./x.ts', existing)).not.toBe('')
    expect(validatePath('src/中文.ts', existing)).not.toBe('')
    expect(validatePath('a b.ts', existing)).not.toBe('')
    expect(validatePath('src/', existing)).not.toBe('')
    expect(validatePath('', existing)).toBe('请输入文件路径')
  })
  it('rejects duplicates and file/folder clashes', () => {
    expect(validatePath('src/index.ts', existing)).toBe('已存在同名文件')
    expect(validatePath('src', existing)).toBe('已存在同名目录')
    expect(validatePath('manifest.json/x', existing)).toContain('是文件')
    // Renaming a file to itself is fine.
    expect(validatePath('src/index.ts', existing, 'src/index.ts')).toBe('')
  })
  it('enforces the file count limit', () => {
    const many = Array.from({ length: 64 }, (_, i) => `f${i}.ts`)
    expect(validatePath('new.ts', many)).toBe('最多 64 个文件')
  })
})

describe('buildTree', () => {
  it('lists folders first with depth', () => {
    expect(buildTree(['manifest.json', 'src/lib/a.ts', 'src/index.ts', 'README.md']).map(e => `${'  '.repeat(e.depth)}${e.kind === 'folder' ? `${e.name}/` : e.name}`)).toEqual([
      'src/',
      '  lib/',
      '    a.ts',
      '  index.ts',
      'README.md',
      'manifest.json',
    ])
  })
})

describe('helpers', () => {
  it('measures UTF-8 bytes', () => {
    expect(byteLength('abc')).toBe(3)
    expect(byteLength('余额')).toBe(6)
  })
  it('compares file maps', () => {
    expect(filesDiffer({ a: '1' }, { a: '1' })).toBe(false)
    expect(filesDiffer({ a: '1' }, { a: '2' })).toBe(true)
    expect(filesDiffer({ a: '1' }, { b: '1' })).toBe(true)
    expect(filesDiffer({ a: '1' }, { a: '1', b: '' })).toBe(true)
  })
})
