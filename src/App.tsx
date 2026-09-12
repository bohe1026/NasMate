import { useEffect, useMemo, useState } from 'react'
import {
  Activity,
  ArrowUpRight,
  Bot,
  Check,
  ChevronDown,
  CircleDot,
  Database,
  Download,
  FileSearch,
  FolderOpen,
  HardDrive,
  LockKeyhole,
  Menu,
  MoreHorizontal,
  Plus,
  Server,
  Settings2,
  ShieldCheck,
  Sparkles,
  SquareTerminal,
  X,
  Zap,
} from 'lucide-react'
import './App.css'
import { apiFetch } from './api'

type TaskStatus = '待处理' | '规划中' | '等待确认' | '运行中' | '已完成' | '已取消' | '失败'
type TaskIcon = 'storage' | 'search' | 'docker' | 'download'
type Task = {
  id: string
  title: string
  detail: string
  status: TaskStatus
  time: string
  icon: TaskIcon
  summary: string
}
type ApiTask = { id: string; prompt: string; status: TaskStatus; summary: string; updatedAt: string }
type StorageUsage = {
  volumes?: Array<{ totalBytes: number; usedBytes: number; freeBytes: number }>
  topItems?: Array<{ kind: string }>
}
type DownloadPlan = {
  id: string
  targetDirectory: string
  sources: Array<{ title: string; url: string; license: string; sizeBytes: number }>
  estimatedBytes: number
  status: string
}
type Message = { role: 'user' | 'assistant'; text: string; time: string }

const welcomeTask: Task = {
  id: 'welcome',
  title: '开始一个 NAS 任务',
  detail: '等待你的指令',
  status: '待处理',
  time: '现在',
  icon: 'storage',
  summary: '描述你想检查或整理的内容，NasMate 会先生成计划。',
}

function formatBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes < 0) return '--'
  if (bytes < 1024 ** 3) return `${(bytes / 1024 ** 2).toFixed(1)} MB`
  return `${(bytes / 1024 ** 3).toFixed(1)} GB`
}

function formatTaskTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '--'
  return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
}

function taskIcon(prompt: string): TaskIcon {
  if (prompt.includes('Docker') || prompt.includes('容器')) return 'docker'
  if (prompt.includes('下载')) return 'download'
  if (prompt.includes('文件') || prompt.includes('目录')) return 'search'
  return 'storage'
}

function fromApiTask(task: ApiTask): Task {
  return {
    id: task.id,
    title: task.prompt,
    detail: task.summary || task.status,
    status: task.status,
    time: formatTaskTime(task.updatedAt),
    icon: taskIcon(task.prompt),
    summary: task.summary || '任务已创建，等待后端执行结果。',
  }
}

function iconForTask(icon: TaskIcon) {
  if (icon === 'storage') return <HardDrive size={17} />
  if (icon === 'search') return <FileSearch size={17} />
  if (icon === 'docker') return <SquareTerminal size={17} />
  return <Download size={17} />
}

async function readJSON<T>(response: Response): Promise<T> {
  if (!response.ok) throw new Error(`request failed: ${response.status}`)
  return response.json() as Promise<T>
}

