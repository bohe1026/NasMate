import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import App from './App'
import { apiFetch } from './api'

vi.mock('./api', () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
const runningTask = { id: 'task-1', prompt: '检查备份', status: '运行中', summary: '正在检查', updatedAt: '2026-09-15T00:00:00Z' }
let networkReply: object
let taskReply: object[]
let eventReads: number

beforeEach(() => {
  networkReply = { items: [{ url: 'https://example.com/file.jpg', accessible: true, statusCode: 200, contentType: 'image/jpeg', sizeBytes: 1024 }] }
  taskReply = []
  eventReads = 0
  fetchMock.mockReset()
  fetchMock.mockImplementation(async (input) => {
    const url = String(input)
    if (url === '/api/tasks') return Response.json({ items: taskReply })
    if (url === '/api/network/sources/probe') return Response.json(networkReply)
    if (url === '/api/tasks/task-1/cancel') return Response.json({ ...runningTask, status: '已取消' })
    if (url === '/api/tasks/task-1/events') return Response.json({ items: [{ id: ++eventReads, type: eventReads > 1 ? 'task.cancelled' : 'task.progress', data: {} }] })
    if (url === '/api/models/status') return Response.json({ provider: 'local', model: '规则规划器', configured: true, mode: 'local' })
    return Response.json({ items: [], volumes: [] })
  })
})

describe('network source workflow', () => {
  it('shows HTTP status, media type and size without creating a download', async () => {
    const user = userEvent.setup()
    render(<App />)
    await user.click(screen.getByRole('button', { name: '下载任务' }))
    await user.type(screen.getByRole('textbox', { name: '待探测的来源 URL' }), 'https://example.com/file.jpg')
    await user.click(screen.getByRole('button', { name: '开始探测' }))
    await screen.findByText('HTTP 200')
    expect(screen.getByText('image/jpeg')).toBeTruthy()
    expect(screen.getByText('1.0 KB')).toBeTruthy()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/downloads/prepare'))).toBe(false)
  })

  it('invalidates a successful probe when the URL changes', async () => {
    const user = userEvent.setup()
    render(<App />)
    await user.click(screen.getByRole('button', { name: '下载任务' }))
    const input = screen.getByRole('textbox', { name: '待探测的来源 URL' })
    await user.type(input, 'https://example.com/file.jpg')
    await user.click(screen.getByRole('button', { name: '开始探测' }))
    await screen.findByText('来源可访问')
    await user.clear(input)
    expect(screen.queryByText('来源可访问')).toBeNull()
  })

  it('explains blocked internal addresses without showing raw errors', async () => {
    networkReply = { items: [{ accessible: false, error: 'POLICY_BLOCKED', sizeBytes: -1 }] }
    const user = userEvent.setup()
    render(<App />)
    await user.click(screen.getByRole('button', { name: '下载任务' }))
    await user.type(screen.getByRole('textbox', { name: '待探测的来源 URL' }), 'http://127.0.0.1/file.jpg')
    await user.click(screen.getByRole('button', { name: '开始探测' }))
    await screen.findByText('已拦截非公网地址，不会访问 NAS 或局域网服务。')
  })
})

it('refreshes the event trail after cancelling a task', async () => {
  taskReply = [runningTask]
  const user = userEvent.setup()
  render(<App />)
  await user.click(await screen.findByRole('button', { name: '检查备份' }))
  await waitFor(() => expect(eventReads).toBe(1))
  await user.click(screen.getByRole('button', { name: '取消任务' }))
  await screen.findByText('已取消')
  await waitFor(() => expect(eventReads).toBe(2))
  expect(screen.getByText('task.cancelled')).toBeTruthy()
})

it('polls a running task until its final status and event trail appear', async () => {
  taskReply = [runningTask]
  const fallback = fetchMock.getMockImplementation()!
  fetchMock.mockImplementation(async (input, init) => String(input) === '/api/tasks/task-1'
    ? Response.json({ ...runningTask, status: '已完成', summary: '只读检查完成' })
    : fallback(input, init))
  render(<App />)
  const taskButton = await screen.findByRole('button', { name: '检查备份' })
  vi.useFakeTimers()
  try {
    fireEvent.click(taskButton)
    await act(async () => { await vi.advanceTimersByTimeAsync(2200) })
    expect(screen.getByText('已完成')).toBeTruthy()
    expect(eventReads).toBeGreaterThan(1)
  } finally {
    vi.useRealTimers()
  }
})

it('offers a manual retry for an interrupted task without replaying it automatically', async () => {
  taskReply = [{ ...runningTask, status: '失败', summary: '服务重启中断，未继续执行；请重新发起任务' }]
  const user = userEvent.setup()
  render(<App />)
  await user.click(await screen.findByRole('button', { name: '检查备份' }))
  await user.click(screen.getByRole('button', { name: '重新发起任务' }))
  expect(screen.getByRole('textbox', { name: '描述你的 NAS 任务' })).toHaveProperty('value', '检查备份')
  expect(fetchMock.mock.calls.some(([input, init]) => String(input) === '/api/tasks' && init?.method === 'POST')).toBe(false)
})

it('explains interrupted downloads without silently reusing approval', async () => {
  const interrupted = { id: 'download-1', targetDirectory: '/photos', sources: [{ title: '素材', url: 'https://example.com/image.jpg', license: 'CC0', sizeBytes: 1024 }], estimatedBytes: 1024, status: '失败', errorCode: 'TASK_INTERRUPTED' }
  const fallback = fetchMock.getMockImplementation()!
  fetchMock.mockImplementation(async (input, init) => String(input).startsWith('/api/downloads?') ? Response.json({ items: [interrupted] }) : fallback(input, init))
  const user = userEvent.setup()
  render(<App />)
  await user.click(screen.getByRole('button', { name: '下载任务' }))
  await screen.findByText('服务重启中断，请重新生成下载计划并审批。')
  expect(screen.queryByRole('button', { name: '确认下载' })).toBeNull()
})

it('shows the complete dry run preview without executing file changes', async () => {
  const result = {
    dryRun: true, summary: '已生成批量整理预览，未修改任何文件', spaceDelta: 0,
    items: Array.from({ length: 12 }, (_, i) => ({ source: `/photos/image-${i}.jpg`, destination: `/photos/2026-09/image-${i}.jpg`, reason: '按日期整理' })),
    conflicts: ['/photos/2026-09/existing.jpg'], skipped: ['/photos/already-filed.jpg'],
  }
  const fallback = fetchMock.getMockImplementation()!
  fetchMock.mockImplementation(async (input, init) => String(input) === '/api/organize/dry-run' ? Response.json(result) : fallback(input, init))
  const user = userEvent.setup()
  render(<App />)
  await user.click(screen.getByRole('button', { name: '常用能力' }))
  await user.type(screen.getByRole('textbox', { name: '整理授权目录' }), '/photos')
  await user.click(screen.getByRole('button', { name: '生成预览' }))
  await screen.findByText('/photos/2026-09/image-11.jpg')
  expect(screen.getByText('/photos/2026-09/existing.jpg')).toBeTruthy()
  expect(screen.getByText('/photos/already-filed.jpg')).toBeTruthy()
  expect(screen.queryByRole('button', { name: /执行整理/ })).toBeNull()
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST').map(([url]) => url)).toEqual(['/api/organize/dry-run'])
})

it('creates a download plan and waits for a separate user approval', async () => {
  const planned = { id: 'download-1', targetDirectory: '/photos', sources: [{ title: '素材', url: 'https://example.com/image.jpg', license: 'CC0', sizeBytes: 1024 }], estimatedBytes: 1024, status: '待确认' }
  const fallback = fetchMock.getMockImplementation()!
  fetchMock.mockImplementation(async (input, init) => {
    if (String(input) === '/api/downloads/prepare') return Response.json(planned, { status: 201 })
    if (String(input) === '/api/downloads/download-1?action=deny') return Response.json({ ...planned, status: '已取消' })
    return fallback(input, init)
  })
  const user = userEvent.setup()
  render(<App />)
  await user.click(screen.getByRole('button', { name: '下载任务' }))
  await user.type(screen.getByRole('textbox', { name: '保存目录' }), '/photos')
  await user.type(screen.getByRole('textbox', { name: '文件地址 1' }), 'https://example.com/image.jpg')
  await user.type(screen.getByRole('textbox', { name: '许可证或使用说明 1' }), 'CC0')
  await user.type(screen.getByRole('spinbutton', { name: '大小上限（字节）1' }), '1024')
  await user.click(screen.getByRole('button', { name: '生成下载计划' }))
  await screen.findByRole('button', { name: '确认下载' })
  expect(fetchMock.mock.calls.some(([url]) => String(url).includes('action=approve'))).toBe(false)
  await user.click(screen.getByRole('button', { name: '拒绝' }))
  await screen.findByText('已取消')
  expect(fetchMock.mock.calls.some(([url]) => String(url).includes('action=approve'))).toBe(false)
})
