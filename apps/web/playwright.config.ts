import { defineConfig } from '@playwright/test'
import { readFileSync } from 'node:fs'

if (process.env.ONE_NVR_E2E_SECRETS_FILE) {
  process.env.ONE_NVR_E2E_SETUP_TOKEN=JSON.parse(readFileSync(process.env.ONE_NVR_E2E_SECRETS_FILE,'utf8')).setup_token
}

export default defineConfig({
  testDir: '../../tests/e2e',
  testMatch: process.env.ONE_NVR_E2E_PHASE==='protocol'?'**/protocol.spec.ts':process.env.ONE_NVR_E2E_PHASE==='restart'?'**/restart.spec.ts':'**/m1a.spec.ts',
  timeout: 30000,
  workers: 1,
  retries: 0,
  reporter: 'list',
  // Screenshots/traces may contain setup token, passwords or PEM inputs.
  use: { ignoreHTTPSErrors: process.env.ONE_NVR_E2E_PHASE==='protocol', baseURL: process.env.ONE_NVR_E2E_URL || 'http://gateway', screenshot:'off', trace:'off', video:'off' },
})
