<script>
  import { onMount } from 'svelte'
  import { api } from '../../lib/api.js'
  import { appState } from '../../lib/store.js'
  import { t } from '../../lib/i18n.js'

  let cfg = $state(null)
  let authOn = $state(false)
  let username = $state('')
  let password = $state('')
  let flash = $state('')
  let error = $state('')

  onMount(async () => {
    cfg = await api.config()
    authOn = !!cfg.proxy.auth
    username = cfg.proxy.auth?.username ?? ''
    password = cfg.proxy.auth?.password ?? ''
  })

  let proxy = $derived($appState?.proxy ?? {})
  let published = $derived($appState?.container ?? {})

  async function toggleAuth() {
    error = ''
    if (authOn) {
      // Выключить.
      try {
        await api.patchConfig({ proxy: { auth: null } })
        authOn = false
        username = ''
        password = ''
        showApplied()
      } catch (err) {
        error = err.body?.message || err.message
      }
    } else {
      authOn = true // поля появляются; применение по Save
    }
  }

  async function saveAuth(e) {
    e.preventDefault()
    error = ''
    try {
      await api.patchConfig({ proxy: { auth: { username, password } } })
      showApplied()
    } catch (err) {
      error = err.body?.message || err.message
    }
  }

  function showApplied() {
    flash = 'ok'
    setTimeout(() => (flash = ''), 2500)
  }

  function fmtBytes(n) {
    if (!n) return '0 B'
    const u = ['B', 'KB', 'MB', 'GB', 'TB']
    let i = 0
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
    return n.toFixed(i ? 1 : 0) + ' ' + u[i]
  }
</script>

<div class="stack" style="max-width:640px">
  <h2>{$t('proxy.title')}</h2>

  <div class="card">
    <div class="settings-row" style="border-bottom:none">
      <div>
        <div class="label">{$t('proxy.auth')}</div>
        <div class="hint">{$t('proxy.auth.hint')}</div>
      </div>
      <div class="row">
        {#if flash === 'ok'}<span class="applied">✓ {$t('common.applied')}</span>{/if}
        <button class="toggle {authOn ? 'on' : ''}" aria-label="proxy auth" onclick={toggleAuth}></button>
      </div>
    </div>
    {#if authOn}
      <form onsubmit={saveAuth} class="row" style="margin-top:8px;flex-wrap:wrap">
        <input type="text" style="flex:1;min-width:140px" placeholder={$t('uplink.username')} bind:value={username} />
        <input type="password" style="flex:1;min-width:140px" placeholder={$t('uplink.password')} bind:value={password} />
        <button class="btn-primary" disabled={!username || !password}>{$t('common.save')}</button>
      </form>
    {/if}
    {#if error}<div class="field-error" style="margin-top:6px">{error}</div>{/if}
  </div>

  <div class="card">
    <h3 style="font-size:14px;margin-bottom:8px">{$t('proxy.stats')}</h3>
    <div class="grid-2">
      <div class="tile">
        <div class="small muted">{$t('proxy.active')}</div>
        <div style="font-weight:600">{proxy.activeConns ?? 0}</div>
      </div>
      <div class="tile">
        <div class="small muted">{$t('proxy.bytes')}</div>
        <div style="font-weight:600">↑ {fmtBytes(proxy.bytesIn)} · ↓ {fmtBytes(proxy.bytesOut)}</div>
      </div>
    </div>
  </div>

  <div class="card">
    <div class="settings-row" style="border-bottom:none">
      <div>
        <div class="label">{$t('proxy.port.host')} <span class="badge-env">edit in .env</span></div>
        <div class="hint">SOCKS_PORT → <code class="mono">docker compose up -d</code></div>
      </div>
      <span class="mono" style="font-size:15px">{published.publishedBindAddr ?? '127.0.0.1'}:{published.publishedSocksPort ?? 1080}</span>
    </div>
  </div>
</div>
