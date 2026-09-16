import { useEffect, useRef, useState } from 'react'
import { ExternalLink, Search } from 'lucide-react'
import type { FormEvent } from 'react'
import { apiFetch } from './api'

type NetworkSearchResult = { title: string; url: string; snippet?: string; provider?: string }

async function responseJSON<T>(response: Response): Promise<T> {
  if (!response.ok) throw new Error('search unavailable')
  return response.json() as Promise<T>
}

export function NetworkSearchPanel({ onSelectURL }: { onSelectURL: (url: string) => void }) {
  const [query, setQuery] = useState('')
  const [items, setItems] = useState<NetworkSearchResult[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => { controller.current?.abort() }, [])

  async function search(event: FormEvent) {
    event.preventDefault()
    const trimmed = query.trim()
    if (trimmed.length < 2 || controller.current) return
    const request = new AbortController()
    controller.current = request
    setBusy(true); setItems([]); setError('')
    try {
      const response = await apiFetch('/api/network/sources/search', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, signal: request.signal,
        body: JSON.stringify({ query: trimmed, limit: 10 }),
      })
      const data = await responseJSON<{ items?: NetworkSearchResult[] }>(response)
      if (!request.signal.aborted) setItems(data.items ?? [])
    } catch {
      if (!request.signal.aborted) setError('公开来源暂时无法读取，请稍后重试。')
    } finally {
      if (!request.signal.aborted) { controller.current = null; setBusy(false) }
    }
  }

  function cancel() {
    controller.current?.abort(); controller.current = null
    setBusy(false); setError('搜索已取消。')
  }

  return <section className="source-probe network-search" aria-labelledby="network-search-title">
    <h2 id="network-search-title">公开素材检索</h2>
    <p>只返回公开来源元数据。搜索结果不代表许可证或可再分发权，选入下载计划前仍需核实。</p>
    <form className="source-probe-row" onSubmit={search}>
      <input required minLength={2} maxLength={200} value={query} readOnly={busy} onChange={(event) => { setQuery(event.target.value); setItems([]); setError('') }} placeholder="例如：免版权自然风景图片" aria-label="网络素材关键词" />
      <button className="secondary-button" disabled={query.trim().length < 2 || busy}><Search size={14} />{busy ? '搜索中…' : '搜索公开来源'}</button>
      {busy && <button className="secondary-button" type="button" onClick={cancel}>取消搜索</button>}
    </form>
    {error && <p role="status">{error}</p>}
    {items.length > 0 && <div className="network-search-results" role="list" aria-label="公开来源搜索结果">{items.map((item) => <article className="network-search-result" role="listitem" key={item.url}><div><h3>{item.title || '未命名来源'}</h3>{item.snippet && <p>{item.snippet}</p>}<small><span>{item.provider || '公开来源'}</span><span>许可证待确认</span><a href={item.url} target="_blank" rel="noreferrer"><ExternalLink size={12} />查看来源</a></small></div><button className="text-button" onClick={() => onSelectURL(item.url)}>用于下载计划</button></article>)}</div>}
    {!busy && !error && query.trim().length >= 2 && items.length === 0 && <p className="readonly-empty">暂无可用公开来源。</p>}
  </section>
}
