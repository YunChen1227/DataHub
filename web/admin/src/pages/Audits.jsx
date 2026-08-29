import React, { useEffect, useState } from 'react'
import { api } from '../api.js'

export default function Audits({ version }) {
  const ver = (version || '').toUpperCase()
  const [rows, setRows] = useState([])
  const [err, setErr] = useState('')
  const [keyword, setKeyword] = useState('')
  const [busiCode, setBusiCode] = useState('')
  const [loading, setLoading] = useState(false)

  const load = async () => {
    setErr('')
    setLoading(true)
    try {
      const params = new URLSearchParams()
      if (keyword) params.set('q', keyword)
      if (busiCode) params.set('busiCode', busiCode)
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