function App() {
  const [tasks, setTasks] = useState<Task[]>([])
  const [activeTaskId, setActiveTaskId] = useState<string | null>(null)
  const [input, setInput] = useState('')
  const [mobileMenu, setMobileMenu] = useState(false)
  const [storage, setStorage] = useState<StorageUsage | null>(null)
  const [storageState, setStorageState] = useState<'loading' | 'ready' | 'unavailable'>('loading')
  const [containerCount, setContainerCount] = useState<number | null>(null)
  const [backupState, setBackupState] = useState<'unknown' | 'ready' | 'unavailable'>('unknown')
  const [downloadPlans, setDownloadPlans] = useState<DownloadPlan[]>([])
  const [messages, setMessages] = useState<Message[]>([{ role: 'assistant', text: '描述你想在 NAS 上检查或整理的内容，我会先生成计划。', time: '--:--' }])

  const activeTask = useMemo(() => tasks.find((task) => task.id === activeTaskId) ?? tasks[0] ?? welcomeTask, [activeTaskId, tasks])
  const volume = storage?.volumes?.[0]
  const storagePercent = volume && volume.totalBytes > 0 ? Math.round((volume.usedBytes / volume.totalBytes) * 100) : null
  const pendingPlan = downloadPlans.find((plan) => plan.status === '待确认')
  const storageLabel = storageState === 'loading' ? '读取中' : storageState === 'ready' ? 'NAS 在线' : '等待授权目录'

  useEffect(() => {
    void apiFetch('/api/tasks')
      .then((response) => readJSON<{ items?: ApiTask[] }>(response))
      .then((data) => {
        const next = (data.items ?? []).map(fromApiTask)
        setTasks(next)
        setActiveTaskId((current) => current ?? next[0]?.id ?? null)
      })
      .catch(() => undefined)

    void apiFetch('/api/storage/usage')
      .then((response) => readJSON<StorageUsage>(response))
      .then((data) => {
        setStorage(data)
        setStorageState('ready')
      })
      .catch(() => setStorageState('unavailable'))

    void apiFetch('/api/docker/containers')
      .then((response) => readJSON<{ items?: unknown[] }>(response))
      .then((data) => setContainerCount(data.items?.length ?? 0))
      .catch(() => setContainerCount(null))

    void apiFetch('/api/backups/status')
      .then((response) => readJSON(response))
      .then(() => setBackupState('ready'))
      .catch(() => setBackupState('unavailable'))

    void apiFetch('/api/downloads')
      .then((response) => readJSON<{ items?: DownloadPlan[] }>(response))
      .then((data) => setDownloadPlans(data.items ?? []))
      .catch(() => undefined)
  }, [])

  const addMessage = (text: string, role: 'user' | 'assistant') => {
    setMessages((current) => [...current, { role, text, time: new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }) }])
  }

  const createTask = async (prompt: string) => {
    try {
      const response = await apiFetch('/api/tasks', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ prompt }) })
      const task = fromApiTask(await readJSON<ApiTask>(response))
      setTasks((current) => [task, ...current.filter((item) => item.id !== task.id)])
      setActiveTaskId(task.id)
      return task
    } catch {
      return null
    }
  }

  const runQuickAction = (prompt: string) => {
    addMessage(prompt, 'user')
    setMobileMenu(false)
    void createTask(prompt).then((task) => addMessage(task ? '任务已创建，后端会先执行权限检查。' : '任务创建失败，请检查 UGOS 登录和应用服务状态。', 'assistant'))
  }

  const submitPrompt = () => {
    const trimmed = input.trim()
    if (!trimmed) return
    addMessage(trimmed, 'user')
    setInput('')
    void createTask(trimmed).then((task) => addMessage(task ? '任务已创建，当前仅允许在授权范围内执行。' : '任务创建失败，请检查 UGOS 登录和应用服务状态。', 'assistant'))
  }

  const updateDownload = async (plan: DownloadPlan, action: 'approve' | 'deny') => {
    try {
      const response = await apiFetch(`/api/downloads/${plan.id}?action=${action}`, { method: 'POST' })
      const updated = await readJSON<DownloadPlan>(response)
      setDownloadPlans((current) => current.map((item) => (item.id === updated.id ? updated : item)))
    } catch {
      addMessage('审批请求失败，请检查登录状态和应用服务状态。', 'assistant')
    }
  }

  return (
    <div className="app-shell">
      <aside className={`sidebar ${mobileMenu ? 'sidebar-open' : ''}`}>
        <div className="brand-row"><div className="brand-mark"><Sparkles size={17} /></div><div><div className="brand-name">NasMate</div><div className="brand-sub">LOCAL AI WORKBENCH</div></div><button className="icon-button mobile-close" aria-label="关闭菜单" onClick={() => setMobileMenu(false)}><X size={17} /></button></div>
        <div className="device-chip"><span className="online-dot" /><div><strong>UGOS Pro NAS</strong><span>{storageLabel}</span></div><ChevronDown size={15} className="muted-icon" /></div>
        <div className="nav-section"><div className="nav-label">工作区</div><button className="nav-item nav-item-active"><Activity size={17} /><span>任务中心</span><span className="nav-count">{tasks.length}</span></button><button className="nav-item"><FolderOpen size={17} /><span>文件空间</span></button><button className="nav-item"><Database size={17} /><span>存储健康</span></button><button className="nav-item"><Server size={17} /><span>容器诊断</span></button></div>
        <div className="nav-section task-section"><div className="nav-label task-label"><span>最近任务</span><button className="tiny-button" aria-label="新建任务" onClick={() => setInput('')}><Plus size={14} /></button></div><div className="task-list">{tasks.map((task) => <button className={`task-row ${activeTask.id === task.id ? 'task-row-active' : ''}`} key={task.id} onClick={() => setActiveTaskId(task.id)}><span className={`task-icon task-icon-${task.icon}`}>{iconForTask(task.icon)}</span><span className="task-copy"><strong>{task.title}</strong><small>{task.detail}</small></span><span className={`task-status-dot status-${task.status === '运行中' ? 'running' : task.status === '等待确认' ? 'pending' : task.status === '失败' ? 'failed' : 'done'}`} /></button>)}</div></div>
        <div className="sidebar-footer"><button className="nav-item"><Settings2 size={17} /><span>工作区设置</span></button><div className="privacy-note"><ShieldCheck size={15} /><span>本地数据边界已启用</span></div></div>
      </aside>

      <main className="main-panel">
        <header className="topbar"><button className="icon-button menu-trigger" aria-label="打开菜单" onClick={() => setMobileMenu(true)}><Menu size={19} /></button><div className="breadcrumb"><span>任务中心</span><span className="breadcrumb-sep">/</span><strong>{activeTask.title}</strong></div><div className="topbar-actions"><span className="model-pill"><span className="model-dot" /> Agent · 只读策略</span><button className="icon-button" aria-label="更多操作"><MoreHorizontal size={19} /></button></div></header>
        <div className="workspace-grid">
          <section className="conversation-panel">
            <div className="conversation-head"><div><div className="eyebrow"><span className="eyebrow-line" />自主任务</div><h1>{activeTask.title}</h1><p className="subheading">{activeTask.id === 'welcome' ? '所有写入动作都必须先经过计划与确认' : activeTask.summary}</p></div><div className="head-status"><span className="status-pulse" />{activeTask.status}</div></div>
            <div className="message-stream">{messages.map((message, index) => <div className={`message-row message-${message.role}`} key={`${message.time}-${index}`}><div className="message-avatar">{message.role === 'user' ? '你' : <Bot size={16} />}</div><div className="message-content"><div className="message-meta"><strong>{message.role === 'user' ? '你' : 'NasMate'}</strong><span>{message.time}</span></div><p>{message.text}</p></div></div>)}
              {activeTask.id !== 'welcome' && <div className="execution-card"><div className="execution-head"><div className="execution-title"><span className="tool-badge"><HardDrive size={15} /></span><strong>任务状态</strong><span className="read-only-label">受策略控制</span></div><span className="execution-time">{activeTask.time}</span></div><div className="execution-progress"><div className="progress-label"><span>后端摘要</span><strong>{activeTask.status}</strong></div><p className="state-note">{activeTask.summary}</p></div><div className="execution-details"><span><Check size={13} /> 用户认证</span><span><Check size={13} /> 授权范围</span><span className="detail-muted"><CircleDot size={13} /> 工具执行由后端决定</span></div></div>}
              <div className="assistant-note"><span className="note-icon"><LockKeyhole size={14} /></span><span>安全边界：后端只访问 UGOS 已授权目录，写入动作必须先审批。</span></div>
            </div>
            <div className="composer-wrap"><div className="composer-suggestions"><button onClick={() => setInput('找出最近 30 天增长最快的文件夹')}>增长最快的目录</button><button onClick={() => setInput('检查我的 Docker 容器是否有异常')}>检查容器异常</button><button onClick={() => setInput('验证最近一次备份是否可恢复')}>验证备份</button></div><div className="composer"><textarea value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); submitPrompt() } }} placeholder="描述你想在 NAS 上完成的任务…" rows={2} /><div className="composer-footer"><span className="composer-hint"><LockKeyhole size={13} /> 仅访问已授权范围</span><div className="composer-actions"><button className="icon-button" aria-label="添加附件"><Plus size={17} /></button><button className="send-button" aria-label="发送任务" onClick={submitPrompt}><ArrowUpRight size={17} /></button></div></div></div></div>
          </section>

          <aside className="inspector-panel">
            <div className="inspector-section overview-section"><div className="section-kicker">任务概览 <span>{storageState === 'ready' ? 'LIVE' : 'WAIT'}</span></div><div className="overview-metrics"><div><strong>{storagePercent === null ? '--' : `${storagePercent}%`}</strong><span>空间占用</span></div><div><strong>{storage?.topItems?.filter((item) => item.kind === 'directory').length ?? '--'}</strong><span>占用项</span></div><div><strong>{containerCount ?? '--'}</strong><span>容器</span></div></div><div className="storage-bar"><span style={{ width: `${storagePercent ?? 0}%` }} /></div><div className="storage-meta"><span>已用 {volume ? formatBytes(volume.usedBytes) : '--'}</span><span>可用 {volume ? formatBytes(volume.freeBytes) : '--'}</span></div>{storageState === 'unavailable' && <p className="state-note">未读取到存储数据，请在 UGOS 应用设置中授权文件夹。</p>}</div>
            <div className="inspector-section quick-section"><div className="section-heading"><span>快速操作</span><button className="icon-button small" aria-label="快速操作设置"><MoreHorizontal size={16} /></button></div><button className="quick-action" onClick={() => runQuickAction('检查 Docker 容器是否有异常')}><span className="quick-icon blue"><SquareTerminal size={16} /></span><span><strong>Docker 健康巡检</strong><small>状态 · 资源 · 日志</small></span><ArrowUpRight size={15} /></button><button className="quick-action" onClick={() => runQuickAction('查找最近 30 天增长最快的文件夹')}><span className="quick-icon green"><FileSearch size={16} /></span><span><strong>增长趋势分析</strong><small>定位空间占用</small></span><ArrowUpRight size={15} /></button><button className="quick-action" onClick={() => runQuickAction('验证最近一次备份是否可恢复')}><span className="quick-icon amber"><ShieldCheck size={16} /></span><span><strong>验证备份</strong><small>检查最近恢复点</small></span><ArrowUpRight size={15} /></button></div>
            <div className="inspector-section approval-section"><div className="section-heading"><span>待确认操作</span><span className="approval-count">{pendingPlan ? 1 : 0}</span></div>{pendingPlan ? <div className="approval-card"><div className="approval-title"><span className="approval-icon"><Download size={16} /></span><div><strong>下载计划</strong><small>{pendingPlan.sources.length} 个来源待确认</small></div></div><div className="approval-summary"><div><span>目标目录</span><strong>{pendingPlan.targetDirectory}</strong></div><div><span>预计占用</span><strong>{formatBytes(pendingPlan.estimatedBytes)}</strong></div><div><span>来源</span><strong>{pendingPlan.sources.length} 项</strong></div></div><div className="approval-buttons"><button className="deny-button" onClick={() => void updateDownload(pendingPlan, 'deny')}>拒绝</button><button className="approve-button" onClick={() => void updateDownload(pendingPlan, 'approve')}><Check size={15} /> 允许下载</button></div></div> : <p className="state-note">当前没有待确认的下载计划。</p>}</div>
            <div className="inspector-section boundary-section"><div className="boundary-title"><ShieldCheck size={16} /><strong>安全边界</strong><span>已启用</span></div><div className="boundary-list"><span><Check size={13} /> 仅访问授权目录</span><span><Check size={13} /> 写入前需确认</span><span><Check size={13} /> 默认只读取元数据</span></div></div>
          </aside>
        </div>
        <footer className="statusbar"><span><span className="online-dot" /> {storageLabel}</span><span><Zap size={13} /> 容器 {containerCount === null ? '未接入' : `${containerCount} 个`}</span><span><Database size={13} /> 备份 {backupState === 'ready' ? '可查询' : backupState === 'unavailable' ? '未接入' : '读取中'}</span><span className="statusbar-right">本地会话日志 <span className="status-dot-small" /></span></footer>
      </main>
    </div>
  )
}

export default App
