import React, { useEffect, useState } from 'react'
import { api, downloadReport, timeRange, VERSIONS, VERSION_LABELS } from '../api.js'

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

// today 返回北京时间的今天 (YYYY-MM-DD)。
function today() {
  return new Date(Date.now() + 8 * 3600000).toISOString().slice(0, 10)
}

// firstOfMonth 返回北京时间本月 1 日。
function firstOfMonth() {
  return today().slice(0, 8) + '01'
}

// ReportExport 导出出账/对账 Excel（仅计数，不涉金额）：
//   - 下游账单：按客户跨所有路由统计 调用/查得/查询/计费 次数，每条路由单独计费、
//     标注查得计费/查询计费。
//   - 上游对账：每条路由就是一个上游，统计该上游区间内真正调用（已排除走缓存）。
function ReportExport({ version }) {
  const [kind, setKind] = useState('downstream')
  const [from, setFrom] = useState(firstOfMonth())
  const [to, setTo] = useState(today())
  const [customers, setCustomers] = useState([])
  const [customer, setCustomer] = useState('')
  const [route, setRoute] = useState(version || VERSIONS[0])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  // 下游账单的客户下拉：取当前路由的用户名（跨路由客户大多一致，够用）。
  useEffect(() => {
    api
      .listUsers()
      .then((d) => {
        const names = [...new Set((d.users || []).map((u) => u.name).filter(Boolean))]
        names.sort((a, b) => a.localeCompare(b, 'zh'))
        setCustomers(names)
      })
      .catch((e) => setErr(e.message))
  }, [version])

  const exportExcel = async (e) => {
    e.preventDefault()
    setErr('')
    setBusy(true)
    try {
      if (kind === 'downstream') {
        await downloadReport('downstream', { customer, from, to })
      } else {
        await downloadReport('upstream', { route, from, to })
      }
    } catch (e2) {
      setErr(e2.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="card">
      <h2>导出对账单（仅计数）</h2>
      <p className="muted">
        下游账单：所选客户在区间内的 调用/查得/查询/计费 次数，按路由拆行，每条路由标注查得计费或查询计费。
        上游对账：每条路由就是一个上游，统计该上游区间内真正调用的请求（已自动排除走缓存回放）。
        日期按北京时间，含截止日当天，单次最长 366 天。
      </p>
      <form className="toolbar" onSubmit={exportExcel}>
        <div>
          <label>类型</label>
          <div className="version-switch" role="group" aria-label="导出类型">
            <button
              type="button"
              className={'btn small' + (kind === 'downstream' ? '' : ' ghost')}
              onClick={() => setKind('downstream')}
            >
              下游账单
            </button>
            <button
              type="button"
              className={'btn small' + (kind === 'upstream' ? '' : ' ghost')}
              onClick={() => setKind('upstream')}
            >
              上游对账
            </button>
          </div>
        </div>
        <div>
          <label>起始日期</label>
          <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} required />
        </div>
        <div>
          <label>截止日期</label>
          <input type="date" value={to} onChange={(e) => setTo(e.target.value)} required />
        </div>
        {kind === 'downstream' ? (
          <div>
            <label>客户</label>
            <select value={customer} onChange={(e) => setCustomer(e.target.value)}>
              <option value="">全部客户</option>
              {customers.map((n) => (
                <option key={n} value={n}>{n}</option>
              ))}
            </select>
          </div>
        ) : (
          <div>
            <label>上游（路由）</label>
            <select value={route} onChange={(e) => setRoute(e.target.value)}>
              {VERSIONS.map((v) => (
                <option key={v} value={v}>{VERSION_LABELS[v] || v.toUpperCase()}</option>
              ))}
            </select>
          </div>
        )}
        <div>
          <button className="btn" type="submit" disabled={busy}>
            {busy ? '生成中…' : '导出 Excel'}
          </button>
        </div>
      </form>
      {err && <div className="error">{err}</div>}
    </div>
  )
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
    <>
    <ReportExport version={version} />
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
    </>
  )
}
