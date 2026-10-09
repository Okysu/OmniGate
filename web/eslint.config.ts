import js from '@eslint/js'
import { defineConfigWithVueTs, vueTsConfigs } from '@vue/eslint-config-typescript'
import pluginVue from 'eslint-plugin-vue'
import globals from 'globals'

export default defineConfigWithVueTs(
  {
    name: 'app/files-to-ignore',
    ignores: ['dist/**', 'coverage/**', 'node_modules/**'],
  },
  {
    name: 'app/globals',
    languageOptions: {
      globals: { ...globals.browser },
    },
  },
  js.configs.recommended,
  pluginVue.configs['flat/recommended'],
  vueTsConfigs.recommended,
  {
    name: 'app/rules',
    rules: {
      'vue/multi-word-component-names': 'off',
      // Layout is left to the author; this rule fights shadcn-style one-line components.
      'vue/max-attributes-per-line': 'off',
      'no-console': ['warn', { allow: ['warn', 'error'] }],
      '@typescript-eslint/consistent-type-imports': ['error', { prefer: 'type-imports', fixStyle: 'separate-type-imports' }],
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
    },
  },
  {
    // Generated shadcn-vue components: keep them close to upstream, relax style-only rules.
    name: 'app/shadcn-ui',
    files: ['src/components/ui/**'],
    rules: {
      'vue/require-default-prop': 'off',
      'vue/no-v-html': 'off',
    },
  },
  {
    name: 'app/node-configs',
    files: ['*.config.ts'],
    languageOptions: {
      globals: { ...globals.node },
    },
  },
)
