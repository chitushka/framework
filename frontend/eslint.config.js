import js from "@eslint/js"
import vue from "eslint-plugin-vue"
import globals from "globals"

export default [
  { ignores: ["node_modules/**", "playwright-report/**", "test-results/**"] },
  js.configs.recommended,
  ...vue.configs["flat/essential"],
  { files: ["src/**/*.{js,vue}"], languageOptions: { globals: globals.browser } },
  { files: ["*.js", "test/**/*.mjs", "e2e/**/*.js", "e2e-admin/**/*.js"], languageOptions: { globals: { ...globals.node, ...globals.browser } } },
]
