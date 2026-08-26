<script>
  import { api } from '../lib/api.js'
  import { appState } from '../lib/store.js'
  import { t } from '../lib/i18n.js'

  let busy = $state(false)
  let error = $state('')
  let copied = $state(false)
  let search = $state('')
  let locations = $state([])
  let checkBusy = $state(false)
  let uplinkBusy = $state(false)

  $effect(() => {
    if ($appState?.expressvpn?.auth === 'logged_in') loadLocations()
  })

  async function loadLocations() {
    try {
      const r = await api.locations()
      locations = r.locations ?? []
    } catch {}
  }

  let conn = $derived($appState?.expressvpn?.connection ?? 'disconnected')
  let loggedIn = $derived($appState?.expressvpn?.auth === 'logged_in')
  let proto = $derived($appState?.expressvpn?.protocol ?? {})
  let uplink = $derived($appState?.uplink ?? {})
  let proxy = $derived($appState?.proxy ?? {})
  let sc = $derived($appState?.selfcheck)

  let filtered = $derived(
    locations.filter(l =>
      !search || (l.name + l.id + l.country).toLowerCase().includes(search.toLowerCase())))

  let proxyAddr = $derived(
    `socks5h://${location.hostname}:${$appState?.container?.publishedSocksPort ?? 1080}`)

  // Имя текущей локации — как в списке (id → человекочитаемое имя).
  let currentLocName = $derived.by(() => {
    const id = $appState?.expressvpn?.location
    if (!id || id === 'smart') return $t('dash.smart')
    const l = locations.find((x) => x.id === id)
    return l?.name ?? id
  })

  let connLabel = $derived({
    connected: $t('dash.connected'), connecting: $t('dash.connecting'),
    reconnecting: $t('dash.reconnecting'), disconnected: $t('dash.disconnected'),
    error: $t('dash.error'),
  }[conn] ?? conn)

  async function toggleConnection() {
    error = ''
    busy = true
    try {
      if (conn === 'connected') await api.runAction('disconnect')
      else await api.runAction('connect', {})
    } catch (err) {
      error = err.message
    } finally {
      busy = false
    }
  }

  async function connectTo(id) {
    error = ''
    busy = true
    try {
      await api.runAction('connect', { location: id === 'smart' ? '' : id })
    } catch (err) {
      error = err.message
    } finally {
      busy = false
    }
  }

  async function switchUplink(mode) {
    if (mode === uplink.mode) return
    error = ''
    uplinkBusy = true
    try {
      await api.patchConfig({ uplink: { mode } })
    } catch (err) {
      error = err.body?.message || err.message
    } finally {
      uplinkBusy = false
    }
  }

  async function copyProxy() {
    try {
      await navigator.clipboard.writeText(proxyAddr)
      copied = true
      setTimeout(() => (copied = false), 1500)
    } catch {}
  }

  async function runSelfcheck() {
    checkBusy = true
    try { await api.runAction('selfcheck') } catch {} finally { checkBusy = false }
  }

  function fmtBytes(n) {
    if (!n) return '0 B'
    const u = ['B', 'KB', 'MB', 'GB', 'TB']
    let i = 0
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
    return n.toFixed(i ? 1 : 0) + ' ' + u[i]
  }

  function duration(fromISO) {
    if (!fromISO) return ''
    const s = Math.max(0, (Date.now() - new Date(fromISO).getTime()) / 1000) | 0
    const h = (s / 3600) | 0, m = ((s % 3600) / 60) | 0
    return h ? `${h}h ${m}m` : `${m}m`
  }
</script>

