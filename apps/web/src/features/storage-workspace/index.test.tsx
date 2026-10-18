import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, test, vi } from 'vitest'
import { useState } from 'react'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { useAuthStore } from '@/stores/auth-store'
import { StorageWorkspace, type StorageTab } from './index'
import '@/styles/index.css'

const calls = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('@/lib/api-client', async (original) => ({
  ...(await original<typeof import('@/lib/api-client')>()),
  apiRequest: calls.request,
}))
afterEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().reset()
})

async function setup(initialTab?: string) {
  // The workspace is controlled now: the route owns the tab so the address bar
  // tracks it. A small stateful harness stands in for the route.
  function Harness() {
    const [tab, setTab] = useState<StorageTab>(
      initialTab === 'archive' ? 'archive' : 'pools'
    )
    return <StorageWorkspace tab={tab} onTabChange={setTab} />
  }
  useAuthStore.getState().setUser({
    id: 'admin',
    username: 'admin',
    role: 'admin',
    enabled: true,
    version: 1,
  })
  calls.request.mockImplementation(async (path: string) => {
    if (path.startsWith('/api/v1/storage-pools'))
      return { items: [], next_cursor: null, capacity: {} }
    if (path.startsWith('/api/v1/archive-targets'))
      return { items: [], next_cursor: null }
    if (path.startsWith('/api/v1/archive-policies')) return { items: [] }
    if (path.startsWith('/api/v1/channels'))
      return { items: [], next_cursor: null }
    if (path.startsWith('/api/v1/site'))
      return { timezone: 'Asia/Shanghai', version: 1 }
    return {}
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <Harness />
    </QueryClientProvider>
  )
}

/** Both panels are always rendered; exactly one carries aria-hidden="true". */
function panels(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>('[data-storage-panel]')]
}
function hiddenPanels(): number {
  return panels().filter((element) => element.getAttribute('aria-hidden') === 'true')
    .length
}

test('opens on the pools tab by default', async () => {
  const page = await setup()
  await expect
    .element(page.getByRole('button', { name: /^存储池/ }))
    .toHaveAttribute('aria-pressed', 'true')
})

test('opens on the archive tab when the link asks for it', async () => {
  // The settings page links here with ?tab=archive; landing on the pool list
  // would make the operator hunt for the page they were told to visit.
  const page = await setup('archive')
  await expect
    .element(page.getByRole('button', { name: /^归档目标/ }))
    .toHaveAttribute('aria-pressed', 'true')
  await expect
    .element(page.getByRole('button', { name: /^存储池/ }))
    .toHaveAttribute('aria-pressed', 'false')
})

test('keeps both panels mounted so a half-filled form survives a tab switch', async () => {
  await setup()
  // Both panels exist from the first paint; only one is hidden at a time.
  expect(panels().length).toBe(2)
  expect(hiddenPanels()).toBe(1)
})

test('switches which panel is visible without unmounting either', async () => {
  const page = await setup()
  const before = panels().length
  await userEvent.click(page.getByRole('button', { name: /^归档目标/ }))
  await expect
    .element(page.getByRole('button', { name: /^归档目标/ }))
    .toHaveAttribute('aria-pressed', 'true')
  // The node count is unchanged: switching tabs hides a panel, it does not
  // rebuild the other one (which would discard its open form and scroll).
  expect(panels().length).toBe(before)
  expect(hiddenPanels()).toBe(1)
})
