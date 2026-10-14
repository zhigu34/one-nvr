/// <reference types="vitest/config" />
import path from 'path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { tanstackRouter } from '@tanstack/router-plugin/vite'
import { playwright } from '@vitest/browser-playwright'

// https://vite.dev/config/
export default defineConfig({
  optimizeDeps: {
    include: [
      'zustand',
      '@tanstack/react-query',
      '@radix-ui/react-tabs',
      '@radix-ui/react-collapsible',
      // Pre-bundling keeps these on the same React instance as the test
      // renderer; an un-optimized Radix package pulls in its own copy and the
      // hooks dispatcher is null.
      '@radix-ui/react-dropdown-menu',
      '@radix-ui/react-switch',
    ],
  },
  server: {
    proxy: {
      '/api': {
        target: process.env.ONE_NVR_DEV_API || 'http://127.0.0.1:8081',
        changeOrigin: false,
      },
    },
  },
  plugins: [
    tanstackRouter({
      target: 'react',
      autoCodeSplitting: true,
    }),
    react(),
    tailwindcss(),
  ],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  test: {
    silent: 'passed-only',
    unstubEnvs: true,
    browser: {
      enabled: true,
      screenshotFailures: false,
      provider: playwright(),
      // Default to a desktop viewport so tests asserting on the wide-layout
      // file panel (testid='files-panel') cover the same path the operator
      // uses; the records-workspace vitest covers the narrow path separately
      // where needed.
      instances: [
        { browser: 'chromium', viewport: { width: 1440, height: 900 } },
      ],
    },
    coverage: {
      // include: ['src/**/*.{js,jsx,ts,tsx}'], // Uncomment to expand the report to all src/**/* so untested modules appear as 0% coverage.
      exclude: [
        'src/components/ui/**',
        'src/assets/**',
        'src/tanstack-table.d.ts',
        'src/routeTree.gen.ts',
        'src/test-utils/**',
        'src/routes/**',
      ],
    },
  },
})
