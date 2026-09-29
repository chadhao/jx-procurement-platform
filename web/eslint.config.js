// eslint 配置（批 0 · A8）——**只报不改**（lint 不带 --fix；修复另起任务，不与门禁绑定）。
// ★ 未接入 check_all.sh：现有代码未按本规则集整理过，直接挂必绿会常年红（README 定案
//   「必绿 vs 会报」分列纪律）；先作为本地/CI 可选工具，清完存量再考虑升门禁。
import js from '@eslint/js'
import pluginVue from 'eslint-plugin-vue'

export default [
  { ignores: ['dist/**', 'node_modules/**'] },
  js.configs.recommended,
  ...pluginVue.configs['flat/recommended'],
  {
    languageOptions: {
      ecmaVersion: 'latest',
      sourceType: 'module',
      globals: {
        window: 'readonly',
        document: 'readonly',
        navigator: 'readonly',
        sessionStorage: 'readonly',
        localStorage: 'readonly',
        fetch: 'readonly',
        URL: 'readonly',
        URLSearchParams: 'readonly',
        FormData: 'readonly',
        console: 'readonly',
        setTimeout: 'readonly',
        clearTimeout: 'readonly',
        setInterval: 'readonly',
        clearInterval: 'readonly',
        AbortController: 'readonly',
      },
    },
    rules: {
      // 现有代码风格既成事实（先报告问题、不制造百条风格噪声）：
      'vue/multi-word-component-names': 'off',
      'no-unused-vars': ['warn', { argsIgnorePattern: '^_' }],
      'no-console': 'off',
    },
  },
]
