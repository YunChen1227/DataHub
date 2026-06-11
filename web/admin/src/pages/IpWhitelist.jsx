import React, { useEffect, useState } from 'react'
import { api } from '../api.js'

export default function IpWhitelist() {
  const [text, setText] = useState('')
  const [err, setErr] = useState('')
  const [msg, setMsg] = useState('')
  const [loading, setLoading] = useState(false)

  const load = async () => {
    setErr('')
    try {
      const { cidrs } = await api.getGlobalIP()
      setText((cidrs || []).join('\n'))
    } catch (e) {
      setErr(e.message)
    }
  }

  useEffect(() => {
    load()
  }, [])

  const save = async () => {
    setErr('')
    setMsg('')
    setLoading(true)
    try {
      const cidrs = text.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean)
      await api.setGlobalIP(cidrs)
      setMsg('已保存。为空表示不限制访问。')
      load()
    } catch (e) {
      setErr(e.message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="card">
      <h2>全局 IP 白名单</h2>
      <p className="muted">
        作用于业务入口（doCheck / quota）。每行一个：支持单 IP（如 <code>203.0.113.5</code>）或 CIDR 段（如 <code>10.0.0.0/8</code>）。
        留空表示不限制。每用户白名单在「用户管理」中单独配置。
      </p>
      <textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={10}
        style={{
          width: '100%', background: 'var(--bg)', color: 'var(--text)',
          border: '1px solid var(--line)', borderRadius: 8, padding: 12,
          fontFamily: 'monospace', fontSize: 13,
        }}
        placeholder="203.0.113.5&#10;10.0.0.0/8"
      />
      {err && <div className="error">{err}</div>}
      {msg && <div className="muted" style={{ marginTop: 8 }}>{msg}</div>}
      <div style={{ marginTop: 12 }}>
        <button className="btn" onClick={save} disabled={loading}>{loading ? '保存中…' : '保存'}</button>
      </div>
    </div>
  )
}
