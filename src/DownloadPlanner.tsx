import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { apiFetch } from './api'

export type DownloadPlan = {
  id: string; targetDirectory: string; estimatedBytes: number; status: string
  downloadedBytes?: number; currentSource?: string; errorCode?: string
  sources: { title: string; url: string; license: string; sizeBytes: number }[]
}
type SourceDraft = { id: number; url: string; license: string; bytes: string }
const emptySource = (id: number): SourceDraft => ({ id, url: '', license: '', bytes: '' })
const errorMessages: Record<string, string> = {
  AUTH_REQUIRED: '请重新登录 UGOS 后重试。',
  PATH_NOT_ALLOWED: '保存目录不在授权范围内，请在 UGOS 应用设置中检查目录授权。',
  STORAGE_INSUFFICIENT: '空间不足或大小上限过大，请减少文件后重试。',
  VALIDATION_FAILED: '请检查地址、文件扩展名、许可证或使用说明，以及每个文件的大小上限。',
  RATE_LIMITED: '操作过于频繁，请稍后重试。',
  POLICY_BLOCKED: '来源被安全策略拦截，请使用公开网络地址。',
}

export function DownloadPlanner({ onCreated }: { onCreated: (plan: DownloadPlan) => void }) {
  const [directory, setDirectory] = useState('')
  const [sources, setSources] = useState<SourceDraft[]>([emptySource(1)])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const nextID = useRef(2)
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => { controller.current?.abort() }, [])

  function update(id: number, field: 'url' | 'license' | 'bytes', value: string) {
    setSources((current) => current.map((source) => source.id === id ? { ...source, [field]: value } : source))
    setError('')
  }

  async function prepare(event: FormEvent) {
    event.preventDefault()
    if (controller.current) return
    if (!directory.trim() || sources.some((source) => !source.url.trim() || !source.license.trim() || !Number.isSafeInteger(Number(source.bytes)) || Number(source.bytes) < 1 || Number(source.bytes) > 10 * 1024 ** 3)) {
      setError(errorMessages.VALIDATION_FAILED); return
    }
    const request = new AbortController()
    controller.current = request
    setBusy(true); setError('')
    try {
      const response = await apiFetch('/api/downloads/prepare', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, signal: request.signal,
        body: JSON.stringify({ targetDirectory: directory.trim(), sources: sources.map((source) => ({ url: source.url.trim(), license: source.license.trim(), title: '', sizeBytes: Number(source.bytes) })) }),
      })
      if (!response.ok) {
        const data = await response.json() as { error?: { code?: string } }
        if (!request.signal.aborted) setError(errorMessages[data.error?.code ?? ''] ?? '计划未生成，请稍后重试。')
        return
      }
      const plan = await response.json() as DownloadPlan
      if (plan.status !== '待确认') throw new Error('unexpected plan state')
      if (!request.signal.aborted) { onCreated(plan); setSources([emptySource(nextID.current++)]) }
    } catch {
      if (!request.signal.aborted) setError('未能确认计划是否生成，请刷新下载列表后再重试；此操作不会自动下载。')
    } finally {
      if (!request.signal.aborted) { controller.current = null; setBusy(false) }
    }
  }

  return <section className="source-probe" aria-labelledby="download-planner-title">
    <h2 id="download-planner-title">创建下载计划</h2>
    <p>填写已有的授权目录。仅生成计划；每个文件大小上限会在实际下载时强制执行，之后仍需单独确认下载。</p>
    <form className="download-planner-form" onSubmit={prepare}>
      <label>保存目录<input required maxLength={4096} value={directory} disabled={busy} onChange={(event) => { setDirectory(event.target.value); setError('') }} placeholder="UGOS 已授权的目录" /></label>
      {sources.map((source, index) => <fieldset key={source.id} disabled={busy}>
        <legend>来源 {index + 1}</legend>
        <label>文件地址 {index + 1}<input type="url" required maxLength={4096} value={source.url} onChange={(event) => update(source.id, 'url', event.target.value)} placeholder="https://example.com/file.jpg" /></label>
        <label>许可证或使用说明 {index + 1}<input required maxLength={2000} value={source.license} onChange={(event) => update(source.id, 'license', event.target.value)} placeholder="例如 CC0，或来源提供的使用条件" /></label>
        <label>大小上限（字节）{index + 1}<input type="number" required min={1} max={10 * 1024 ** 3} step={1} value={source.bytes} onChange={(event) => update(source.id, 'bytes', event.target.value)} /></label>
        {sources.length > 1 && <button className="text-button" type="button" onClick={() => setSources((current) => current.filter((row) => row.id !== source.id))}>移除来源 {index + 1}</button>}
      </fieldset>)}
      <p>支持常见图片、音视频、PDF、文本、CSV、JSON 与 ZIP。不会运行、解压或自动打开下载文件；填写许可信息不代表 NasMate 保证第三方内容可再分发。</p>
      {error && <p role="alert">{error}</p>}
      <div className="heading-actions"><button className="secondary-button" type="button" disabled={busy || sources.length >= 50} onClick={() => setSources((current) => [...current, emptySource(nextID.current++)])}>添加来源</button><button className="secondary-button" disabled={busy}>{busy ? '生成中…' : '生成下载计划'}</button></div>
    </form>
  </section>
}
