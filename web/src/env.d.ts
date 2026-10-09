/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Optional URL of the OmniGate documentation, shown as "文档" in the top bar. */
  readonly VITE_DOCS_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
