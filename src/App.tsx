import { useEffect, useRef, useState } from 'react'
import type { CSSProperties, FormEvent } from 'react'
import {
  ArrowUp, ArrowUpRight, Check, ChevronDown, ChevronRight, CircleHelp, Download,
  FileSearch, FolderOpen, HardDrive, History, LayoutGrid, LoaderCircle,
  MessageSquare, Monitor, Palette, PanelLeft, Plus, Search, ShieldCheck,
  Sparkles, SquareTerminal, X,
} from 'lucide-react'
import { apiFetch } from './api'
import './App.css'

type Page = 'home' | 'skills' | 'health' | 'downloads'
type Task = { id: string; prompt: string; status: string; summary: string; updatedAt: string }
type Usage = { volumes?: { name: string; totalBytes: number; usedBytes: number; freeBytes: number }[]; topItems?: { path: string; sizeBytes: number; kind: string }[] }
type DownloadPlan = { id: string; targetDirectory: string; sources: { title: string; url: string; license: string; sizeBytes: number }[]; estimatedBytes: number; status: string }
type TaskEvent = { id: number; type: string; data?: unknown }
type Artifact = { name: string; path: string; sizeBytes: number; createdAt: string }
type ModelStatus = { provider: string; model: string; configured: boolean; mode: string }
type IndexStatus = { mode: string; bodyIndexEnabled: boolean; ocrEnabled: boolean; mediaTranscriptionEnabled: boolean }
const themes = [{ name: '鸢尾紫', color: '#7363df' }, { name: '晴空蓝', color: '#437acb' }, { name: '松石绿', color: '#278375' }, { name: '暖杏橙', color: '#b86c3c' }, { name: '玫瑰粉', color: '#b76187' }]
const capabilities = [
  { title: '文件搜索', description: '不记得放哪了？从文件名找起。', prompt: '搜索授权目录中的文件', icon: FileSearch, tone: 'blue', tag: '文件元数据' },
  { title: '空间分析', description: '看看是谁，悄悄占满了空间。', prompt: '分析 NAS 的空间占用', icon: HardDrive, tone: 'amber', tag: '只读分析' },
  { title: '容器诊断', description: '容器的小状况，一起看看。', prompt: '检查 Docker 容器是否有异常', icon: SquareTerminal, tone: 'violet', tag: '状态与日志' },
  { title: '备份检查', description: '重要的资料，多一份安心。', prompt: '检查最近一次备份状态', icon: ShieldCheck, tone: 'mint', tag: '恢复点检查' },
]
const pageNames: Record<Page, string> = { home: '新任务', skills: '常用能力', health: '存储健康', downloads: '下载任务' }

