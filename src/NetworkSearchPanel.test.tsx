import { beforeEach, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { apiFetch } from './api'
import { NetworkSearchPanel } from './NetworkSearchPanel'

vi.mock('./api', () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)

beforeEach(() => {
  fetchMock.mockReset()
  fetchMock.mockResolvedValue(Response.json({
    items: [{ title: 'Public photo', url: 'https://example.com/photo.jpg', snippet: 'A public source result', provider: 'DuckDuckGo' }],
    untrusted: true,
  }))
})

it('searches public sources and only fills the download draft after user selection', async () => {
  const selected = vi.fn()
  const user = userEvent.setup()
  render(<NetworkSearchPanel onSelectURL={selected} />)
  await user.type(screen.getByRole('textbox', { name: '网络素材关键词' }), 'nature photo')
  await user.click(screen.getByRole('button', { name: '搜索公开来源' }))
  await screen.findByText('Public photo')
  expect(screen.getByText('许可证待确认')).toBeTruthy()
  expect(selected).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: '用于下载计划' }))
  expect(selected).toHaveBeenCalledWith('https://example.com/photo.jpg')
  const call = fetchMock.mock.calls.find(([input]) => String(input) === '/api/network/sources/search')
  expect(call?.[1]?.method).toBe('POST')
})
