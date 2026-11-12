import { resolve } from 'path'
import { fileURLToPath } from 'url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Plain Vite config consumed by Wails v2 (see wails.json frontend:build).
// The Wails embed directive lives at main.go: //go:embed all:frontend/dist,
// so this build must emit into <repo root>/frontend/dist.
const repoRoot = fileURLToPath(new URL('../', import.meta.url))

export default defineConfig({
  root: resolve(repoRoot, 'src/renderer'),
  plugins: [react()],
  resolve: {
    alias: {
      '@renderer': resolve(repoRoot, 'src/renderer/src')
    }
  },
  build: {
    outDir: resolve(repoRoot, 'frontend/dist'),
    emptyOutDir: true
  },
  server: {
    port: 5173,
    strictPort: false
  }
})
