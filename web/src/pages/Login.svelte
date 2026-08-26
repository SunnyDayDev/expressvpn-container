<script>
  import { api } from '../lib/api.js'
  import { t } from '../lib/i18n.js'

  let { done } = $props()
  let password = $state('')
  let error = $state('')
  let busy = $state(false)

  async function submit(e) {
    e.preventDefault()
    error = ''
    busy = true
    try {
      await api.login(password)
      done()
    } catch (err) {
      if (err.status === 429) error = $t('login.throttled')
      else if (err.status === 401) error = $t('login.wrong')
      else error = err.message
    } finally {
      busy = false
    }
  }
</script>

<div class="center-page">
  <div class="center-card">
    <div class="row" style="margin-bottom:18px">
      <div class="brand-mark">D</div>
      <div>
        <div style="font-weight:650">Detour</div>
        <div class="small muted">ExpressVPN sidecar</div>
      </div>
    </div>
    <h1>{$t('login.title')}</h1>
    <p class="muted" style="margin:4px 0 18px">{$t('login.hint')}</p>
    <form onsubmit={submit} class="stack" style="gap:12px">
      <input type="password" placeholder={$t('login.password')} bind:value={password} autofocus />
      {#if error}<div class="field-error">{error}</div>{/if}
      <button class="btn-primary" disabled={busy || !password}>{$t('login.signin')}</button>
    </form>
    <p class="small muted" style="margin-top:20px">
      {$t('login.forgot')}<br />
      <code class="mono">docker compose exec detour detour-agent reset-password</code>
    </p>
  </div>
</div>
