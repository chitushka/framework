import { defineConfig } from "vite"
import vue from "@vitejs/plugin-vue"

export default defineConfig({
  plugins: [vue()],
  build: {
    manifest: true,
    outDir: "../public",
    emptyOutDir: true,
    rollupOptions: {
      input: "src/main.js",
    },
  },
  server: {
    port: 5173,
    strictPort: true,
  },
})
