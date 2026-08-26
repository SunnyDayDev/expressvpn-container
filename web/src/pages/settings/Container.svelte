<script>
  import { onMount } from 'svelte'
  import { api } from '../../lib/api.js'
  import { appState } from '../../lib/store.js'
  import { t } from '../../lib/i18n.js'

  let version = $state(null)
  onMount(async () => { version = await api.version() })

  let c = $derived($appState?.container ?? {})

  // Read-only страница (артборд 7b): параметры уровня compose показываются
  // текстом с именем .env-переменной — UI не может их применить (UI honesty).
  let rows = $derived([
    { label: $t('cont.image'), value: `detour/expressvpn-sidecar:${version?.image ?? '…'}`, env: null },
    { label: 'ExpressVPN', value: version?.expressvpn ?? '…', env: 'EXPRESSVPN_VERSION' },
    { label: 'Uplink engine', value: version?.uplinkEngine ?? '…', env: 'SINGBOX_VERSION' },
    { label: 'Agent', value: version?.agent ?? '…', env: 'AGENT_VERSION' },
    { label: 'SOCKS5 → host', value: `${c.publishedBindAddr ?? '127.0.0.1'}:${c.publishedSocksPort ?? 1080}`, env: 'SOCKS_PORT' },
    { label: 'Web UI / API → host', value: `${c.publishedBindAddr ?? '127.0.0.1'}:${c.publishedHTTPPort ?? 48100}`, env: 'HTTP_PORT' },
    { label: 'Bind address', value: c.publishedBindAddr ?? '127.0.0.1', env: 'BIND_ADDR' },
    { label: $t('cont.volume'), value: 'detour-data → /data', env: null },
  ])

  let copied = $state(false)
  async function copyCmd() {
    try {
      await navigator.clipboard.writeText('docker compose up -d --build')
      copied = true
      setTimeout(() => (copied = false), 1500)
    } catch {}
  }
</script>

<div class="stack" style="max-width:640px">
  <h2>{$t('cont.title')}</h2>
  <div class="banner info">{$t('cont.hint')}</div>

  <div class="card">
    {#each rows as r}
      <div class="settings-row">
        <div>
          <span class="label">{r.label}</span>
          {#if r.env}<span class="badge-env" style="margin-left:8px">{r.env}</span>{/if}
        </div>
        <span class="mono">{r.value}</span>
      </div>
    {/each}
  </div>

  <div class="card">
    <div class="settings-row" style="border-bottom:none">
      <div>
        <div class="label">{$t('cont.apply')}</div>
        <div class="hint mono">docker compose up -d --build</div>
      </div>
      <button class="btn-secondary" onclick={copyCmd}>{copied ? $t('dash.copied') : $t('dash.copy')}</button>
    </div>
  </div>
</div>
