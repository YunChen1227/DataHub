// Admin API client (DESIGN §16): fetch wrapper with Bearer JWT.
const BASE = '/admin/api'

let token = localStorage.getItem('adminToken') || ''

export function setToken(t) {
  token = t || ''
  if (token) localStorage.setItem('adminToken', token)
  else localStorage.removeItem('adminToken')
}

export function getToken() {
  return token
}

async function req(method, path, body) {
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
  login: (username, password) => req('POST', '/login', { username, password }),
  listUsers: () => req('GET', '/users'),
  createUser: (u) => req('POST', '/users', u),
  updateUser: (id, u) => req('PATCH', '/users/' + encodeURIComponent(id), u),
  deleteUser: (id) => req('DELETE', '/users/' + encodeURIComponent(id)),
  rotateSecret: (id) => req('POST', '/users/' + encodeURIComponent(id) + '/rotate-secret'),
  listAudits: (query) => req('GET', '/audits' + (query || '')),
  getGlobalIP: () => req('GET', '/ip-whitelist'),
  setGlobalIP: (cidrs) => req('PUT', '/ip-whitelist', { cidrs }),
}
