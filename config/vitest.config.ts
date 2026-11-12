import { resolve } from 'path'
import { fileURLToPath } from 'url'
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

const repoRoot = fileURLToPath(new URL('../', import.meta.url))

export default defineConfig({
  root: repoRoot,
  plugins: [react()],
  test: {
    environment: 'node',
    // jsdom tests assert behavior using stable feature class names. Production
    // Vite still hashes CSS Modules; stylesheet equivalence is checked separately.
    css: { modules: { classNameStrategy: 'non-scoped' } },
    include: ['tests/**/*.test.{ts,tsx}'],
    setupFiles: ['tests/support/setup.ts']
  },
  resolve: {
    alias: {
      '@shared': resolve(repoRoot, 'src/shared')
    }
  }
})
