import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { Archive, ChevronRight, Container, FileSearch, RefreshCw, ShieldCheck } from 'lucide-react'
import { apiFetch } from './api'
import { formatBytes } from './format'

type FileMetadata = {
  name: string
  path: string
  sizeBytes: number
  modifiedAt: string
  extension: string
}

type FileSearchResult = {
  items?: FileMetadata[]
  total?: number
  truncated?: boolean
  readMode?: string
}

type ContainerInfo = {
  name: string
  image: string
  status: string
  cpuPercent: number
  memoryBytes: number
  ports?: string[]
  mounts?: string[]
}

type ContainerDiagnostic = {
  container: ContainerInfo
  logs?: string[]
  findings?: string[]
}

type BackupStatus = {
  name?: string
  lastRunAt?: string
  lastRunStatus?: string
  target?: string
  readable?: boolean
  sampledFiles?: number
  verifiedFiles?: number
  recoveryReady?: boolean
  recommendations?: string[]
}

type RecoveryPlan = {
  summary?: string
  backupName?: string
  target?: string
  sampledFiles?: number
  steps?: string[]
  willOverwrite?: boolean
  requiresApproval?: boolean
  readOnly?: boolean
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

function errorMessage() {
  return '暂时无法读取数据，请检查应用连接与 UGOS 授权后重试。'
}

export function FileSearchPanel() {
  const [keyword, setKeyword] = useState('')
  const [rootPath, setRootPath] = useState('')
  const [extension, setExtension] = useState('')
  const [result, setResult] = useState<FileSearchResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => controller.current?.abort(), [])

  async function search(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    const request = new AbortController(); controller.current = request
    const params = new URLSearchParams()
    if (keyword.trim()) params.set('keyword', keyword.trim())
    if (rootPath.trim()) params.set('rootPath', rootPath.trim())
    if (extension.trim()) params.set('extension', extension.trim().replace(/^\./, ''))
    setBusy(true)
    setError('')
    try {
      const query = params.toString()
      const response = await apiFetch(`/api/files/search${query ? `?${query}` : ''}`, { signal: request.signal })
      const next = await responseJSON<FileSearchResult>(response)
      if (!request.signal.aborted) setResult(next)
    } catch {
      if (!request.signal.aborted) { setResult(null); setError(errorMessage()) }
    } finally {
      if (!request.signal.aborted) { controller.current = null; setBusy(false) }
    }
  }

  return <section className="readonly-panel" aria-labelledby="file-search-panel-title">
    <header className="readonly-panel-heading">
      <div><span className="readonly-icon blue"><FileSearch size={18} /></span><div><h2 id="file-search-panel-title">文件元数据搜索</h2><p>只搜索授权目录中的名称和元数据，不读取正文。</p></div></div>
    </header>
    <form className="readonly-form" onSubmit={search}>
      <label>文件关键词<input aria-label="文件关键词" value={keyword} maxLength={2000} onChange={(event) => setKeyword(event.target.value)} placeholder="例如 report 或照片" /></label>
      <label>授权目录<input aria-label="授权目录" value={rootPath} maxLength={4096} onChange={(event) => setRootPath(event.target.value)} placeholder="可选，UGOS 已授权目录" /></label>
      <label>扩展名<input aria-label="文件扩展名" value={extension} maxLength={32} onChange={(event) => setExtension(event.target.value)} placeholder="可选，例如 pdf" /></label>
      <button className="secondary-button" disabled={busy}>{busy ? '搜索中…' : '搜索文件'}</button>{busy && <button type="button" className="secondary-button" onClick={() => { controller.current?.abort(); controller.current = null; setBusy(false); setError('文件搜索已取消。') }}>取消搜索</button>}
    </form>
    {error && <p className="readonly-error" role="alert">{error}</p>}
    {result && <div className="readonly-result">
      <div className="readonly-result-meta"><span>找到 {result.total ?? result.items?.length ?? 0} 个文件</span><span>元数据模式</span></div>
      {result.truncated && <p className="readonly-warning" role="status">结果达到上限，仅展示部分文件；请缩小目录范围后重试。</p>}
      {!result.items?.length ? <p className="readonly-empty">没有找到匹配文件。</p> : <div className="readonly-table-wrap"><table className="readonly-table"><caption className="sr-only">文件元数据结果</caption><thead><tr><th>名称</th><th>路径</th><th>大小</th><th>修改时间</th></tr></thead><tbody>{result.items.map((item, index) => <tr key={`${item.path}-${index}`}><td><strong>{item.name || '—'}</strong></td><td title={item.path}>{item.path || '—'}</td><td>{formatBytes(item.sizeBytes)}</td><td>{formatDate(item.modifiedAt)}</td></tr>)}</tbody></table></div>}
    </div>}
  </section>
}

