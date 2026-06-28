import React, { useState } from 'react'
import { getToken, setToken, getVersion, setVersion, VERSIONS } from './api.js'
import Login from './pages/Login.jsx'
import Users from './pages/Users.jsx'
import Audits from './pages/Audits.jsx'

const TABS = [
  { id: 'users', label: '用户管理' },
  { id: 'audits', label: '操作记录' },
]

const VERSION_LABELS = { x1: 'X1', v9: 'V9', v8: 'V8', zlf: 'ZLF', blk: 'BLK' }

// 共享 license 的路由：v8/v9 同属 v8v9 域，license 互通但统计/日志各自独立。
const SHARED_LICENSE_HINT = {
  v8: '（与 V9 共用同一套 license / appKey / secret，统计与日志各自独立）',
  v9: '（与 V8 共用同一套 license / appKey / secret，统计与日志各自独立）',
}

export default function App() {
  const [authed, setAuthed] = useState(!!getToken())
  const [tab, setTab] = useState('users')
  const [version, setVer] = useState(getVersion())

  if (!authed) {
    return <Login onLogin={() => setAuthed(true)} />
  }

  const logout = () => {
    setToken('')
    setAuthed(false)
  }

  const switchVersion = (v) => {
    setVersion(v)
    setVer(v)
  }

  return (
    <>
      <header className="app-header">
        <h1>DataHub 管理后台</h1>
        <nav className="nav">
          {TABS.map((t) => (
            <button
              key={t.id}
              className={tab === t.id ? 'active' : ''}
              onClick={() => setTab(t.id)}
            >
              {t.label}
            </button>
          ))}
        </nav>
        <div className="version-switch" role="group" aria-label="版本切换">
          {VERSIONS.map((v) => (
            <button
              key={v}
              className={'btn small' + (version === v ? '' : ' ghost')}
              onClick={() => switchVersion(v)}
              title={'切换到 ' + VERSION_LABELS[v] + ' 路由' + (SHARED_LICENSE_HINT[v] || '（统计与日志独立）')}
            >
              {VERSION_LABELS[v]}
            </button>
          ))}
        </div>
        <button className="btn ghost small" onClick={logout}>退出登录</button>
      </header>
      <div className="container">
        {/* version 作为 key：切换版本时强制重挂载，重新拉取该版本作用域的数据 */}
        {tab === 'users' && <Users key={'users-' + version} version={version} />}
        {tab === 'audits' && <Audits key={'audits-' + version} version={version} />}
      </div>
    </>
  )
}
