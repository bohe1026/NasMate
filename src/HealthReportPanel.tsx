import { useEffect, useState } from 'react'
import { AlertTriangle, Activity, Archive, CheckCircle2, Container, Download, RefreshCw, ShieldCheck } from 'lucide-react'
import { apiFetch } from './api'
import { formatBytes } from './format'

type HealthReport = {
  generatedAt?: string
  storage?: { suggestions?: string[] }
  docker?: { container?: { name?: string; status?: string }; findings?: string[] }[]
  backup?: { name?: string; lastRunStatus?: string; recoveryReady?: boolean }
  failedTasks?: number
  failedDownloads?: number
  comparedAt?: string
  fastestGrowing?: { path: string; deltaBytes: number }[]
  warnings?: string[]
  readOnly?: boolean
}

async function readReport() {
  const response = await apiFetch('/api/reports/health')
  if (!response.ok) throw new Error('service unavailable')
  return response.json() as Promise<HealthReport>
}

function formatDate(value?: string) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString('zh-CN', { dateStyle: 'medium', timeStyle: 'short' })
}

export function HealthReportPanel() {
  const [report, setReport] = useState<HealthReport | null>(null)
  const [state, setState] = useState<'loading' | 'ready' | 'error'>('loading')

  async function refresh() {
    setState('loading')
    try { setReport(await readReport()); setState('ready') } catch { setState('error') }
  }
  useEffect(() => {
    let live = true
    void readReport().then((data) => { if (live) { setReport(data); setState('ready') } }).catch(() => { if (live) setState('error') })
    return () => { live = false }
  }, [])

  const unhealthy = report?.docker?.filter((item) => item.container?.status !== 'running').length ?? 0
  const backupOK = report?.backup?.lastRunStatus === 'success' && report.backup.recoveryReady

  return <section className="readonly-panel health-snapshot" aria-labelledby="health-snapshot-title">
    <header className="readonly-panel-heading"><div><span className="readonly-icon mint"><Activity size={18} /></span><div><h2 id="health-snapshot-title">健康日报快照</h2><p>汇总当前授权范围内的存储、容器、备份和任务状态。</p></div></div><div><button className="icon-button" aria-label="刷新健康日报" title="刷新健康日报" onClick={() => void refresh()} disabled={state === 'loading'}><RefreshCw size={16} className={state === 'loading' ? 'spin' : undefined} /></button><span className="status-tag">只读报告</span></div></header>
    {state === 'loading' && <p className="readonly-empty">正在读取最近健康状态…</p>}
    {state === 'error' && <p className="readonly-error" role="alert">健康日报暂时无法读取，请稍后重试。</p>}
    {report && <div className="health-report-content"><div className="health-report-meta"><span>生成于 {formatDate(report.generatedAt)}</span><span><ShieldCheck size={13} />不会执行任何变更</span></div>{!!report.warnings?.length && <div className="health-warnings"><strong><AlertTriangle size={14} />需要关注</strong><ul>{report.warnings.map((warning, index) => <li key={`${warning}-${index}`}>{warning}</li>)}</ul></div>}<div className="health-summary-grid"><div><span><Activity size={14} />失败任务</span><strong>失败任务 {report.failedTasks ?? 0}</strong></div><div><span><Download size={14} />下载</span><strong>失败下载 {report.failedDownloads ?? 0}</strong></div><div><span><Container size={14} />容器</span><strong>{report.docker ? unhealthy ? `${unhealthy} 个需关注` : '全部正常' : '状态不可用'}</strong></div><div><span><Archive size={14} />备份</span><strong>{backupOK ? '最近成功' : '需要检查'}</strong></div></div>{!!report.fastestGrowing?.length && <div className="health-subsystem"><h3>增长最快目录 · 自 {formatDate(report.comparedAt)}</h3>{report.fastestGrowing.map((item, index) => <div key={`${item.path}-${index}`}><span className="health-growth-path">{item.path}</span><small>+{formatBytes(item.deltaBytes)}</small></div>)}</div>}{!!report.docker?.length && <div className="health-subsystem"><h3>容器摘要</h3>{report.docker.map((item, index) => <div key={`${item.container?.name}-${index}`}><span>{item.container?.name || '未命名容器'}</span><small>{item.findings?.[0] || item.container?.status || '状态未知'}</small></div>)}</div>}{report.backup && <div className="health-subsystem"><h3>备份摘要</h3><div><span>{report.backup.name || '未命名备份'}</span><small>{backupOK ? '恢复点可用' : '恢复有效性待检查'}</small></div></div>}{!!report.storage?.suggestions?.length && <div className="health-subsystem"><h3>存储建议</h3><ul>{report.storage.suggestions.map((suggestion, index) => <li key={`${suggestion}-${index}`}>{suggestion}</li>)}</ul></div>}</div>}
    <p className="readonly-footnote"><CheckCircle2 size={14} />报告内容来自只读诊断；修复、重启或恢复动作需要另行生成计划并审批。</p>
  </section>
}
