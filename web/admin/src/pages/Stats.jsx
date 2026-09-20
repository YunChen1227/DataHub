import React, { useEffect, useState } from 'react'
import { api, timeRange } from '../api.js'

// 粒度选项：按年/月/日分桶。
const GRANS = [
  { id: 'day', label: '按日' },
  { id: 'month', label: '按月' },
  { id: 'year', label: '按年' },
]

// periodInputType 按粒度决定「限定周期」输入框类型。
function periodInputType(gran) {
  if (gran === 'year') return 'number'
  if (gran === 'month') return 'month'
  return 'date'
}

function rate(total, success) {
  if (!total) return '-'
  return ((success / total) * 100).toFixed(1) + '%'
}

export default function Stats({ version }) {
  const ver = (version || '').toUpperCase()
  const [gran, setGran] = useState('day')
  const [period, setPeriod] = useState('') // 限定的具体年/月/日；空=全部时间
  const [keyword, setKeyword] = useState('')
  const [rows, setRows] = useState([])
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(false)

  const load = async (g = gran, p = period, kw = keyword) => {
    setErr('')
    setLoading(true)
    try {
      const params = new URLSearchParams()
      params.set('granularity', g)
      if (kw) params.set('q', kw)
      const { from, to } = timeRange(g, p)
      if (from) params.set('from', from)
      if (to) params.set('to', to)
      const { stats } = await api.usageStats('?' + params.toString())
      setRows(stats || [])
    } catch (e) {
      setErr(e.message)
    } finally {
      setLoading(false)
    }
  }

  // 版本切换由父级 key 强制重挂载；首挂载即拉取。
  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 切粒度时清空已选周期（避免月输入残留给日粒度）并立即重查。
  const switchGran = (g) => {
    setGran(g)
    setPeriod('')
    load(g, '', keyword)
  }

  // 合计行：当前结果集的请求/成功总数。
  const totalReq = rows.reduce((s, r) => s + (r.total || 0), 0)
  const totalOk = rows.reduce((s, r) => s + (r.success || 0), 0)

  return (
    <div className="card">
      <h2>{ver} 用量统计（按时间）</h2>
      <p className="muted">按「年 / 月 / 日」查看本路由下每个用户的请求次数与成功次数。「请求次数」= 收到的全部请求（含鉴权/参数失败）；「成功次数」= 查得数据（与用户列表的「成功查得数」口径一致）。时间按北京时间（+08:00）分桶。</p>

      <form className="toolbar" onSubmit={(e) => { e.preventDefault(); load() }}>
        <div>
          <label>粒度</label>
          <div className="version-switch" role="group" aria-label="统计粒度">
            {GRANS.map((g) => (
              <button
                key={g.id}
                type="button"
                className={'btn small' + (gran === g.id ? '' : ' ghost')}
                onClick={() => switchGran(g.id)}
              >
                {g.label}
              </button>
            ))}
          </div>
        </div>
        <div>
          <label>限定周期（可空=全部）</label>
          <input
            type={periodInputType(gran)}
            value={period}
            onChange={(e) => setPeriod(e.target.value)}
            placeholder={gran === 'year' ? '如 2026' : ''}
          />
        </div>
        <div>
          <label>用户检索（uuid / 名称 / 手机号）</label>
          <input value={keyword} onChange={(e) => setKeyword(e.target.value)} placeholder="全部用户" />
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
              <th>时间</th><th>uuid</th><th>名称</th>
              <th>请求次数</th><th>成功次数</th><th>成功率</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={r.bucket + '|' + r.appKey + '|' + i}>
                <td className="muted">{r.bucket}</td>
                <td><code>{r.appKey}</code></td>
                <td>{r.name || '-'}</td>
                <td><strong>{r.total}</strong></td>
                <td><strong>{r.success}</strong></td>
                <td className="muted">{rate(r.total, r.success)}</td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr><td colSpan="6" className="muted">暂无统计数据</td></tr>
            )}
          </tbody>
          {rows.length > 0 && (
            <tfoot>
              <tr>
                <td colSpan="3"><strong>合计（当前结果）</strong></td>
                <td><strong>{totalReq}</strong></td>
                <td><strong>{totalOk}</strong></td>
                <td className="muted">{rate(totalReq, totalOk)}</td>
              </tr>
            </tfoot>
          )}
        </table>
      </div>
    </div>
  )
}
