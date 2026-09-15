import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { apiFetch } from './api'
import { formatBytes } from './format'

type OrganizePlan = {
  summary: string; dryRun: boolean; spaceDelta: number; truncated: boolean
  items: { source: string; destination: string; reason: string }[]
  conflicts: string[]; skipped: string[]
}

export function OrganizePanel() {
  const [root, setRoot] = useState('')
  const [mode, setMode] = useState('date')
  const [plan, setPlan] = useState<OrganizePlan | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => { controller.current?.abort() }, [])

  async function preview(event: FormEvent) {
    event.preventDefault()
    if (!root.trim() || controller.current) return
    const request = new AbortController()
    controller.current = request
    setBusy(true); setPlan(null); setError('')
    try {
      const response = await apiFetch('/api/organize/dry-run', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ rootPath: root.trim(), mode }), signal: request.signal,
      })
      if (!response.ok) throw new Error('preview unavailable')
      const result = await response.json() as OrganizePlan
      if (!result.dryRun) throw new Error('not a dry run')
      if (!request.signal.aborted) setPlan(result)
    } catch {
      if (!request.signal.aborted) setError('无法生成预览，请确认目录已授权、可读取后重试。')
    } finally {
      if (!request.signal.aborted) { controller.current = null; setBusy(false) }
    }
  }

  return <section className="source-probe" aria-labelledby="organize-title">
    <h2 id="organize-title">批量整理 Dry Run</h2>
    <p>只读取元数据并生成建议，不会移动、重命名、压缩或删除文件；日期按文件修改时间归类。</p>
    <form className="source-probe-row" onSubmit={preview}>
      <input aria-label="整理授权目录" placeholder="输入 UGOS 已授权的目录" maxLength={4096} required readOnly={busy} value={root} onChange={(event) => { setRoot(event.target.value); setPlan(null) }} />
      <select aria-label="整理方式" value={mode} disabled={busy} onChange={(event) => { setMode(event.target.value); setPlan(null) }}><option value="date">按修改月份</option><option value="extension">按扩展名</option></select>
      <button className="secondary-button" disabled={!root.trim() || busy}>{busy ? '生成中…' : '生成预览'}</button>
      {busy && <button type="button" className="secondary-button" onClick={() => { controller.current?.abort(); controller.current = null; setBusy(false); setError('预览已取消，文件未变更。') }}>取消预览</button>}
    </form>
    {error && <p role="alert">{error}</p>}
    {plan && <div className="organize-result">
      <p role="status">{plan.summary} · {plan.items.length} 项建议 · {plan.conflicts.length} 项冲突 · {plan.skipped.length} 项跳过 · 空间变化 {formatBytes(plan.spaceDelta)}</p>
      {plan.truncated && <p role="status">扫描达到上限，仅展示部分文件；请缩小目录范围后重新预览。</p>}
      <div className="organize-table"><table><caption>全部返回的整理建议（最多 1000 项）</caption><thead><tr><th>原路径</th><th>目标路径</th><th>说明</th></tr></thead><tbody>{plan.items.map((item) => <tr key={item.source}><td>{item.source}</td><td>{item.destination}</td><td>{item.reason}</td></tr>)}</tbody></table></div>
      {!!plan.conflicts.length && <details open><summary>冲突项（不覆盖）</summary><ul>{plan.conflicts.map((path, i) => <li key={`${path}-${i}`}>{path}</li>)}</ul></details>}
      {!!plan.skipped.length && <details open><summary>已归档或不可安全处理的跳过项</summary><ul>{plan.skipped.map((path, i) => <li key={`${path}-${i}`}>{path}</li>)}</ul></details>}
      <p>未执行任何文件变更，无需撤销；这些建议不能直接用于覆盖已有文件。</p>
    </div>}
  </section>
}
