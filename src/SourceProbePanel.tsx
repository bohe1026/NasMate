import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { apiFetch } from './api'
import { formatBytes } from './format'

type SourceProbe = { accessible: boolean; statusCode?: number; contentType?: string; sizeBytes?: number; error?: string }
const errors: Record<string, string> = {
  POLICY_BLOCKED: '已拦截非公网地址，不会访问 NAS 或局域网服务。',
  VALIDATION_FAILED: '请输入不含凭据的公开 HTTP(S) URL。',
  TOOL_TIMEOUT: '来源响应超时，请稍后重试。',
  USER_CANCELLED: '探测已取消。',
  NETWORK_ERROR: '来源暂时无法访问，未创建任何下载任务。',
}

export function SourceProbePanel() {
  const [url, setURL] = useState('')
  const [result, setResult] = useState<SourceProbe | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => { controller.current?.abort() }, [])

  async function probe(event: FormEvent) {
    event.preventDefault()
    if (!url.trim() || controller.current) return
    const request = new AbortController()
    controller.current = request
    setBusy(true); setResult(null); setError('')
    try {
      const response = await apiFetch('/api/network/sources/probe', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sources: [{ url: url.trim() }] }), signal: request.signal,
      })
      if (!response.ok) throw new Error('probe unavailable')
      const data = await response.json() as { items?: SourceProbe[] }
      if (!data.items?.length) throw new Error('probe response missing')
      if (!request.signal.aborted) setResult(data.items[0])
    } catch {
      if (!request.signal.aborted) setError(errors.NETWORK_ERROR)
    } finally {
      if (!request.signal.aborted) { controller.current = null; setBusy(false) }
    }
  }

  function cancel() {
    controller.current?.abort(); controller.current = null
    setBusy(false); setError(errors.USER_CANCELLED)
  }

  return <section className="source-probe" aria-labelledby="source-probe-title">
    <h2 id="source-probe-title">来源连通性检查</h2>
    <p>仅请求公开地址的响应头，不下载文件。可访问不代表文件安全，也不代表已获得使用许可。</p>
    <form className="source-probe-row" onSubmit={probe}>
      <input type="url" required maxLength={4096} value={url} readOnly={busy} onChange={(event) => { setURL(event.target.value); setResult(null); setError('') }} placeholder="https://example.com/file.zip" aria-label="待探测的来源 URL" />
      <button className="secondary-button" disabled={!url.trim() || busy}>{busy ? '探测中…' : '开始探测'}</button>
      {busy && <button className="secondary-button" type="button" onClick={cancel}>取消探测</button>}
    </form>
    {error && <p role="status">{error}</p>}
    {result && <div className={`probe-result ${result.accessible ? 'is-ok' : 'is-error'}`} role="status">
      <strong>{result.accessible ? '来源可访问' : '来源不可访问'}</strong>
      {result.statusCode && <span>HTTP {result.statusCode}</span>}
      {result.contentType && <span>{result.contentType}</span>}
      <span>{typeof result.sizeBytes === 'number' && result.sizeBytes >= 0 ? formatBytes(result.sizeBytes) : '大小未知'}</span>
      {!result.accessible && <span>{result.statusCode && result.statusCode >= 300 && result.statusCode < 400 ? '来源要求重定向，未跟随跳转；请核实最终公开地址后重新探测。' : errors[result.error ?? 'NETWORK_ERROR'] ?? errors.NETWORK_ERROR}</span>}
    </div>}
  </section>
}