export function DockerPanel() {
  const [items, setItems] = useState<ContainerDiagnostic[]>([])
  const [state, setState] = useState<'idle' | 'loading' | 'ready' | 'error'>('loading')

  async function fetchContainers() {
    const response = await apiFetch('/api/docker/containers')
    return responseJSON<{ items?: ContainerDiagnostic[] }>(response)
  }

  async function load() {
    setState('loading')
    try {
      const data = await fetchContainers()
      setItems(data.items ?? [])
      setState('ready')
    } catch {
      setItems([])
      setState('error')
    }
  }

  useEffect(() => {
    void fetchContainers().then((data) => { setItems(data.items ?? []); setState('ready') }).catch(() => setState('error'))
  }, [])

  return <section className="readonly-panel" aria-labelledby="docker-panel-title">
    <header className="readonly-panel-heading">
      <div><span className="readonly-icon violet"><Container size={18} /></span><div><h2 id="docker-panel-title">Docker 只读诊断</h2><p>查看状态、资源、端口、挂载和受限日志，不会修改容器。</p></div></div>
      <button className="icon-button" aria-label="刷新 Docker 状态" title="刷新 Docker 状态" onClick={() => void load()} disabled={state === 'loading'}><RefreshCw size={16} className={state === 'loading' ? 'spin' : undefined} /></button>
    </header>
    {state === 'error' && <p className="readonly-error" role="alert">Docker 状态暂不可用，请确认 UGOS 已提供只读能力。</p>}
    {state === 'ready' && !items.length && <p className="readonly-empty">当前没有可读取的容器，或 Docker 能力尚未接入。</p>}
    <div className="docker-list">{items.map((item) => <article className="docker-item" key={item.container.name}>
      <div className="docker-item-heading"><div><strong>{item.container.name || '未命名容器'}</strong><span>{item.container.image || '—'}</span></div><span className={`status-tag ${item.container.status === 'running' ? 'is-success' : 'is-warning'}`}>{item.container.status || 'unknown'}</span></div>
      <dl className="docker-stats"><div><dt>CPU</dt><dd>{Number.isFinite(item.container.cpuPercent) ? `${item.container.cpuPercent.toFixed(1)}%` : '—'}</dd></div><div><dt>内存</dt><dd>{formatBytes(item.container.memoryBytes)}</dd></div><div><dt>端口</dt><dd>{item.container.ports?.join(', ') || '—'}</dd></div></dl>
      {!!item.container.mounts?.length && <p className="docker-detail"><strong>挂载</strong>{item.container.mounts.join(' · ')}</p>}
      {!!item.logs?.length && <details className="docker-logs"><summary>最近日志（已脱敏）</summary><pre>{item.logs.join('\n')}</pre></details>}
      {!!item.findings?.length && <ul className="finding-list">{item.findings.map((finding, index) => <li key={`${finding}-${index}`}>{finding}</li>)}</ul>}
    </article>)}</div>
    <p className="readonly-footnote"><ShieldCheck size={14} />此面板仅提供诊断信息，不支持 exec、停止、重启或删除容器。</p>
  </section>
}

