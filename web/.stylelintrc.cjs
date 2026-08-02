module.exports = {
  plugins: ['./tools/stylelint-four-px.cjs'],
  rules: {
    'sc/four-px-grid': true
  },
  overrides: [
    {
      files: ['**/*.svelte'],
      customSyntax: 'postcss-html'
    }
  ],
  ignoreFiles: ['build/**', '.svelte-kit/**', 'node_modules/**']
}
