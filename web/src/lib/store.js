// Единый store состояния: GET /v1/state + SSE /v1/events с авто-реконнектом.
import { writable } from 'svelte/store'
import { api } from './api.js'
import { notifyStateTransition } from './notify.js'

// null — ещё не загружено; объект — снимок состояния агента.
export const appState = writable(null)
// true — SSE живо; false — «агент недоступен» (бейдж в шапке).
export const agentOnline = writable(true)

let es = null
let prev = null

export function startEvents() {
  if (es) return
  connect()
}

function connect() {
  es = new EventSource('/v1/events')
  es.addEventListener('state', (ev) => {
    agentOnline.set(true)
    const next = JSON.parse(ev.data)
    notifyStateTransition(prev, next)
    prev = next
    appState.set(next)
  })
  es.onerror = () => {
    agentOnline.set(false)
    // EventSource переподключается сам; если соединение закрыто навсегда
    // (401 после logout) — перезапустим вручную через 3 с.
    if (es && es.readyState === EventSource.CLOSED) {
      es = null
      setTimeout(connect, 3000)
    }
  }
}

export function stopEvents() {
  if (es) {
    es.close()
    es = null
  }
  prev = null
}

// Разовая загрузка снимка (до подключения SSE или после логина).
export async function refreshState() {
  try {
    const s = await api.state()
    prev = s
    appState.set(s)
    agentOnline.set(true)
  } catch {
    agentOnline.set(false)
  }
}