function savedAccent() {
  try { const value = localStorage.getItem('nasmate-accent'); if (value && /^#[0-9a-f]{6}$/i.test(value)) return value } catch { /* Some embedded browsers disable storage. */ }
  return themes[0].color
}
function formatBytes(value: number) {
  if (!Number.isFinite(value) || value < 0) return '—'
  const unit = value >= 1024 ** 4 ? 4 : value >= 1024 ** 3 ? 3 : value >= 1024 ** 2 ? 2 : value >= 1024 ? 1 : 0
  return `${(value / 1024 ** unit).toFixed(unit ? 1 : 0)} ${['B', 'KB', 'MB', 'GB', 'TB'][unit]}`
}
async function readJSON<T>(response: Response): Promise<T> {
  if (!response.ok) throw new Error('应用服务暂时不可用，请稍后重试。')
  return response.json() as Promise<T>
}

function App() {
  const [page, setPage] = useState<Page>('home')
  const [collapsed, setCollapsed] = useState(false)
  const [mobileOpen, setMobileOpen] = useState(false)
  const [accent, setAccent] = useState(savedAccent)
  const [tasks, setTasks] = useState<Task[]>([])
  const [tasksState, setTasksState] = useState('loading')
  const [historySearch, setHistorySearch] = useState('')
  const [searchOpen, setSearchOpen] = useState(false)
  const [activeId, setActiveId] = useState<string | null>(null)
  const [input, setInput] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [usage, setUsage] = useState<Usage | null>(null)
  const [usageState, setUsageState] = useState('loading')
  const [plans, setPlans] = useState<DownloadPlan[]>([])
  const [plansState, setPlansState] = useState('loading')
  const [approving, setApproving] = useState<string | null>(null)
  const [events, setEvents] = useState<TaskEvent[]>([])
  const [eventsState, setEventsState] = useState('idle')
  const [artifacts, setArtifacts] = useState<Artifact[]>([])
  const [modelStatus, setModelStatus] = useState<ModelStatus | null>(null)
  const [indexStatus, setIndexStatus] = useState<IndexStatus | null>(null)
  const composer = useRef<HTMLTextAreaElement>(null)
  const appearance = useRef<HTMLDialogElement>(null)
  const permissions = useRef<HTMLDialogElement>(null)
  const sidebar = useRef<HTMLElement>(null)
  const menuButton = useRef<HTMLButtonElement>(null)
  const activeTask = tasks.find((task) => task.id === activeId)
  const pendingCount = plans.filter((plan) => plan.status === '待确认').length
  const filteredTasks = tasks.filter((task) => task.prompt.toLowerCase().includes(historySearch.toLowerCase()))

  useEffect(() => { try { localStorage.setItem('nasmate-accent', accent) } catch { /* The theme still works without storage. */ } }, [accent])
  useEffect(() => {
    let live = true
    void apiFetch('/api/tasks').then(readJSON<{ items: Task[] }>).then((data) => { if (live) { setTasks((data.items ?? []).sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))); setTasksState('ready') } }).catch(() => { if (live) setTasksState('error') })
    void apiFetch('/api/storage/usage').then(readJSON<Usage>).then((data) => { if (live) { setUsage(data); setUsageState('ready') } }).catch(() => { if (live) setUsageState('error') })
    void apiFetch('/api/downloads').then(readJSON<{ items: DownloadPlan[] }>).then((data) => { if (live) { setPlans(data.items ?? []); setPlansState('ready') } }).catch(() => { if (live) setPlansState('error') })
    void apiFetch('/api/artifacts').then(readJSON<{ items: Artifact[] }>).then((data) => { if (live) setArtifacts(data.items ?? []) }).catch(() => undefined)
    void apiFetch('/api/models/status').then(readJSON<ModelStatus>).then((data) => { if (live) setModelStatus(data) }).catch(() => undefined)
    void apiFetch('/api/index/status').then(readJSON<IndexStatus>).then((data) => { if (live) setIndexStatus(data) }).catch(() => undefined)
    return () => { live = false }
  }, [])
  useEffect(() => {
    if (!activeId) return
    let live = true
    void apiFetch(`/api/tasks/${encodeURIComponent(activeId)}/events`).then(readJSON<{ items: TaskEvent[] }>).then((data) => { if (live) { setEvents(data.items ?? []); setEventsState('ready') } }).catch(() => { if (live) setEventsState('error') })
    return () => { live = false }
  }, [activeId])
  useEffect(() => {
    if (!mobileOpen) return
    const closeOnEscape = (event: KeyboardEvent) => { if (event.key === 'Escape') { setMobileOpen(false); menuButton.current?.focus() } }
    sidebar.current?.querySelector<HTMLButtonElement>('button')?.focus()
    document.addEventListener('keydown', closeOnEscape)
    return () => document.removeEventListener('keydown', closeOnEscape)
  }, [mobileOpen])

  function navigate(next: Page) { setPage(next); setActiveId(null); setMobileOpen(false); setError('') }
  function newTask() { navigate('home'); setInput(''); requestAnimationFrame(() => composer.current?.focus()) }
  function handleSuggestion(prompt: string) { navigate('home'); setInput(prompt); requestAnimationFrame(() => composer.current?.focus()) }
  function selectTask(id: string) { setEvents([]); setEventsState('loading'); setActiveId(id) }
  async function submit(event: FormEvent) {
    event.preventDefault()
    const prompt = input.trim()
    if (prompt.length < 2 || submitting) return
    setSubmitting(true)
    setError('')
    try {
      const task = await apiFetch('/api/tasks', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ prompt }) }).then(readJSON<Task>)
      setTasks((current) => [task, ...current.filter((item) => item.id !== task.id)])
      setTasksState('ready')
      selectTask(task.id)
      setInput('')
    } catch { setError('任务未能创建。请检查 UGOS 登录与应用连接，输入内容已保留。') }
    finally { setSubmitting(false) }
  }
  async function approve(plan: DownloadPlan, action: 'approve' | 'deny' | 'cancel') {
    if (approving) return
    setApproving(plan.id)
    setError('')
    try {
      const updated = await apiFetch(`/api/downloads/${encodeURIComponent(plan.id)}?action=${action}`, { method: 'POST' }).then(readJSON<DownloadPlan>)
      setPlans((current) => current.map((item) => item.id === updated.id ? updated : item))
    } catch { setError('审批未成功，请检查连接后重试。') }
    finally { setApproving(null) }
  }

  return (
    <div className={`workspace ${collapsed ? 'is-collapsed' : ''}`} style={{ '--accent': accent } as CSSProperties}>
      <a className="skip-link" href="#workspace-main">跳到主要内容</a>
      {mobileOpen && <button className="sidebar-scrim" aria-label="关闭导航菜单" onClick={() => { setMobileOpen(false); menuButton.current?.focus() }} />}
      <aside ref={sidebar} className={`workspace-sidebar ${mobileOpen ? 'is-open' : ''}`} aria-label="工作区导航">
        <div className="sidebar-top">
          <button className="brand" onClick={newTask} aria-label="NasMate 首页"><span className="brand-symbol"><span /><span /></span><span className="brand-word">NasMate<span className="brand-period">.</span></span></button>
          <button className="icon-button sidebar-toggle" title={collapsed ? '展开侧栏' : '收起侧栏'} aria-label={collapsed ? '展开侧栏' : '收起侧栏'} onClick={() => setCollapsed(!collapsed)}><PanelLeft size={17} /></button>
          <button className="icon-button mobile-close" aria-label="关闭菜单" onClick={() => { setMobileOpen(false); menuButton.current?.focus() }}><X size={19} /></button>
        </div>
        <button className={`new-task ${page === 'home' && !activeId ? 'is-active' : ''}`} onClick={newTask} title="新任务"><Plus size={19} /><span>新任务</span><span className="new-task-hint">开始探索</span></button>
        <nav className="primary-nav">
          {([{ page: 'skills', icon: LayoutGrid }, { page: 'health', icon: HardDrive }, { page: 'downloads', icon: Download }] as const).map(({ page: target, icon: Icon }) => <button key={target} title={pageNames[target]} className={`nav-link ${page === target ? 'is-active' : ''}`} aria-current={page === target ? 'page' : undefined} onClick={() => navigate(target)}><Icon size={19} /><span>{pageNames[target]}</span>{target === 'downloads' && pendingCount > 0 && <span className="nav-badge">{pendingCount}</span>}</button>)}
        </nav>
        <div className="history-section">
          <div className="history-heading"><span>最近任务 <ChevronDown size={12} /></span><button className="icon-button" title="搜索任务" aria-label="搜索最近任务" aria-expanded={searchOpen} onClick={() => setSearchOpen(!searchOpen)}><Search size={16} /></button></div>
          {searchOpen && <input className="history-search" aria-label="搜索最近任务" placeholder="搜索任务…" value={historySearch} onChange={(event) => setHistorySearch(event.target.value)} />}
          <div className="history-list">
            {filteredTasks.map((task) => <button key={task.id} title={task.prompt} className={`history-item ${activeId === task.id ? 'is-active' : ''}`} onClick={() => { navigate('home'); selectTask(task.id) }}><MessageSquare size={15} /><span>{task.prompt}</span></button>)}
            {!filteredTasks.length && <div className="history-empty"><History size={21} /><p>{tasksState === 'loading' ? '正在读取任务…' : tasksState === 'error' ? '暂时无法读取历史任务' : historySearch ? '没有匹配的任务' : '你的下一件小事，从这里开始。'}</p></div>}
          </div>
        </div>
        <div className="sidebar-bottom">
          <div className="local-note"><ShieldCheck size={18} /><div><strong>你的 NAS，你的数据</strong><p>始终在授权范围内工作</p></div></div>
          <button className="nav-link appearance-trigger" onClick={() => appearance.current?.showModal()} title="外观与主题"><Palette size={18} /><span>外观与主题</span><span className="current-color" /></button>
          <div className="workspace-profile"><span className="profile-avatar">N</span><div><strong>我的工作区</strong><span>NasMate · 本地优先</span></div><button className="icon-button" aria-label="查看权限说明" title="权限说明" onClick={() => permissions.current?.showModal()}><CircleHelp size={18} /></button></div>
        </div>
      </aside>

      <main id="workspace-main" className="workspace-main">
        <header className="workspace-header"><div><button ref={menuButton} className="icon-button mobile-menu" aria-label="打开导航菜单" aria-expanded={mobileOpen} onClick={() => setMobileOpen(true)}><PanelLeft size={20} /></button><span className="header-label">{activeTask ? '任务 / ' + activeTask.prompt : '个人工作台'}</span></div><div className="header-actions"><span className="model-status"><span />{modelStatus ? `${modelStatus.provider} · ${modelStatus.model}` : '模型状态读取中'}</span><button className="theme-shortcut" onClick={() => appearance.current?.showModal()}><Palette size={15} /><span>装扮工作台</span></button></div></header>
        {page === 'home' && <section className={`home-content ${activeTask ? 'has-task' : ''}`}>
          {activeTask ? <div className="task-conversation"><button className="text-button" onClick={newTask}>← 返回工作台</button><h1>{activeTask.prompt}</h1><div className="task-answer"><span className="answer-avatar"><Sparkles size={20} /></span><div><strong>NasMate <span className="status-tag">{activeTask.status}</span></strong><p>{activeTask.summary}</p></div></div><details className="event-details"><summary><History size={15} />执行轨迹 <span>{events.length} 条记录</span></summary>{eventsState === 'error' ? <p>暂时无法读取轨迹。</p> : eventsState === 'loading' ? <p>正在读取…</p> : events.map((entry) => <div className="event-entry" key={entry.id}><strong>{entry.type}</strong><pre>{JSON.stringify(entry.data, null, 2)}</pre></div>)}</details></div> : <div className="welcome-heading"><span className="welcome-eyebrow"><span />YOUR EVERYDAY NAS COMPANION</span><h1>让琐事简单，<br />让生活<span className="accent-word">多一点空间<span className="heading-spark">✦</span></span>。</h1><p>你好，我是 NasMate。今天，有什么可以一起完成？</p></div>}
          <form className="prompt-shell" onSubmit={submit}>
            <div className="prompt-box"><label className="sr-only" htmlFor="task-prompt">描述你的 NAS 任务</label><textarea id="task-prompt" ref={composer} value={input} disabled={submitting} maxLength={2000} placeholder={activeTask ? '开始另一项任务…' : '找一份文件，看看空间，或检查一次备份…'} onChange={(event) => setInput(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit() } }} /><div className="prompt-toolbar"><button type="button" className="permission-button" onClick={() => permissions.current?.showModal()}><ShieldCheck size={17} />默认权限<ChevronDown size={12} /></button><div className="prompt-send"><span>{submitting ? '正在处理任务' : 'Enter 发送'}</span><button className="send-task" type="submit" aria-label="发送任务" disabled={input.trim().length < 2 || submitting}>{submitting ? <LoaderCircle className="spin" size={21} /> : <ArrowUp size={22} />}</button></div></div></div>
            <div className="prompt-context"><span><Monitor size={15} />NAS 工作区</span><i /><button type="button" onClick={() => permissions.current?.showModal()}><FolderOpen size={15} />已授权的文件夹<ChevronDown size={12} /></button><span className="prompt-local"><span />本地文件处理</span></div>
          </form>
          {error && <p className="inline-error" role="alert">{error}</p>}
          {!activeTask && <><div className="suggestion-row"><span>试着问问</span>{['分析 NAS 空间占用', '检查 Docker 容器', '检查备份状态'].map((prompt) => <button key={prompt} onClick={() => handleSuggestion(prompt)}>{prompt}<ArrowUpRight size={13} /></button>)}</div><div className="section-caption"><h2>把这些小事，交给我</h2><button onClick={() => navigate('skills')}>全部能力<ChevronRight size={14} /></button></div><div className="capability-grid">{capabilities.map(({ title, description, prompt, icon: Icon, tone, tag }) => <button className={`capability-card ${tone}`} key={title} onClick={() => handleSuggestion(prompt)}><span className="capability-art"><Icon size={27} strokeWidth={1.7} /></span><ArrowUpRight className="card-arrow" size={17} /><strong>{title}</strong><p>{description}</p><span className="card-tag">{tag}</span></button>)}</div><p className="home-footnote"><ShieldCheck size={13} />每一步都有迹可循，每一次写入由你决定。</p></>}
        </section>}

        {page === 'skills' && <section className="utility-content"><div className="utility-heading"><span className="welcome-eyebrow">MADE FOR YOUR NAS</span><h1>日常小事，各有所长。</h1><p>选择一项能力，描述具体需求后再发送任务。</p></div><div className="capability-grid">{capabilities.map(({ title, description, prompt, icon: Icon, tone, tag }) => <button className={`capability-card ${tone}`} key={title} onClick={() => handleSuggestion(prompt)}><span className="capability-art"><Icon size={27} /></span><ArrowUpRight className="card-arrow" size={17} /><strong>{title}</strong><p>{description}</p><span className="card-tag">{tag}</span></button>)}</div><div className="info-strip"><ShieldCheck size={18} /><p>容器和备份能力取决于 NAS 的实际接入状态。暂不可用时，任务会明确提示。</p></div></section>}
        {page === 'health' && <section className="utility-content"><div className="utility-heading"><span className="welcome-eyebrow">A LITTLE MORE ROOM</span><h1>空间，一目了然。</h1><p>授权范围内的存储使用情况。</p></div>{usageState !== 'ready' ? <div className="empty-panel"><HardDrive size={32} /><h2>{usageState === 'loading' ? '正在读取存储状态' : '暂时无法读取存储'}</h2><p>{usageState === 'loading' ? '大目录的首次统计可能需要一些时间。' : '请检查应用连接，并在 UGOS 应用设置中授权文件夹。'}</p></div> : <><div className="volume-grid">{usage?.volumes?.map((volume, index) => <div className="volume-card" key={`${volume.name}-${index}`}><span><HardDrive size={19} />{volume.name}</span><h2>{formatBytes(volume.usedBytes)}<small> / {formatBytes(volume.totalBytes)}</small></h2><div className="usage-track"><span style={{ width: `${Math.max(0, Math.min(100, volume.totalBytes ? volume.usedBytes / volume.totalBytes * 100 : 0))}%` }} /></div><p>剩余可用 {formatBytes(volume.freeBytes)}</p></div>)}</div><div className="directory-list"><h2>空间占用排行</h2>{usage?.topItems?.map((item, index) => <div key={`${item.path}-${index}`}><FolderOpen size={17} /><span>{item.path}</span><strong>{formatBytes(item.sizeBytes)}</strong></div>)}</div></>}<div className="directory-list"><h2>健康报告</h2>{artifacts.length ? artifacts.map((artifact) => <div key={artifact.name}><History size={17} /><span>{artifact.name}</span><strong>{formatBytes(artifact.sizeBytes)}</strong></div>) : <div><History size={17} /><span>暂无已生成报告</span></div>}</div></section>}
        {page === 'downloads' && <section className="utility-content"><div className="utility-heading"><span className="welcome-eyebrow">EVERY FILE HAS A HOME</span><h1>下载，心中有数。</h1><p>查看任务状态，并确认每一次文件写入。</p></div>{error && <p className="inline-error" role="alert">{error}</p>}{plansState !== 'ready' || !plans.length ? <div className="empty-panel"><Download size={32} /><h2>{plansState === 'loading' ? '正在读取下载任务' : plansState === 'error' ? '暂时无法读取下载任务' : '还没有下载计划'}</h2><p>已有计划会出现在这里，供你查看来源与审批。</p></div> : plans.map((plan) => <article className="download-card" key={plan.id}><header><h2><Download size={19} />{plan.sources.length} 个文件</h2><span className="status-tag">{plan.status}</span></header><p className="download-path">保存到 {plan.targetDirectory}</p><p>预计占用 {formatBytes(plan.estimatedBytes)}</p><ul>{plan.sources.map((source, index) => <li key={`${source.url}-${index}`}><strong>{source.title || '未命名来源'}</strong><span className="source-url">{source.url}</span><small>许可：{source.license || '未提供'} · {formatBytes(source.sizeBytes)}</small></li>)}</ul><div className="approval-actions">{plan.status === '待确认' && <><button disabled={approving !== null} onClick={() => void approve(plan, 'deny')}>拒绝</button><button className="primary-button" disabled={approving !== null} onClick={() => void approve(plan, 'approve')}>{approving === plan.id ? '处理中…' : '确认下载'}</button></>}{plan.status === '运行中' && <button className="approval-actions-cancel" disabled={approving !== null} onClick={() => void approve(plan, 'cancel')}>{approving === plan.id ? '处理中…' : '取消下载'}</button>}</div></article>)}</section>}
      </main>

      <dialog ref={appearance} className="settings-dialog" aria-labelledby="appearance-title" onClick={(event) => { if (event.target === event.currentTarget) appearance.current?.close() }}><div className="dialog-content"><header><span className="dialog-icon"><Palette size={22} /></span><button className="icon-button" autoFocus aria-label="关闭外观设置" onClick={() => appearance.current?.close()}><X size={20} /></button></header><h2 id="appearance-title">一点颜色，很像你。</h2><p>选择你喜欢的主题，让工作台多一点个人风格。</p><div className="color-options">{themes.map((theme) => <button key={theme.color} aria-label={theme.name} aria-pressed={theme.color === accent} className={theme.color === accent ? 'selected' : ''} onClick={() => setAccent(theme.color)}><span style={{ background: theme.color }}>{theme.color === accent && <Check size={19} />}</span><small>{theme.name}</small></button>)}</div><label className="custom-color"><span>或者，挑一个自己的颜色<small>仅保存在当前浏览器</small></span><input type="color" value={accent} onChange={(event) => setAccent(event.target.value)} aria-label="自定义主题颜色" /></label><div className="theme-preview"><span className="preview-dot" /><div><strong>NasMate</strong><p>把复杂留给我，把简单留给你。</p></div><ArrowUpRight size={19} /></div><button className="primary-button dialog-done" onClick={() => appearance.current?.close()}>就用这个颜色</button></div></dialog>
      <dialog ref={permissions} className="settings-dialog" aria-labelledby="permissions-title" onClick={(event) => { if (event.target === event.currentTarget) permissions.current?.close() }}><div className="dialog-content"><header><span className="dialog-icon"><ShieldCheck size={22} /></span><button className="icon-button" autoFocus aria-label="关闭权限说明" onClick={() => permissions.current?.close()}><X size={20} /></button></header><h2 id="permissions-title">你的文件，你做主。</h2><p>NasMate 通过 UGOS 获取已授权的文件夹。请在 UGOS 应用设置中管理目录授权。</p><ul className="permission-list"><li><Check size={16} />默认读取文件名和元数据</li><li><Check size={16} />下载写入前需要你确认</li><li><Check size={16} />不开放任意系统命令和自动删除</li>{indexStatus && <li><Check size={16} />正文索引、OCR、音视频转写默认关闭</li>}</ul><button className="primary-button dialog-done" onClick={() => permissions.current?.close()}>知道了</button></div></dialog>
    </div>
  )
}

export default App
