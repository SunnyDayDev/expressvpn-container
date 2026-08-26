<script>
  import { onMount } from 'svelte'
  import { api } from './lib/api.js'
  import { appState, agentOnline, startEvents, stopEvents, refreshState } from './lib/store.js'
  import { route, go } from './lib/router.js'
  import { t } from './lib/i18n.js'

  import Setup from './pages/Setup.svelte'
  import Login from './pages/Login.svelte'
  import Dashboard from './pages/Dashboard.svelte'
  import ExpressVPN from './pages/settings/ExpressVPN.svelte'
  import Uplink from './pages/settings/Uplink.svelte'
  import Proxy from './pages/settings/Proxy.svelte'
  import Container from './pages/settings/Container.svelte'
  import Access from './pages/settings/Access.svelte'
  import Diagnostics from './pages/Diagnostics.svelte'

  // phase: loading | setup | login | app
  let phase = $state('loading')

  async function boot() {
    try {
      const st = await api.authStatus()
      if (!st.passwordSet) phase = 'setup'
      else if (!st.authenticated) phase = 'login'
      else {
        phase = 'app'
        await refreshState()
        startEvents()
      }
    } catch {
      // Агент недоступен — пробуем снова.
      setTimeout(boot, 2000)
    }
  }
  onMount(boot)

  function onAuthed() {
    phase = 'app'
    refreshState()
    startEvents()
  }

  async function signOut() {
    stopEvents()
    try { await api.logout() } catch {}
    phase = 'login'
  }

  const pages = {
    '/': Dashboard,
    '/settings/expressvpn': ExpressVPN,
    '/settings/uplink': Uplink,
    '/settings/proxy': Proxy,
    '/settings/container': Container,
    '/settings/access': Access,
    '/diagnostics': Diagnostics,
  }

  let Page = $derived(pages[$route] ?? Dashboard)

  const connDot = {
    connected: 'ok', connecting: 'warn', reconnecting: 'warn',
    disconnected: '', error: 'err',
  }
</script>

{#if phase === 'loading'}
  <div class="center-page"><p class="muted">Detour…</p></div>
{:else if phase === 'setup'}
  <Setup done={onAuthed} />
{:else if phase === 'login'}
  <Login done={onAuthed} />
{:else}
  <div class="layout">
    <nav class="sidebar">
      <div class="side-brand">
        <div class="brand-mark">D</div>
        <div>
          <div style="font-weight:650">Detour</div>
          <div class="small muted">ExpressVPN sidecar</div>
        </div>
      </div>

      <a class="side-link" class:active={$route === '/'} href="#/">
        <span class="row" style="gap:8px">
          <span class="dot {connDot[$appState?.expressvpn?.connection] ?? ''}"></span>
          {$t('nav.dashboard')}
        </span>
      </a>

      <div class="side-section">{$t('nav.settings')}</div>
      <a class="side-link" class:active={$route === '/settings/expressvpn'} href="#/settings/expressvpn">{$t('nav.expressvpn')}</a>
      <a class="side-link" class:active={$route === '/settings/uplink'} href="#/settings/uplink">{$t('nav.uplink')}</a>
      <a class="side-link" class:active={$route === '/settings/proxy'} href="#/settings/proxy">{$t('nav.proxy')}</a>
      <a class="side-link" class:active={$route === '/settings/container'} href="#/settings/container">{$t('nav.container')}</a>
      <a class="side-link" class:active={$route === '/settings/access'} href="#/settings/access">{$t('nav.access')}</a>

      <div class="side-section">{$t('nav.system')}</div>
      <a class="side-link" class:active={$route === '/diagnostics'} href="#/diagnostics">{$t('nav.diagnostics')}</a>

      <div style="flex:1"></div>
      <button class="btn-secondary" style="margin:8px 10px" onclick={signOut}>{$t('nav.signout')}</button>
    </nav>

    <main class="content">
      {#if !$agentOnline}
        <div class="banner warn" style="margin-bottom:16px">{$t('agent.offline')}</div>
      {/if}
      <Page />
    </main>
  </div>
{/if}
