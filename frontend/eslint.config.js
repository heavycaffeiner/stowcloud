import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  {
    ignores: [
      'build/**',
      'coverage/**',
      'node_modules/**',
      'playwright-report/**',
      'src/api/generated/**',
      'test-results/**'
    ]
  },
  {
    files: ['**/*.{js,mjs,ts,tsx}'],
    languageOptions: {
      parser: tseslint.parser,
      globals: {
        ...globals.browser,
        ...globals.node,
        ...globals.serviceworker
      }
    },
    plugins: {
      'react-hooks': reactHooks
    }
  },
  {
    files: ['src/**/*.{ts,tsx}'],
    plugins: {
      'react-hooks': reactHooks
    },
    rules: {
      'react-hooks/rules-of-hooks': 'error',
      'react-hooks/exhaustive-deps': 'warn'
    }
  },
  {
    // mdui stays behind the ui layer so a component library swap touches src/ui only.
    files: ['src/**/*.{ts,tsx}'],
    ignores: ['src/ui/**'],
    rules: {
      'no-restricted-imports': [
        'error',
        { patterns: [{ group: ['mdui', 'mdui/*', '@mdui/*'], message: 'Import mdui only from src/ui.' }] }
      ],
      'no-restricted-syntax': [
        'error',
        {
          selector: 'JSXOpeningElement > JSXIdentifier[name=/^mdui-/]',
          message: 'Render mdui elements through a src/ui component.'
        }
      ]
    }
  }
)
