import React, { useState } from 'react'
import { api, setToken } from '../api.js'

export default function Login({ onLogin }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e) => {
    e.preventDefault()
    setErr('')
    setBusy(true)
    try {
      const { token } = await api.login(username, password)
      setToken(token)
      onLogin()
    } catch (e) {
      setErr(e.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login-wrap">
      <form className="card login-box" onSubmit={submit}>
        <h2>管理员登录</h2>
        <div className="field">
          <label>用户名</label>
          <input value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
        </div>
        <div className="field">
          <label>密码</label>
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </div>
        {err && <div className="error">{err}</div>}
        <button className="btn" type="submit" disabled={busy} style={{ width: '100%' }}>
          {busy ? '登录中…' : '登录'}
        </button>
      </form>
    </div>
  )
}
