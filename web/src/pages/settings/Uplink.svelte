<script>
  import { onMount } from 'svelte'
  import { api } from '../../lib/api.js'
  import { appState } from '../../lib/store.js'
  import { t } from '../../lib/i18n.js'

  let cfg = $state(null)
  let flash = $state({})
  let testBusy = $state(false)
  let saveBusy = $state(false)

  onMount(async () => { cfg = await api.config() })

  let uplink = $derived($appState?.uplink ?? {})

  function flashOK(key) {
    flash = { ...flash, [key]: 'ok' }
    setTimeout(() => { flash = { ...flash, [key]: undefined } }, 2500)
  }

  async function setMode(mode) {
    cfg.uplink.mode = mode
    // socks5 без настроенных host/port применить нельзя — сначала параметры
    // и Save; во всех остальных случаях клик применяется сразу.
    if (mode === 'socks5' && !(cfg.uplink.socks5.host && cfg.uplink.socks5.port)) return
    try {
      await api.patchConfig({ uplink: { mode } })
      flashOK('mode')
    } catch (err) {
      flash = { ...flash, mode: err.body?.message || err.message }
    }
  }

  async function saveSocks(e) {
    e.preventDefault()
    saveBusy = true
    flash = { ...flash, mode: undefined }
    try {
      await api.patchConfig({
        uplink: {
          mode: 'socks5',
          socks5: {
            host: cfg.uplink.socks5.host,
            port: Number(cfg.uplink.socks5.port),
            username: cfg.uplink.socks5.username,
            password: cfg.uplink.socks5.password,
            udp: cfg.uplink.socks5.udp,
          },
        },
      })
      flashOK('mode')
    } catch (err) {
      flash = { ...flash, mode: err.body?.message || err.message }
    } finally {
      saveBusy = false
    }
  }

  async function testUplink() {
    testBusy = true
    try { await api.runAction('probe-uplink') } catch {} finally { testBusy = false }
  }

  let udpLabel = $derived(
    uplink.udpSupported === 'true' ? $t('uplink.udp.supported')
    : uplink.udpSupported === 'false' ? $t('uplink.udp.unsupported')
    : $t('uplink.udp.unknown'))
</script>

<div class="stack" style="max-width:640px">
  <h2>{$t('uplink.title')}</h2>

  <div class="card">
    <div class="settings-row" style="border-bottom:none">
      <div>
        <div class="label">{$t('uplink.mode')}</div>
        <div class="hint">{$t('uplink.mode.hint')}</div>
      </div>
      <div class="row">
        {#if flash.mode === 'ok'}<span class="applied">✓ {$t('common.applied')}</span>{/if}
        <div class="segment">
          <button class="{cfg?.uplink.mode === 'host' ? 'active' : ''}" onclick={() => setMode('host')}>host</button>
          <button class="{cfg?.uplink.mode === 'socks5' ? 'active' : ''}" onclick={() => setMode('socks5')}>socks5</button>
        </div>
      </div>
    </div>
    {#if flash.mode && flash.mode !== 'ok'}<div class="field-error">{flash.mode}</div>{/if}

    {#if cfg?.uplink.mode === 'socks5'}
      <form onsubmit={saveSocks} class="stack" style="gap:10px;margin-top:10px">
        <div class="grid-2">
          <div>
            <div class="small muted" style="margin-bottom:4px">{$t('uplink.host.label')}</div>
            <input type="text" class="mono" bind:value={cfg.uplink.socks5.host} placeholder="host.docker.internal" />
          </div>
          <div>
            <div class="small muted" style="margin-bottom:4px">{$t('uplink.port')}</div>
            <input type="number" class="mono" bind:value={cfg.uplink.socks5.port} min="1" max="65535" />
          </div>
        </div>
        <div class="grid-2">
          <div>
            <div class="small muted" style="margin-bottom:4px">{$t('uplink.username')}</div>
            <input type="text" bind:value={cfg.uplink.socks5.username} />
          </div>
          <div>
            <div class="small muted" style="margin-bottom:4px">{$t('uplink.password')}</div>
            <input type="password" bind:value={cfg.uplink.socks5.password} />
          </div>
        </div>
        <div class="row spread">
          <div>
            <div class="small muted" style="margin-bottom:4px">{$t('uplink.udp')}</div>
            <div class="segment">
              {#each ['auto', 'on', 'off'] as u}
                <button type="button" class="{cfg.uplink.socks5.udp === u ? 'active' : ''}"
                  onclick={() => (cfg.uplink.socks5.udp = u)}>{u}</button>
              {/each}
            </div>
          </div>
          <button class="btn-primary" style="align-self:flex-end" disabled={saveBusy}>
            {saveBusy ? '…' : $t('common.save')}
          </button>
        </div>
        <div class="small muted">{$t('uplink.udp.hint')}</div>
      </form>
    {/if}
  </div>

  <div class="card">
    <div class="row spread">
      <div>
        <div class="label">{$t('uplink.status')}</div>
        <div class="row" style="gap:8px;margin-top:6px">
          <span class="dot {uplink.status === 'up' ? 'ok' : uplink.status === 'down' ? 'err' : 'warn'}"></span>
          <span>{uplink.mode} · {uplink.status}</span>
          {#if uplink.endpoint}<span class="muted mono small">{uplink.endpoint}</span>{/if}
        </div>
        <div class="small muted" style="margin-top:4px">{udpLabel}{uplink.reason ? ' · ' + uplink.reason : ''}</div>
      </div>
      <button class="btn-secondary" disabled={testBusy || uplink.mode !== 'socks5'} onclick={testUplink}>
        {testBusy ? $t('uplink.testing') : $t('uplink.test')}
      </button>
    </div>
  </div>
</div>
