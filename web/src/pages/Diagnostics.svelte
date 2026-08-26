<script>
  import { onMount, onDestroy } from 'svelte'
  import { api } from '../lib/api.js'
  import { appState } from '../lib/store.js'
  import { t } from '../lib/i18n.js'

  let component = $state('')
  let lines = $state([])
  let checkBusy = $state(false)
  let es = null
  let logBox = $state(null)

  let sc = $derived($appState?.selfcheck)

  async function loadLogs() {
    const params = { limit: 300 }
    if (component) params.component = component
    const r = await api.logs(params)
    lines = r.entries ?? []
    scrollDown()
  }

  function connectStream() {
    es?.close()
    const q = component ? `?component=${component}` : ''
    es = new EventSource('/v1/logs/stream' + q)
    es.addEventListener('log', (ev) => {
      const e = JSON.parse(ev.data)
      lines = [...lines.slice(-499), e]
      scrollDown()
    })
  }

  function scrollDown() {
    queueMicrotask(() => { if (logBox) logBox.scrollTop = logBox.scrollHeight })
  }

  function setComponent(c) {
    component = c
    loadLogs().then(connectStream)
  }

  onMount(() => { loadLogs().then(connectStream) })
  onDestroy(() => es?.close())

  async function runSelfcheck() {
    checkBusy = true
    try { await api.runAction('selfcheck') } catch {} finally { checkBusy = false }
  }

  const components = ['', 'agent', 'expressvpn', 'uplink', 'proxy', 'killswitch']

  function ts(iso) {
    return new Date(iso).toLocaleTimeString('en-GB')
  }
</script>

<div class="stack">
  <h2>{$t('diag.title')}</h2>

  <div class="row" style="align-items:stretch;gap:14px;flex-wrap:wrap">
    <div class="card" style="flex:1;min-width:240px">
      <div class="row spread">
        <h3 style="font-size:14px">{$t('dash.selfcheck')}</h3>
        <button class="btn-secondary" disabled={checkBusy} onclick={runSelfcheck}>
          {checkBusy ? '…' : $t('dash.selfcheck.run')}
        </button>
      </div>
      {#if sc}
        <div class="stack" style="gap:8px;margin-top:10px">
          <div class="row">
            <span class="chip {sc.verdict === 'ok' ? 'ok' : sc.verdict === 'fail' ? 'err' : 'warn'}">{sc.verdict}</span>
            <span class="small muted">{new Date(sc.at).toLocaleString()}</span>
          </div>
          <div class="grid-2">
            <div class="tile">
              <div class="small muted">IP via proxy</div>
              <div class="mono">{sc.proxyIP || '—'} {sc.proxyCountry || ''}</div>
            </div>
            <div class="tile">
              <div class="small muted">IP via uplink</div>
              <div class="mono">{sc.uplinkIP || '—'}</div>
            </div>
          </div>
          {#if sc.dns?.length}
            <div class="tile"><div class="small muted">DNS</div><div class="mono">{sc.dns.join(', ')}</div></div>
          {/if}
          {#if sc.reasons?.length}
            <div class="banner warn small">{sc.reasons.join(', ')}</div>
          {/if}
        </div>
      {:else}
        <p class="muted small" style="margin-top:8px">{$t('dash.selfcheck.never')}</p>
      {/if}
    </div>

    <div class="card" style="flex:1;min-width:240px">
      <div class="row spread" style="gap:16px">
        <div>
          <div class="label">{$t('diag.download')}</div>
          <div class="hint">{$t('diag.archive.hint')}</div>
        </div>
        <a class="btn-secondary" href="/v1/diagnostics/archive" download>{$t('diag.download.short')}</a>
      </div>
    </div>
  </div>

  <div class="card">
    <div class="row spread" style="margin-bottom:10px;flex-wrap:wrap">
      <h3 style="font-size:14px">{$t('diag.logs')}</h3>
      <div class="segment">
        {#each components as c}
          <button class="{component === c ? 'active' : ''}" onclick={() => setComponent(c)}>
            {c === '' ? $t('diag.all') : c}
          </button>
        {/each}
      </div>
    </div>
    <div bind:this={logBox} style="max-height:420px;overflow-y:auto;background:var(--bg-inner);border-radius:10px;padding:12px">
      {#each lines as l}
        <div class="log-line">
          <span class="muted">{ts(l.time)}</span>
          <span class="log-{l.component}">[{l.component}]</span>
          {l.message}
        </div>
      {:else}
        <p class="muted small">—</p>
      {/each}
    </div>
  </div>
</div>
