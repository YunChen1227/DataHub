// Admin API client (DESIGN §16): fetch wrapper with Bearer JWT.
// 三版本 (x1/v9/v8) 数据完全隔离：登录走统一控制面 /admin/api/login，用户/审计
// 等数据请求带版本前缀 /admin/api/{ver}/...，仅在所选版本作用域内操作。
const BASE = '/admin/api'

export const VERSIONS = ['x1', 'v9', 'v8']

let token = localStorage.getItem('adminToken') || ''
let version = localStorage.getItem('adminVersion') || 'x1'
if (!VERSIONS.includes(version)) version = 'x1'

export function setToken(t) {
  token = t || ''
  if (token) localStorage.setItem('adminToken', token)
  else localStorage.removeItem('adminToken')
}

export function getToken() {
  return token
}

export function setVersion(v) {
  version = VERSIONS.includes(v) ? v : 'x1'
  localStorage.setItem('adminVersion', version)
}

export function getVersion() {
  return version
}

// req issues a version-scoped data request (prefixes /{ver}).
async function req(method, path, body) {
  return rawReq(method, '/' + version + path, body)
}

// rawReq issues a request against the raw admin base (no version prefix),
// used for the shared control-plane login.
async function rawReq(method, path, body) {
  const headers = { 'Content-Type': 'application/json' }
  if (token) headers['Authorization'] = 'Bearer ' + token
  const res = await fetch(BASE + path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const text = await res.text()
  const data = text ? JSON.parse(text) : {}
  if (!res.ok) {
    const err = new Error(data.error || 'HTTP ' + res.status)
    err.status = res.status
    throw err
  }
  return data
}

export const api = {
  login: (username, password) => rawReq('POST', '/login', { username, password }),
  listUsers: (q) => req('GET', '/users' + (q ? '?q=' + encodeURIComponent(q) : '')),
  createUser: (u) => req('POST', '/users', u),
  updateUser: (id, u) => req('PATCH', '/users/' + encodeURIComponent(id), u),
  deleteUser: (id) => req('DELETE', '/users/' + encodeURIComponent(id)),
  rotateSecret: (id) => req('POST', '/users/' + encodeURIComponent(id) + '/rotate-secret'),
  listAudits: (query) => req('GET', '/audits' + (query || '')),
}
