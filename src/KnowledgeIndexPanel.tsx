import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Database, RefreshCw, Search, ShieldCheck } from 'lucide-react'
import { apiFetch } from './api'
import { formatBytes } from './format'

type IndexStatus = {
  status?: string
  mode?: string
  roots?: string[]
  bodyIndexEnabled?: boolean
  ocrEnabled?: boolean
  mediaTranscriptionEnabled?: boolean
  requiresExplicitConsent?: boolean
  generatedAt?: string
  itemCount?: number
  rebuild?: RebuildJob | null
}

type IndexedFile = {
  name: string
  path: string
  sizeBytes: number
  modifiedAt: string
  extension: string
}

type RebuildJob = {
  id: string
  status: string
  summary?: string
  scannedEntries?: number
  indexedItems?: number
  errorCode?: string
}

async function responseJSON<T>(response: Response): Promise<T> {
  if (!response.ok) throw new Error('service unavailable')
  return response.json() as Promise<T>
}

function formatDate(value?: string) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString('zh-CN', { dateStyle: 'medium', timeStyle: 'short' })
}

export function KnowledgeIndexPanel() {
  const [status, setStatus] = useState<IndexStatus | null>(null)
  const [statusState, setStatusState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [keyword, setKeyword] = useState('')
  const [items, setItems] = useState<IndexedFile[]>([])
  const [total, setTotal] = useState(0)
  const [offset, setOffset] = useState(0)
  const [searchState, setSearchState] = useState<'idle' | 'loading' | 'ready' | 'error'>('idle')
  const [rebuildState, setRebuildState] = useState<'idle' | 'loading' | 'success' | 'error'>('idle')
  const [rebuildMessage, setRebuildMessage] = useState('')
  const [rebuildJob, setRebuildJob] = useState<RebuildJob | null>(null)

  async function readStatus() {
    const response = await apiFetch('/api/index/status')
    return responseJSON<IndexStatus>(response)
  }

  function applyStatus(data: IndexStatus) {
    setStatus(data)
    const job = data.rebuild ?? null
    setRebuildJob(job)
    if (job?.status === '规划中' || job?.status === '运行中') {
      setRebuildState('loading')
      setRebuildMessage('')
    }
  }

  useEffect(() => {
    void readStatus().then((data) => { applyStatus(data); setStatusState('ready') }).catch(() => setStatusState('error'))
  }, [])

  const rebuildID = rebuildJob?.id
  const rebuildStatus = rebuildJob?.status

  useEffect(() => {
    if (!rebuildID || (rebuildStatus !== '规划中' && rebuildStatus !== '运行中')) return
    let live = true
    const timer = window.setInterval(() => {
      void apiFetch(`/api/index/rebuild/${encodeURIComponent(rebuildID)}`).then(responseJSON<RebuildJob>).then((data) => {
        if (!live) return
        setRebuildJob(data)
        if (data.status === '已完成' || data.status === '已取消' || data.status === '失败') {
          setRebuildState(data.status === '已完成' ? 'success' : 'error')
          setRebuildMessage(`${data.summary || '索引重建已结束'} · ${data.indexedItems ?? 0} 项`)
          void readStatus().then(applyStatus).catch(() => undefined)
        }
      }).catch(() => undefined)
    }, 500)
    return () => { live = false; window.clearInterval(timer) }
  }, [rebuildID, rebuildStatus])

  async function rebuild() {
    if (rebuildState === 'loading') return
    setRebuildState('loading')
    setRebuildMessage('')
    try {
      const data = await apiFetch('/api/index/rebuild', { method: 'POST' }).then(responseJSON<RebuildJob>)
      setRebuildJob(data)
      if (data.status === '已完成') {
        setRebuildMessage(`${data.summary || '元数据索引已重建'} · ${data.indexedItems ?? 0} 项`)
        setRebuildState('success')
      } else {
        setRebuildState('loading')
      }
    } catch {
      setRebuildMessage('索引重建失败，请检查授权目录和应用连接后重试。')
      setRebuildState('error')
    }
  }

  async function cancelRebuild() {
    if (!rebuildJob || (rebuildJob.status !== '规划中' && rebuildJob.status !== '运行中')) return
    try {
      const data = await apiFetch(`/api/index/rebuild/${encodeURIComponent(rebuildJob.id)}/cancel`, { method: 'POST' }).then(responseJSON<RebuildJob>)
      setRebuildJob(data)
      setRebuildState('error')
      setRebuildMessage(`${data.summary || '索引重建已取消'} · ${data.indexedItems ?? 0} 项`)
    } catch {
      setRebuildMessage('索引取消失败，请稍后重试。')
      setRebuildState('error')
    }
  }

  async function search(event: FormEvent) {
    event.preventDefault()
    if (searchState === 'loading') return
    setSearchState('loading')
    try {
      const params = new URLSearchParams()
      if (keyword.trim()) params.set('keyword', keyword.trim())
      params.set('limit', '100')
      params.set('offset', '0')
      const query = params.toString()
      const data = await apiFetch(`/api/index/search${query ? `?${query}` : ''}`).then(responseJSON<{ items?: IndexedFile[]; total?: number }>)
      setItems(data.items ?? [])
      setTotal(data.total ?? data.items?.length ?? 0)
      setOffset(data.items?.length ?? 0)
      setSearchState('ready')
    } catch {
      setItems([])
      setTotal(0)
      setSearchState('error')
    }
  }

  async function loadMore() {
    if (searchState === 'loading' || offset >= total) return
    setSearchState('loading')
    try {
      const params = new URLSearchParams({ limit: '100', offset: String(offset) })
      if (keyword.trim()) params.set('keyword', keyword.trim())
      const data = await apiFetch(`/api/index/search?${params.toString()}`).then(responseJSON<{ items?: IndexedFile[]; total?: number }>)
      const next = data.items ?? []
      setItems((current) => [...current, ...next])
      setTotal(data.total ?? total)
      setOffset(offset + next.length)
      setSearchState('ready')
    } catch { setSearchState('error') }
  }

  const metadataOnly = status?.mode === 'metadata-only' && !status.bodyIndexEnabled && !status.ocrEnabled && !status.mediaTranscriptionEnabled

  return <section className="readonly-panel knowledge-panel" aria-labelledby="knowledge-index-panel-title">
    <header className="readonly-panel-heading">
      <div><span className="readonly-icon blue"><Database size={18} /></span><div><h2 id="knowledge-index-panel-title">本地知识索引</h2><p>先从文件名和元数据开始，正文、图片和音视频需要单独授权。</p></div></div>
      <button className="icon-button" aria-label="刷新索引状态" title="刷新索引状态" onClick={() => { setStatusState('loading'); void readStatus().then((data) => { applyStatus(data); setStatusState('ready') }).catch(() => setStatusState('error')) }} disabled={statusState === 'loading'}><RefreshCw size={16} className={statusState === 'loading' ? 'spin' : undefined} /></button>
    </header>
    {statusState === 'error' && <p className="readonly-error" role="alert">索引状态暂时无法读取，请检查应用连接后重试。</p>}
    {status && <div className="index-status-box"><div className="backup-title"><strong>{metadataOnly ? '元数据模式' : '索引状态待确认'}</strong><span className={`status-tag ${status.status === 'available' ? 'is-success' : 'is-warning'}`}>{status.status === 'available' ? '可用' : status.status === 'not_generated' ? '尚未生成' : status.status || '不可用'}</span></div><p>{status.roots?.length ? <>授权根目录：{status.roots.map((root) => <code className="index-root" key={root}>{root}</code>)}</> : '尚未发现授权根目录。'}</p>{status.status === 'available' && <p className="index-meta">{status.itemCount ?? 0} 项 · {formatDate(status.generatedAt)}</p>}<ul className="index-safety-list"><li>正文索引、OCR、音视频转写均已关闭</li><li>{status.requiresExplicitConsent ? '扩展索引需要真实用户单独授权' : '扩展索引授权状态待确认'}</li></ul></div>}
    <div className="index-actions"><button className="secondary-button" onClick={() => void rebuild()} disabled={rebuildState === 'loading'}><RefreshCw size={14} className={rebuildState === 'loading' ? 'spin' : undefined} />{rebuildState === 'loading' ? '重建中…' : '重建元数据索引'}</button>{rebuildJob && (rebuildJob.status === '规划中' || rebuildJob.status === '运行中') && <div className="index-progress" role="status"><span>{rebuildJob.summary || '正在扫描授权目录'} · 已扫描 {rebuildJob.scannedEntries ?? 0} 项 · 已索引 {rebuildJob.indexedItems ?? 0} 项</span><button className="text-button" aria-label="取消索引重建" onClick={() => void cancelRebuild()}>取消</button></div>}{rebuildMessage && <p className={rebuildState === 'error' ? 'readonly-error' : 'index-success'} role={rebuildState === 'error' ? 'alert' : 'status'}>{rebuildMessage}</p>}</div>
    <form className="index-search-form" onSubmit={search}><label>索引关键词<input aria-label="索引关键词" value={keyword} maxLength={2000} onChange={(event) => setKeyword(event.target.value)} placeholder="搜索已索引的文件名" /></label><button className="secondary-button" disabled={searchState === 'loading'}><Search size={14} />{searchState === 'loading' ? '搜索中…' : '搜索索引'}</button></form>
    {searchState === 'error' && <p className="readonly-error" role="alert">索引尚未生成或暂时不可用，请先重建索引后重试。</p>}
    {searchState === 'ready' && <div className="readonly-result"><div className="readonly-result-meta"><span>找到 {total} 个文件</span><span>仅返回元数据</span></div>{!items.length ? <p className="readonly-empty">没有找到匹配的索引文件。</p> : <><div className="readonly-table-wrap"><table className="readonly-table"><caption className="sr-only">本地索引搜索结果</caption><thead><tr><th>名称</th><th>路径</th><th>大小</th><th>修改时间</th></tr></thead><tbody>{items.map((item, index) => <tr key={`${item.path}-${index}`}><td><strong>{item.name || '—'}</strong></td><td title={item.path}>{item.path || '—'}</td><td>{formatBytes(item.sizeBytes)}</td><td>{formatDate(item.modifiedAt)}</td></tr>)}</tbody></table></div>{offset < total && <button className="secondary-button" onClick={() => void loadMore()}>加载更多（{total - offset}）</button>}</>}</div>}
    <p className="readonly-footnote"><ShieldCheck size={14} />索引保存在应用私有目录，只用于授权范围内的文件名和元数据查询。</p>
  </section>
}
