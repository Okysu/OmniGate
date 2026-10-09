// Monaco setup for the plugin editor. Only imported dynamically from
// PluginEditorView, so Monaco never ends up in the main bundle or other routes.
//
// We load the editor core plus the TypeScript/JavaScript, JSON and Markdown
// language support instead of the full `monaco-editor` entry (which registers
// ~80 languages, CSS/HTML services and an LSP client we do not need).
//
// Note (monaco-editor ≥ 0.55): `monaco.languages.typescript` is a deprecated
// stub; the TypeScript defaults live in the top-level `typescript` namespace,
// exported from languages/features/typescript/register. The API is the same
// (`typescriptDefaults.addExtraLib`, `setCompilerOptions`, ...).
import * as monaco from 'monaco-editor/editor/editor.api.js'
// Editor contributions (find, suggest, hover, folding, ...). This is the same
// module the TS/JSON language services import; loading it up front makes the
// contributions available before the first editor is constructed.
import 'monaco-editor/internal/common/workers.js'
import 'monaco-editor/languages/definitions/typescript/register.js'
import 'monaco-editor/languages/definitions/javascript/register.js'
import 'monaco-editor/languages/definitions/markdown/register.js'
import 'monaco-editor/languages/features/json/register.js'
import { ModuleKind, ModuleResolutionKind, ScriptTarget, typescriptDefaults } from 'monaco-editor/languages/features/typescript/register.js'
import EditorWorker from 'monaco-editor/editor/editor.worker.js?worker'
import JsonWorker from 'monaco-editor/language/json/json.worker.js?worker'
import TsWorker from 'monaco-editor/language/typescript/ts.worker.js?worker'
import { pluginsApi } from '@/lib/endpoints'

declare global {
  interface Window {
    MonacoEnvironment?: { getWorker: (workerId: string, label: string) => Worker }
  }
}

window.MonacoEnvironment = {
  getWorker(_workerId: string, label: string): Worker {
    if (label === 'json')
      return new JsonWorker()
    if (label === 'typescript' || label === 'javascript')
      return new TsWorker()
    return new EditorWorker()
  },
}

typescriptDefaults.setCompilerOptions({
  target: ScriptTarget.ES2017,
  module: ModuleKind.ESNext,
  moduleResolution: ModuleResolutionKind.NodeJs,
  strict: true,
  noEmit: true,
  allowNonTsExtensions: true,
  // The sandbox has no DOM, timers, require or process: only the language
  // built-ins and the `og` host API from the SDK declarations.
  lib: ['es2020'],
})
typescriptDefaults.setDiagnosticsOptions({ noSemanticValidation: false, noSyntaxValidation: false })
// Sync every model so relative imports between package files type-check.
typescriptDefaults.setEagerModelSync(true)

export const SDK_TYPES_URI = 'file:///node_modules/@omnigate/plugin-sdk/index.d.ts'

let sdkPromise: Promise<boolean> | null = null

/** Fetches `/api/plugins/sdk.d.ts` once and registers it (module `@omnigate/plugin-sdk` + global `og`). */
export function loadSdkTypes(): Promise<boolean> {
  sdkPromise ??= pluginsApi.sdkTypes()
    .then((dts) => {
      typescriptDefaults.addExtraLib(dts, SDK_TYPES_URI)
      return true
    })
    .catch((err: unknown) => {
      sdkPromise = null
      throw err
    })
  return sdkPromise
}

export function languageForPath(path: string): string {
  if (/\.(?:ts|mts|cts)$/.test(path))
    return 'typescript'
  if (/\.(?:js|mjs|cjs)$/.test(path))
    return 'javascript'
  if (path.endsWith('.json'))
    return 'json'
  if (path.endsWith('.md'))
    return 'markdown'
  return 'plaintext'
}

export function uriForPath(path: string): monaco.Uri {
  return monaco.Uri.parse(`file:///${path}`)
}

export { monaco }
export type Monaco = typeof monaco
