import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
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
