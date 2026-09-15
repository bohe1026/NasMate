import { beforeEach, describe, expect, it, vi } from 'vitest'

const sdk = vi.hoisted(() => ({
  init: vi.fn(),
  useCapacity: vi.fn(),
}))

vi.mock('@ugreen-nas/core', () => ({ default: { init: sdk.init } }))
vi.mock('@ugreen-nas/core/cloudWindow', () => ({ default: { useCapacity: sdk.useCapacity } }))

beforeEach(() => {
  vi.resetModules()
  vi.unstubAllGlobals()
  sdk.init.mockReset()
  sdk.useCapacity.mockReset()
})

describe('UGOS API authentication', () => {
  it('does not block local Vite requests when UGOS initialization never resolves', async () => {
    sdk.init.mockReturnValue(new Promise(() => {}))
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ status: 'ok' }))
    vi.stubGlobal('fetch', fetchMock)
    const { apiFetch } = await import('./api')
    await expect(apiFetch('/api/health')).resolves.toBeInstanceOf(Response)
    expect(fetchMock).toHaveBeenCalledOnce()
    expect(sdk.useCapacity).not.toHaveBeenCalled()
  })

  it('adds the UGOS token when initialization succeeds', async () => {
    sdk.init.mockResolvedValue(undefined)
    sdk.useCapacity.mockResolvedValue({ third_token: 'safe-token' })
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ status: 'ok' }))
    vi.stubGlobal('fetch', fetchMock)
    const { apiFetch } = await import('./api')
    await apiFetch('/api/health')
    expect(fetchMock).toHaveBeenCalledOnce()
    const headers = fetchMock.mock.calls[0][1].headers as Headers
    expect(headers.get('Ugreen-Ttk')).toBe('safe-token')
  })

  it('does not block local Vite requests when token capacity never resolves', async () => {
    sdk.init.mockResolvedValue(undefined)
    sdk.useCapacity.mockReturnValue(new Promise(() => {}))
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ status: 'ok' }))
    vi.stubGlobal('fetch', fetchMock)
    const { apiFetch } = await import('./api')
    await expect(apiFetch('/api/health')).resolves.toBeInstanceOf(Response)
    expect(fetchMock).toHaveBeenCalledOnce()
  })
})
