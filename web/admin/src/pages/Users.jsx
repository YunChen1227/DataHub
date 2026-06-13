import React, { useEffect, useState } from 'react'
import { api } from '../api.js'

const emptyForm = { name: '', serviceTotal: 100000, upstreamTotal: 100000, ipWhitelist: '' }

function parseIPs(s) {
  return (s || '')
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean)
}

export default function Users() {
  const [users, setUsers] = useState([])
  const [err, setErr] = useState('')
  const [form, setForm] = useState(emptyForm)
  const [secret, setSecret] = useState(null) // {appKey, secret, title}
  const [editing, setEditing] = useState(null)

  const load = async () => {
    setErr('')
    try {
      const { users } = await api.listUsers()
      setUsers(users || [])
    } catch (e) {
      setErr(e.message)
    }
  }

  useEffect(() => {
    load()
  }, [])

  const create = async (e) => {
    e.preventDefault()
    setErr('')
    try {
      const res = await api.createUser({
        name: form.name,
        serviceTotal: Number(form.serviceTotal),
        upstreamTotal: Number(form.upstreamTotal),
        ipWhitelist: parseIPs(form.ipWhitelist),
      })
      setForm(emptyForm)
      setSecret({ appKey: res.user.appKey, secret: res.secret, title: '新用户已创建' })
      load()
    } catch (e) {
      setErr(e.message)
    }
  }

  const saveEdit = async () => {
    setErr('')
    try {
      await api.updateUser(editing.licenseId, {
        status: editing.status,
        serviceTotal: Number(editing.serviceTotal),
        upstreamTotal: Number(editing.upstreamTotal),
        ipWhitelist: parseIPs(editing.ipWhitelistText),
      })
      setEditing(null)
      load()
    } catch (e) {
      setErr(e.message)
    }
  }

  const remove = async (u) => {
    if (!confirm(`确认删除用户 ${u.appKey}（${u.name || '-'}）？`)) return
    setErr('')
    try {
      await api.deleteUser(u.licenseId)
      load()
    } catch (e) {
      setErr(e.message)
    }
  }

  const rotate = async (u) => {
    if (!confirm(`确认为 ${u.appKey} 轮换 secret？旧 secret 立即失效。`)) return
    setErr('')
    try {
      const { secret } = await api.rotateSecret(u.licenseId)
      setSecret({ appKey: u.appKey, secret, title: 'secret 已轮换' })
    } catch (e) {
      setErr(e.message)
    }
  }

  return (
    <>
      <div className="card">
        <h2>新建用户</h2>
        <form className="form-grid" onSubmit={create}>
          <div>
            <label>名称/备注</label>
            <input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
          </div>
          <div>
            <label>维度①额度 (对用户计费)</label>
            <input type="number" value={form.serviceTotal} onChange={(e) => setForm({ ...form, serviceTotal: e.target.value })} />
          </div>
          <div>
            <label>维度②额度 (我方成本)</label>
            <input type="number" value={form.upstreamTotal} onChange={(e) => setForm({ ...form, upstreamTotal: e.target.value })} />
          </div>
          <div>
            <label>IP 白名单 (逗号/空格分隔, 可空)</label>
            <input value={form.ipWhitelist} onChange={(e) => setForm({ ...form, ipWhitelist: e.target.value })} placeholder="1.2.3.4, 10.0.0.0/8" />
          </div>
          <div>
            <button className="btn" type="submit">创建并生成密钥</button>
          </div>
        </form>
      </div>

      {err && <div className="error">{err}</div>}

      <div className="card">
        <h2>用户列表（{users.length}）</h2>
        <div style={{ overflowX: 'auto' }}>
          <table>
            <thead>
              <tr>
                <th>appKey</th><th>名称</th><th>状态</th>
                <th>维度①(用/总)</th><th>维度②(已计/预留/总)</th>
                <th>IP 白名单</th><th>创建时间</th><th>操作</th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.licenseId}>
                  <td><code>{u.appKey}</code></td>
                  <td>{u.name || '-'}</td>
                  <td><span className={'badge ' + u.status}>{u.status}</span></td>
                  <td>{u.serviceUsed} / {u.serviceTotal}</td>
                  <td>{u.upstreamCommitted} / {u.upstreamReserved} / {u.upstreamTotal}</td>
                  <td>{(u.ipWhitelist && u.ipWhitelist.length) ? u.ipWhitelist.join(', ') : <span className="muted">不限制</span>}</td>
                  <td className="muted">{new Date(u.createdAt).toLocaleString()}</td>
                  <td className="row-actions">
                    <button className="btn ghost small" onClick={() => setEditing({
                      ...u,
                      ipWhitelistText: (u.ipWhitelist || []).join(', '),
                    })}>编辑</button>
                    <button className="btn ghost small" onClick={() => rotate(u)}>轮换密钥</button>
                    <button className="btn danger small" onClick={() => remove(u)}>删除</button>
                  </td>
                </tr>
              ))}
              {users.length === 0 && (
                <tr><td colSpan="8" className="muted">暂无用户</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      {editing && (
        <div className="modal-backdrop" onClick={() => setEditing(null)}>
          <div className="card modal" onClick={(e) => e.stopPropagation()}>
            <h2>编辑用户 — {editing.appKey}</h2>
            <div className="field">
              <label>状态</label>
              <select value={editing.status} onChange={(e) => setEditing({ ...editing, status: e.target.value })}>
                <option value="ACTIVE">ACTIVE</option>
                <option value="SUSPENDED">SUSPENDED</option>
                <option value="EXPIRED">EXPIRED</option>
              </select>
            </div>
            <div className="field">
              <label>维度①额度</label>
              <input type="number" value={editing.serviceTotal} onChange={(e) => setEditing({ ...editing, serviceTotal: e.target.value })} />
            </div>
            <div className="field">
              <label>维度②额度</label>
              <input type="number" value={editing.upstreamTotal} onChange={(e) => setEditing({ ...editing, upstreamTotal: e.target.value })} />
            </div>
            <div className="field">
              <label>IP 白名单</label>
              <input value={editing.ipWhitelistText} onChange={(e) => setEditing({ ...editing, ipWhitelistText: e.target.value })} />
            </div>
            <div className="row-actions" style={{ marginTop: 12 }}>
              <button className="btn" onClick={saveEdit}>保存</button>
              <button className="btn ghost" onClick={() => setEditing(null)}>取消</button>
            </div>
          </div>
        </div>
      )}

      {secret && (
        <div className="modal-backdrop" onClick={() => setSecret(null)}>
          <div className="card modal" onClick={(e) => e.stopPropagation()}>
            <h2>{secret.title}</h2>
            <p className="muted">appKey：<code>{secret.appKey}</code></p>
            <p className="muted">secret（仅此一次展示，请立即保存）：</p>
            <div className="secret-box">{secret.secret}</div>
            <button className="btn" onClick={() => setSecret(null)}>我已保存</button>
          </div>
        </div>
      )}
    </>
  )
}
