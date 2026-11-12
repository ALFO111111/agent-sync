import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Capacitor 用 file/https 方案加载，资源路径必须是相对的。
export default defineConfig({
  base: './',
  plugins: [vue()],
  build: { outDir: 'dist', emptyOutDir: true },
})