<div class="stack">
  {#if error}<div class="banner error">{error}</div>{/if}
  {#if $appState?.lastError && conn === 'error'}
    <div class="banner error">{$appState.lastError.code}: {$appState.lastError.message}</div>
  {/if}

  <div class="hero">
    <div class="row spread" style="flex-wrap:wrap;gap:14px">
      <div>
        <div class="row" style="gap:8px">
          <span class="dot {conn === 'connected' ? 'ok' : conn === 'error' ? 'err' : conn === 'disconnected' ? '' : 'warn'}"></span>
          <span class="muted small">{connLabel}</span>
        </div>
        <h1 style="margin-top:6px">
          {#if conn === 'connected'}
            {currentLocName}
          {:else if loggedIn}
            {$t('dash.disconnected')}
          {:else}
            {$t('dash.signin.first')}
          {/if}
        </h1>
        {#if conn === 'connected'}
          <div class="muted small" style="margin-top:4px">
            {$appState?.expressvpn?.publicIP || ''}
            {#if $appState?.expressvpn?.connectedAt}· {duration($appState.expressvpn.connectedAt)}{/if}
          </div>
        {/if}
      </div>
      {#if loggedIn}
        <button class="btn-primary" style="align-self:center" disabled={busy} onclick={toggleConnection}>
          {busy ? '…' : conn === 'connected' ? $t('dash.disconnect') : $t('dash.connect')}
        </button>
      {:else}
        <a class="btn-primary" style="align-self:center;display:inline-block" href="#/settings/expressvpn">{$t('nav.expressvpn')} →</a>
      {/if}
    </div>
  </div>

  <div class="row" style="align-items:flex-start;gap:16px;flex-wrap:wrap">
    <div class="card" style="flex:1.2;min-width:280px">
      <div class="row spread" style="margin-bottom:10px">
        <h3>{$t('dash.locations')}</h3>
      </div>
      <input type="text" placeholder={$t('dash.search')} bind:value={search} style="margin-bottom:10px" />
      <div style="max-height:380px;overflow-y:auto">
        {#each filtered as l (l.id)}
          <div class="row spread" style="padding:7px 4px;border-bottom:1px solid var(--border)">
            <div>
              <div>{l.smart && l.id === 'smart' ? $t('dash.smart') : l.name}</div>
              <div class="small muted mono">{l.id}</div>
            </div>
            <button class="btn-secondary" style="padding:5px 12px" disabled={busy}
              onclick={() => connectTo(l.id)}>{$t('dash.connect')}</button>
          </div>
        {:else}
          <p class="muted small">{loggedIn ? '—' : $t('dash.signin.first')}</p>
        {/each}
      </div>
    </div>

    <div class="stack" style="flex:1;min-width:280px">
      <div class="grid-2">
        <div class="tile">
          <div class="small muted">{$t('dash.protocol')}</div>
          <div style="font-weight:600">{proto.effective || proto.requested || 'auto'}</div>
          {#if proto.reason}<div class="small muted">{proto.reason}</div>{/if}
        </div>
        <div class="tile">
          <div class="small muted">{$t('dash.uplink')}</div>
          <div class="row" style="gap:8px;margin:3px 0">
            <div class="segment">
              <button class="{uplink.mode === 'host' ? 'active' : ''}" disabled={uplinkBusy}
                onclick={() => switchUplink('host')}>host</button>
              <button class="{uplink.mode === 'socks5' ? 'active' : ''}" disabled={uplinkBusy}
                onclick={() => switchUplink('socks5')}>socks5</button>
            </div>
            <span class="dot {uplink.status === 'up' ? 'ok' : uplink.status === 'down' ? 'err' : 'warn'}"></span>
          </div>
          <div class="small muted">{$t('dash.uplink.switch.hint')}</div>
        </div>
      </div>

      <div class="grid-2">
        <div class="tile">
          <div class="small muted">{$t('dash.traffic')}</div>
          <div style="font-weight:600">↓ {fmtBytes(proxy.bytesOut)} · ↑ {fmtBytes(proxy.bytesIn)}</div>
          <div class="small muted">{$t('proxy.active')}: {proxy.activeConns ?? 0}</div>
        </div>
        <div class="tile">
          <div class="small muted">Kill switch</div>
          <div style="font-weight:600">{$appState?.killswitch?.active ? $t('common.on') : $t('common.off')}</div>
          <div class="small muted">dropped: {$appState?.killswitch?.dropped ?? 0}</div>
        </div>
      </div>

      <div class="card">
        <div class="small muted">{$t('dash.proxyaddr')}</div>
        <div class="row spread" style="margin-top:6px">
          <span class="mono" style="font-size:14px">{proxyAddr}</span>
          <button class="btn-secondary" style="padding:5px 12px" onclick={copyProxy}>
            {copied ? $t('dash.copied') : $t('dash.copy')}
          </button>
        </div>
        <div class="small muted" style="margin-top:6px">{$t('dash.proxy.hint')}</div>
      </div>

      <div>
        <div class="row spread">
          <h3 style="font-size:14px">{$t('dash.selfcheck')}</h3>
          <button class="btn-secondary" style="padding:5px 12px" disabled={checkBusy} onclick={runSelfcheck}>
            {checkBusy ? '…' : $t('dash.selfcheck.run')}
          </button>
        </div>
        {#if sc}
          <div class="row" style="margin-top:8px;gap:8px;flex-wrap:wrap">
            <span class="chip {sc.verdict === 'ok' ? 'ok' : sc.verdict === 'fail' ? 'err' : 'warn'}">{sc.verdict}</span>
            {#if sc.proxyIP}<span class="small muted mono">proxy: {sc.proxyIP} {sc.proxyCountry}</span>{/if}
            {#if sc.uplinkIP}<span class="small muted mono">uplink: {sc.uplinkIP}</span>{/if}
          </div>
          {#if sc.reasons?.length}
            <div class="small muted" style="margin-top:4px">{sc.reasons.join(', ')}</div>
          {/if}
        {:else}
          <p class="small muted" style="margin-top:6px">{$t('dash.selfcheck.never')}</p>
        {/if}
      </div>
    </div>
  </div>
</div>
