// Браузерные уведомления (Web Notifications): opt-in, переключатель в Access.
// Спека web-ui: reconnect_exhausted и падение uplink'а.

export function notificationsEnabled() {
  return localStorage.getItem('detour_notify') === '1' && Notification?.permission === 'granted'
}

export async function setNotifications(on) {
  if (!on) {
    localStorage.setItem('detour_notify', '0')
    return false
  }
  if (!('Notification' in window)) return false
  const perm = await Notification.requestPermission()
  const granted = perm === 'granted'
  localStorage.setItem('detour_notify', granted ? '1' : '0')
  return granted
}

function show(title, body) {
  try {
    const n = new Notification(title, { body })
    n.onclick = () => {
      window.focus()
      location.hash = '#/'
      n.close()
    }
  } catch {
    // страница без разрешения/фон — молча пропускаем
  }
}

// notifyStateTransition сравнивает снимки состояния и шлёт уведомления
// на значимые переходы.
export function notifyStateTransition(prev, next) {
  if (!prev || !notificationsEnabled()) return
  const wasErr = prev.lastError?.code
  const isErr = next.lastError?.code
  if (isErr === 'reconnect_exhausted' && wasErr !== 'reconnect_exhausted') {
    show('Detour: reconnect failed', 'Automatic reconnection attempts are exhausted. Open the dashboard to retry.')
  }
  if (prev.uplink?.status !== 'down' && next.uplink?.status === 'down') {
    show('Detour: uplink is down', `Uplink (${next.uplink.mode}) is unreachable. VPN traffic is blocked by the kill switch.`)
  }
}
