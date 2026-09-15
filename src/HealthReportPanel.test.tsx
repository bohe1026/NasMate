import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { apiFetch } from './api'
import { HealthReportPanel } from './HealthReportPanel'

vi.mock('./api', () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)

beforeEach(() => {
  fetchMock.mockReset()
  fetchMock.mockImplementation(async (input) => String(input).startsWith('/api/artifacts') ? Response.json({ items: [{ name: 'health-report-20260915-020000.json', sizeBytes: 512, createdAt: '2026-09-15T02:00:00Z' }] }) : Response.json({
    generatedAt: '2026-09-15T02:00:00Z',
    storage: { suggestions: ['家庭影像增长较快'] },
    docker: [{ container: { name: 'paperless', status: 'unhealthy' }, findings: ['健康检查返回 503'] }],
    backup: { name: '夜间备份', lastRunStatus: 'success', recoveryReady: true },
    failedTasks: 2,
    failedDownloads: 1,
    fastestGrowing: [{ path: '/photos', deltaBytes: 200 * 1024 * 1024 }],
    warnings: ['有一个容器需要关注'],
    readOnly: true,
  }))
})

describe('health report snapshot', () => {
  it('shows report warnings and subsystem summaries as read-only information', async () => {
    render(<HealthReportPanel />)
    await screen.findByText('有一个容器需要关注')
    expect(screen.getByText('失败任务 2')).toBeTruthy()
    expect(screen.getByText('失败下载 1')).toBeTruthy()
    expect(screen.getByText('/photos')).toBeTruthy()
    expect(screen.getByText('paperless')).toBeTruthy()
    expect(screen.getByText('夜间备份')).toBeTruthy()
    expect(screen.getByText('家庭影像增长较快')).toBeTruthy()
    expect(screen.getByText(/只读报告/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: /修复|重启|执行/ })).toBeNull()
    expect(fetchMock).toHaveBeenCalledWith('/api/reports/health')
  })

  it('refreshes the snapshot on demand', async () => {
    const user = userEvent.setup()
    render(<HealthReportPanel />)
    await screen.findByText('有一个容器需要关注')
    await user.click(screen.getByRole('button', { name: '刷新健康日报' }))
    expect(fetchMock).toHaveBeenCalledTimes(4)
  })

  it('shows links for historical report artifacts', async () => {
    render(<HealthReportPanel />)
    await screen.findByText('health-report-20260915-020000.json')
    const link = screen.getByRole('link', { name: '查看 / 下载' })
    expect(link.getAttribute('href')).toBe('/api/artifacts/health-report-20260915-020000.json')
  })
})
