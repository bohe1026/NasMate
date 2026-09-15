import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { apiFetch } from './api'
import { BackupPanel, DockerPanel, FileSearchPanel } from './ReadOnlyPanels'

vi.mock('./api', () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)

beforeEach(() => {
  fetchMock.mockReset()
  fetchMock.mockImplementation(async (input) => {
    const url = String(input)
    if (url.startsWith('/api/files/search')) return Response.json({ items: [{ name: 'report.pdf', path: '/photos/report.pdf', sizeBytes: 2048, modifiedAt: '2026-09-15T00:00:00Z', extension: '.pdf' }], total: 1, truncated: false, readMode: 'metadata-only' })
    if (url === '/api/docker/containers') return Response.json({ items: [{ container: { name: 'media-server', image: 'jellyfin:latest', status: 'running', cpuPercent: 2.4, memoryBytes: 1024, ports: ['8096:8096'], mounts: ['/photos:/media'] }, logs: ['health check ok'], findings: ['容器运行正常'] }], readOnly: true })
    if (url === '/api/backups/status') return Response.json({ name: '夜间备份', lastRunAt: '2026-09-15T01:00:00Z', lastRunStatus: 'success', target: '/backup', readable: true, sampledFiles: 12, verifiedFiles: 12, recoveryReady: true, recommendations: ['建议每月演练'] })
    if (url === '/api/backups/recovery-plan') return Response.json({ status: 'success', summary: '只读恢复演练计划', backupName: '夜间备份', target: '/backup', sampledFiles: 12, steps: ['使用新目录抽样恢复'], willOverwrite: false, requiresApproval: true, readOnly: true })
    return Response.json({})
  })
})

describe('file metadata search panel', () => {
  it('submits metadata filters and renders results without body content', async () => {
    const user = userEvent.setup()
    render(<FileSearchPanel />)
    await user.type(screen.getByRole('textbox', { name: '文件关键词' }), 'report')
    await user.type(screen.getByRole('textbox', { name: '授权目录' }), '/photos')
    await user.click(screen.getByRole('button', { name: '搜索文件' }))
    await screen.findByText('report.pdf')
    const call = fetchMock.mock.calls.find(([input]) => String(input).startsWith('/api/files/search'))
    expect(String(call?.[0])).toContain('keyword=report')
    expect(String(call?.[0])).toContain('rootPath=%2Fphotos')
    expect(screen.queryByText(/report contents/)).toBeNull()
  })

  it('cancels a pending search and shows no stale result', async () => {
    const user = userEvent.setup()
    let resolveRequest!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => { resolveRequest = resolve })
    fetchMock.mockImplementationOnce(async (_input, init) => {
      const signal = init?.signal as AbortSignal
      await new Promise<void>((resolve) => signal.addEventListener('abort', () => resolve(), { once: true }))
      return pending
    })
    render(<FileSearchPanel />)
    await user.type(screen.getByRole('textbox', { name: '文件关键词' }), 'report')
    await user.click(screen.getByRole('button', { name: '搜索文件' }))
    await user.click(await screen.findByRole('button', { name: '取消搜索' }))
    expect(screen.getByText('文件搜索已取消。')).toBeTruthy()
    resolveRequest(Response.json({ items: [{ name: 'stale.txt' }], total: 1 }))
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(screen.queryByText('stale.txt')).toBeNull()
  })
})

describe('docker read-only panel', () => {
  it('shows resource and log excerpts but does not expose mutation controls', async () => {
    render(<DockerPanel />)
    await screen.findByText('media-server')
    expect(screen.getByText('health check ok')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /重启|停止|删除|进入容器/ })).toBeNull()
  })
})

describe('backup verification panel', () => {
  it('shows verification counts and a read-only recovery plan', async () => {
    const user = userEvent.setup()
    render(<BackupPanel />)
    await screen.findByText('夜间备份')
    expect(screen.getByText('12 / 12')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: '查看恢复演练计划' }))
    await waitFor(() => expect(screen.getByText('只读恢复演练计划')).toBeTruthy())
    expect(screen.getByText('不会覆盖原文件')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /执行恢复|覆盖原文件/ })).toBeNull()
  })
})