export function BackupPanel() {
  const [status, setStatus] = useState<BackupStatus | null>(null)
  const [state, setState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [plan, setPlan] = useState<RecoveryPlan | null>(null)
  const [planState, setPlanState] = useState<'idle' | 'loading' | 'ready' | 'error'>('idle')

  async function fetchBackupStatus() {
    const response = await apiFetch('/api/backups/status')
    return responseJSON<BackupStatus>(response)
  }

  async function loadStatus() {
    setState('loading')
    try {
      setStatus(await fetchBackupStatus())
      setState('ready')
    } catch {
      setStatus(null)
      setState('error')
    }
  }

  useEffect(() => {
    void fetchBackupStatus().then((data) => { setStatus(data); setState('ready') }).catch(() => setState('error'))
  }, [])

  async function loadPlan() {
    if (planState === 'loading') return
    setPlanState('loading')
    try {
      const response = await apiFetch('/api/backups/recovery-plan')
      setPlan(await responseJSON<RecoveryPlan>(response))
      setPlanState('ready')
    } catch {
      setPlan(null)
      setPlanState('error')
    }
  }

  return <section className="readonly-panel" aria-labelledby="backup-panel-title">
    <header className="readonly-panel-heading">
      <div><span className="readonly-icon mint"><Archive size={18} /></span><div><h2 id="backup-panel-title">备份与恢复验证</h2><p>检查最近备份、目标可读性和抽样文件，不覆盖原文件。</p></div></div>
      <button className="icon-button" aria-label="刷新备份状态" title="刷新备份状态" onClick={() => void loadStatus()} disabled={state === 'loading'}><RefreshCw size={16} className={state === 'loading' ? 'spin' : undefined} /></button>
    </header>
    {state === 'error' && <p className="readonly-error" role="alert">备份状态暂不可用，请确认 UGOS 已配置备份能力。</p>}
    {status && <div className="backup-summary"><div className="backup-title"><strong>{status.name || '未命名备份'}</strong><span className={`status-tag ${status.lastRunStatus === 'success' ? 'is-success' : 'is-warning'}`}>{status.lastRunStatus === 'success' ? '最近成功' : status.lastRunStatus || '未知状态'}</span></div><dl className="backup-stats"><div><dt>最近运行</dt><dd>{formatDate(status.lastRunAt)}</dd></div><div><dt>验证文件</dt><dd>{status.sampledFiles ?? 0} / {status.verifiedFiles ?? 0}</dd></div><div><dt>目标可读</dt><dd>{status.readable ? '是' : '否'}</dd></div></dl><p className="backup-target">目标：{status.target || '—'} · {status.recoveryReady ? '恢复点可用' : '需要进一步检查'}</p>{!!status.recommendations?.length && <ul className="recommendation-list">{status.recommendations.map((recommendation, index) => <li key={`${recommendation}-${index}`}>{recommendation}</li>)}</ul>}</div>}
    <button className="secondary-button recovery-plan-button" onClick={() => void loadPlan()} disabled={planState === 'loading'}><ChevronRight size={15} />{planState === 'loading' ? '正在读取计划…' : '查看恢复演练计划'}</button>
    {planState === 'error' && <p className="readonly-error" role="alert">恢复演练计划暂时无法读取，请稍后重试。</p>}
    {plan && <div className="recovery-plan-preview"><div className="backup-title"><strong>{plan.summary || '只读恢复演练计划'}</strong><span className="status-tag">只读 · 需确认</span></div><dl className="backup-stats"><div><dt>备份</dt><dd>{plan.backupName || '—'}</dd></div><div><dt>目标</dt><dd>{plan.target || '—'}</dd></div><div><dt>抽样文件</dt><dd>{plan.sampledFiles ?? 0}</dd></div></dl><ol>{(plan.steps ?? []).map((step, index) => <li key={`${step}-${index}`}>{step}</li>)}</ol><p className="readonly-footnote"><ShieldCheck size={14} /><strong>不会覆盖原文件</strong>；执行恢复仍需单独审批，目前不会执行恢复。</p></div>}
  </section>
}
