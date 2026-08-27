// API-клиент: fetch с CSRF (double submit) и обработкой 401.

function csrfToken() {
  const m = document.cookie.match(/(?:^|;\s*)detour_csrf=([^;]+)/)
  return m ? m[1] : ''
}

export class ApiError extends Error {
  constructor(status, body) {
    super(body?.message || body?.error || `HTTP ${status}`)
    this.status = status
    this.body = body || {}
  }
}

async function request(method, path, body) {
  const headers = {}
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (method !== 'GET') headers['X-CSRF-Token'] = csrfToken()
  const resp = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  let data = null
  const ct = resp.headers.get('Content-Type') || ''
  if (ct.includes('application/json')) data = await resp.json().catch(() => null)
  if (!resp.ok) throw new ApiError(resp.status, data)
  return data
}

export const api = {
  get: (path) => request('GET', path),
  post: (path, body) => request('POST', path, body ?? {}),
  patch: (path, body) => request('PATCH', path, body),

  authStatus: () => request('GET', '/v1/auth/status'),
  setup: (password) => request('POST', '/v1/auth/setup', { password }),
  skip: () => request('POST', '/v1/auth/skip', {}),
  disableAuth: (current) => request('POST', '/v1/auth/disable', { current }),
  login: (password) => request('POST', '/v1/auth/login', { password }),
  logout: () => request('POST', '/v1/auth/logout', {}),
  logoutAll: () => request('POST', '/v1/auth/logout-all', {}),
  changePassword: (current, next) => request('POST', '/v1/auth/password', { current, new: next }),

  state: () => request('GET', '/v1/state'),
  config: () => request('GET', '/v1/config'),
  patchConfig: (patch) => request('PATCH', '/v1/config', patch),
  version: () => request('GET', '/v1/version'),
  locations: () => request('GET', '/v1/locations'),
  logs: (params) => request('GET', '/v1/logs?' + new URLSearchParams(params)),
  token: () => request('GET', '/v1/auth/token'),
  rotateToken: () => request('POST', '/v1/auth/token/rotate', {}),

  action: (name, body) => request('POST', `/v1/actions/${name}`, body ?? {}),
  operation: (id) => request('GET', `/v1/operations/${id}`),

  // Запускает действие и ждёт завершения операции (poll ≤ timeoutMs).
  async runAction(name, body, timeoutMs = 180000) {
    const { operation } = await this.action(name, body)
    const deadline = Date.now() + timeoutMs
    for (;;) {
      const op = await this.operation(operation)
      if (op.status === 'succeeded') return op
      if (op.status === 'failed') {
        throw new ApiError(200, { error: op.errorCode, message: op.error })
      }
      if (Date.now() > deadline) throw new ApiError(0, { error: 'timeout' })
      await new Promise((r) => setTimeout(r, 700))
    }
  },
}
