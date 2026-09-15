import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { apiFetch } from './api'
import { KnowledgeIndexPanel } from './KnowledgeIndexPanel'

vi.mock('./api', () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)

beforeEach(() => {
  fetchMock.mockReset()
  fetchMock.mockImplementation(async (input) => {
    const url = String(input)
    if (url === '/api/index/status') return Response.json({ status: 'available', mode: 'metadata-only', roots: ['/photos'], bodyIndexEnabled: false, ocrEnabled: false, mediaTranscriptionEnabled: false, requiresExplicitConsent: true })
    if (url === '/api/index/rebuild') return Response.json({ status: 'success', summary: '元数据索引已重建', itemCount: 18, readOnly: true }, { status: 201 })
    if (url.startsWith('/api/index/search')) return Response.json({ items: [{ name: 'notes.md', path: '/photos/notes.md', sizeBytes: 128, modifiedAt: '2026-09-15T00:00:00Z', extension: '.md' }], total: 1, readMode: 'metadata-only', source: 'local-index' })
    return Response.json({})
  })
})

describe('local knowledge index panel', () => {
  it('keeps indexing metadata-only and requires explicit consent for body processing', async () => {
    render(<KnowledgeIndexPanel />)
    await screen.findByText('元数据模式')
    expect(screen.getByText('正文索引、OCR、音视频转写均已关闭')).toBeTruthy()
    expect(screen.getByText('/photos')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /启用正文|开启 OCR|转写视频/ })).toBeNull()
  })

  it('rebuilds the local index and searches names without reading file bodies', async () => {
    const user = userEvent.setup()
    render(<KnowledgeIndexPanel />)
    await user.click(screen.getByRole('button', { name: '重建元数据索引' }))
    await screen.findByText(/已重建.*18/)
    const rebuildCall = fetchMock.mock.calls.find(([input]) => String(input) === '/api/index/rebuild')
    expect(rebuildCall?.[1]?.method).toBe('POST')
    await user.type(screen.getByRole('textbox', { name: '索引关键词' }), 'notes')
    await user.click(screen.getByRole('button', { name: '搜索索引' }))
    await screen.findByText('notes.md')
    const searchCall = fetchMock.mock.calls.find(([input]) => String(input).startsWith('/api/index/search'))
    expect(String(searchCall?.[0])).toContain('keyword=notes')
    expect(screen.queryByText(/file body|正文内容/)).toBeNull()
    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input) === '/api/index/rebuild')).toBe(true))
  })
})
