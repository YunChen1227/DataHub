import React, { useEffect, useState } from 'react'
import { api } from '../api.js'

const COLS = 11
const PAGE_SIZES = [10, 20, 50, 100]

export default function Audits({ version }) {
  const ver = (version || '').toUpperCase()
  const [rows, setRows] = useState([])
  const [total, setTotal] = useState(0)
  const [err, setErr] = useState('')
  const [keyword, setKeyword] = useState('')
  const [reqid, setReqid] = useState('')
  const [pageSize, setPageSize] = useState(PAGE_SIZES[0])
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(false)

  const pages = Math.max(1, Math.ceil(total / pageSize))

  const load = async (p = page, size = pageSize) => {
    setErr('')
    setLoading(true)
    try {
      const params = new URLSearchParams()
      if (keyword.trim()) params.set('q', keyword.trim())
      if (reqid.trim()) params.set('reqid', reqid.trim())
      params.set('limit', String(size))
      params.set('offset', String((p - 1) * size))
      const d = await api.listAudits('?' + params.toString())
      setRows(d.audits || [])
      setTotal(d.total || 0)
      setPage(p)
    } catch (e) {
      setErr(e.message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load(1)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const changePageSize = (size) => {
    setPageSize(size)
    load(1, size)
  }

  return (
    <div className="card">
      <h2>{ver} 操作记录 / 审计日志</h2>
      <p className="muted">仅展示 {ver} 路由自己的调用记录，与其它路由相互独立（v8/v9 虽共用 license，操作日志也按路由分开）。</p>
      <p className="muted">「走缓存」= 命中自然月结果缓存：本月已查过同一人，直接回放首查结果，没有再调上游。这类行「调用上游=否」但「计费=计」，向上游对账时须排除（上游对账导出已自动排除）。</p>
      <form className="toolbar" onSubmit={(e) => { e.preventDefault(); load(1) }}>
        <div>
          <label>检索（uuid / 名称 / 手机号）</label>
          <input value={keyword} onChange={(e) => setKeyword(e.target.value)} placeholder="全部" />
        </div>
        <div>
          <label>reqid</label>
          <input value={reqid} onChange={(e) => setReqid(e.target.value)} placeholder="reqid 或 requestId" />
        </div>
        <div>
          <label>每页</label>
          <select value={pageSize} onChange={(e) => changePageSize(Number(e.target.value))} disabled={loading}>
            {PAGE_SIZES.map((n) => (
              <option key={n} value={n}>{n} 条</option>
            ))}
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
              <th>时间</th><th>requestId</th><th>appKey</th>
              <th>调用上游</th><th>走缓存</th><th>查得数据</th><th>计费</th>
              <th>busiCode</th><th>上游code</th><th>上游uid</th><th>错误</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((a) => (
              <tr key={a.id}>
                <td className="muted">{new Date(a.createdAt).toLocaleString()}</td>
                <td><code>{a.requestId}</code></td>
                <td>{a.appKey || '-'}</td>
                <td className={a.calledUpstream ? 'tag-ok' : 'tag-no'}>{a.calledUpstream ? '是' : '否'}</td>
                <td className={a.fromCache ? 'tag-ok' : 'tag-no'}>{a.fromCache ? '是' : '否'}</td>
                <td className={a.foundData ? 'tag-ok' : 'tag-no'}>{a.foundData ? '是' : '否'}</td>
                <td className={a.billed ? 'tag-ok' : 'tag-no'}>{a.billed ? '计' : '不计'}</td>
                <td>{a.busiCode}</td>
                <td>{a.upstreamCode || '-'}</td>
                <td>{a.upstreamUid || '-'}</td>
                <td className="tag-err">{a.errMsg || ''}</td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr><td colSpan={COLS} className="muted">暂无记录</td></tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="toolbar" style={{ marginTop: 14, alignItems: 'center' }}>
        <span className="muted">共 {total} 条，第 {page} / {pages} 页</span>
        <button className="btn" type="button" disabled={loading || page <= 1} onClick={() => load(page - 1)}>
          上一页
        </button>
        <button className="btn" type="button" disabled={loading || page >= pages} onClick={() => load(page + 1)}>
          下一页
        </button>
      </div>
    </div>
  )
}
