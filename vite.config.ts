import { resolve } from 'path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Plain Vite config consumed by Wails v2 (see wails.json frontend:build).
// The Wails embed directive lives at main.go: //go:embed all:frontend/dist,
// so this build must emit into <repo root>/frontend/dist.
export default defineConfig({
  root: resolve('src/renderer'),
  plugins: [react()],
  resolve: {
    alias: {
      '@renderer': resolve('src/renderer/src')
    }
  },
  build: {
    outDir: resolve('frontend/dist'),
    emptyOutDir: true
  },
  server: {
    port: 5173,
    strictPort: false
  }
})
