import React, { useEffect, useState } from 'react'
import { api, timeRange } from '../api.js'

// 时间粒度：不限 / 按年 / 按月 / 按日。
const GRANS = [
  { id: '', label: '不限时间' },
  { id: 'year', label: '按年' },
  { id: 'month', label: '按月' },
  { id: 'day', label: '按日' },
]

// 成败筛选：全部 / 仅成功(查得数据) / 仅失败(未查得)。
const STATUSES = [
  { id: '', label: '全部' },
  { id: 'success', label: '成功（查得数据）' },
  { id: 'fail', label: '失败（未查得）' },
]

function periodInputType(gran) {
  if (gran === 'year') return 'number'
  if (gran === 'month') return 'month'
  return 'date'
}

export default function Audits({ version }) {
  const ver = (version || '').toUpperCase()
  const [rows, setRows] = useState([])
  const [err, setErr] = useState('')
  const [keyword, setKeyword] = useState('')
  const [busiCode, setBusiCode] = useState('')
  const [gran, setGran] = useState('')
  const [period, setPeriod] = useState('')
  const [status, setStatus] = useState('')
  const [loading, setLoading] = useState(false)

  const load = async () => {
    setErr('')
    setLoading(true)
    try {
      const params = new URLSearchParams()
      if (keyword) params.set('q', keyword)
      if (busiCode) params.set('busiCode', busiCode)
      if (status) params.set('status', status)
      const { from, to } = timeRange(gran, period)
      if (from) params.set('from', from)
      if (to) params.set('to', to)
      params.set('limit', '200')
      const q = params.toString()
      const { audits } = await api.listAudits(q ? '?' + q : '')
      setRows(audits || [])
    } catch (e) {
      setErr(e.message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div className="card">
      <h2>{ver} 操作记录 / 审计日志</h2>
      <p className="muted">仅展示 {ver} 路由自己的调用记录，与其它路由相互独立（v8/v9 虽共用 license，操作日志也按路由分开）。</p>
      <p className="muted">「走缓存」= 命中自然月结果缓存：本月已查过同一人，直接回放首查结果，没有再调上游。这类行「调用上游=否」但「计费=计」，上游uid/logId 是首查时的原值，向上游对账时须排除。</p>
      <form className="toolbar" onSubmit={(e) => { e.preventDefault(); load() }}>
        <div>
          <label>检索（uuid / 名称 / 手机号）</label>
          <input value={keyword} onChange={(e) => setKeyword(e.target.value)} placeholder="全部" />
        </div>
        <div>
          <label>busiCode 筛选</label>
          <input value={busiCode} onChange={(e) => setBusiCode(e.target.value)} placeholder="如 10 / 1000 / 1007" />
        </div>
        <div>
          <label>时间粒度</label>
          <select value={gran} onChange={(e) => { setGran(e.target.value); setPeriod('') }}>
            {GRANS.map((g) => <option key={g.id} value={g.id}>{g.label}</option>)}
          </select>
        </div>
        {gran && (
          <div>
            <label>{gran === 'year' ? '年份' : gran === 'month' ? '月份' : '日期'}</label>
            <input
              type={periodInputType(gran)}
              value={period}
              onChange={(e) => setPeriod(e.target.value)}
              placeholder={gran === 'year' ? '如 2026' : ''}
            />
          </div>
        )}
        <div>
          <label>展示</label>
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            {STATUSES.map((s) => <option key={s.id} value={s.id}>{s.label}</option>)}
          </select>
        </div>
        <div>
          <button className="btn" type="submit" disabled={loading}>{loading ? '查询中…' : '查询'}</button>
        </div>
      </form>

      {err && <div className="error">{err}</div>}

      <div style={{ overflowX: 'auto' }}>
        <table>
          <thead>
            <tr>
              <th>时间</th><th>requestId(seqNo)</th><th>appKey</th><th>来源IP</th>
              <th>调用上游</th><th>走缓存</th><th>查得数据</th><th>计费</th>
              <th>busiCode</th><th>上游code</th><th>上游uid</th><th>上游logId</th>
              <th>耗时(ms)</th><th>入参(脱敏)</th><th>tradeNo/reqid</th><th>错误</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((a) => (
              <tr key={a.id}>
                <td className="muted">{new Date(a.createdAt).toLocaleString()}</td>
                <td><code>{a.requestId}</code></td>
                <td>{a.appKey || '-'}</td>
                <td>{a.clientIp || '-'}</td>
                <td className={a.calledUpstream ? 'tag-ok' : 'tag-no'}>{a.calledUpstream ? '是' : '否'}</td>
                <td className={a.fromCache ? 'tag-ok' : 'tag-no'}>{a.fromCache ? '是' : '否'}</td>
                <td className={a.foundData ? 'tag-ok' : 'tag-no'}>{a.foundData ? '是' : '否'}</td>
                <td className={a.billed ? 'tag-ok' : 'tag-no'}>{a.billed ? '计' : '不计'}</td>
                <td>{a.busiCode}</td>
                <td>{a.upstreamCode || '-'}</td>
                <td>{a.upstreamUid || '-'}</td>
                <td className="muted">{a.upstreamLogId || '-'}</td>
                <td>{a.latencyMs}</td>
                <td className="muted">{[a.nameMask, a.idCardMask, a.mobileMask].filter(Boolean).join(' / ')}</td>
                <td className="muted">{[a.tradeNo, a.reqid].filter(Boolean).join(' / ')}</td>
                <td className="tag-err">{a.errMsg || ''}</td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr><td colSpan="16" className="muted">暂无记录</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
