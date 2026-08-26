// Мини-роутер на location.hash: '#/', '#/settings/uplink', '#/diagnostics'.
import { readable } from 'svelte/store'

function parse() {
  const h = location.hash.replace(/^#\/?/, '')
  return h === '' ? '/' : '/' + h
}

export const route = readable(parse(), (set) => {
  const onChange = () => set(parse())
  window.addEventListener('hashchange', onChange)
  return () => window.removeEventListener('hashchange', onChange)
})

export function go(path) {
  location.hash = '#' + path
}
