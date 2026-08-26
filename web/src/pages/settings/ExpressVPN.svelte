<script>
  import { onMount } from 'svelte'
  import { api } from '../../lib/api.js'
  import { appState } from '../../lib/store.js'
  import { t } from '../../lib/i18n.js'

  let cfg = $state(null)
  let protocols = $state(['auto'])
  let code = $state('')
  let busy = $state(false)
  let flash = $state({}) // key → 'ok' | сообщение об ошибке

  onMount(async () => {
    cfg = await api.config()
    try {
      const v = await api.version()
      protocols = v.protocols ?? protocols
    } catch {}
  })

  let loggedIn = $derived($appState?.expressvpn?.auth === 'logged_in')

  function flashOK(key) {
    flash = { ...flash, [key]: 'ok' }
    setTimeout(() => { flash = { ...flash, [key]: undefined } }, 2500)
  }

  async function patch(key, patchBody) {
    try {
      await api.patchConfig(patchBody)
      flashOK(key)
    } catch (err) {
      flash = { ...flash, [key]: err.body?.message || err.message }
    }
  }

  async function signIn(e) {
    e.preventDefault()
    busy = true
    flash = { ...flash, account: undefined }
    try {
      await api.runAction('login', { activationCode: code })
      code = ''
      flashOK('account')
    } catch (err) {
      flash = { ...flash, account: err.body?.error === 'invalid_activation_code' ? 'Invalid activation code' : err.message }
    } finally {
      busy = false
    }
  }

  async function signOut() {
    busy = true
    try {
      await api.runAction('logout')
    } catch (err) {
      flash = { ...flash, account: err.message }
    } finally {
      busy = false
    }
  }

  const protections = ['ads', 'trackers', 'malicious', 'adult']
</script>

<div class="stack" style="max-width:640px">
  <h2>{$t('evpn.title')}</h2>

  <div class="card">
    <h3 style="font-size:14px;margin-bottom:8px">{$t('evpn.account')}</h3>
    <div class="row spread">
      <span class="chip {loggedIn ? 'ok' : ''}">{loggedIn ? $t('evpn.loggedin') : $t('evpn.loggedout')}</span>
      {#if loggedIn}
        <button class="btn-danger" disabled={busy} onclick={signOut}>{$t('evpn.logout')}</button>
      {/if}
    </div>
    {#if !loggedIn}
      <form onsubmit={signIn} class="row" style="margin-top:12px">
        <input type="password" class="mono" placeholder={$t('setup.code')} bind:value={code} />
        <button class="btn-primary" disabled={busy || !code}>{busy ? '…' : $t('setup.signin')}</button>
      </form>
    {/if}
    {#if flash.account && flash.account !== 'ok'}<div class="field-error" style="margin-top:6px">{flash.account}</div>{/if}
  </div>

  {#if cfg}
    <div class="card">
      <div class="settings-row">
        <div>
          <div class="label">{$t('evpn.protocol')}</div>
          <div class="hint">{$t('evpn.protocol.hint')}</div>
        </div>
        <div class="row">
          {#if flash.protocol === 'ok'}<span class="applied">✓ {$t('common.applied')}</span>{/if}
          <select style="width:170px" bind:value={cfg.expressvpn.protocol}
            onchange={() => patch('protocol', { expressvpn: { protocol: cfg.expressvpn.protocol } })}>
            {#each protocols as p}<option value={p}>{p}</option>{/each}
          </select>
        </div>
      </div>
      {#if flash.protocol && flash.protocol !== 'ok'}<div class="field-error">{flash.protocol}</div>{/if}

      <div class="settings-row">
        <div>
          <div class="label">{$t('evpn.autoconnect')}</div>
          <div class="hint">{$t('evpn.autoconnect.hint')}</div>
        </div>
        <div class="row">
          {#if flash.autoconnect === 'ok'}<span class="applied">✓</span>{/if}
          <button class="toggle {cfg.expressvpn.autoconnect ? 'on' : ''}" aria-label="autoconnect"
            onclick={() => { cfg.expressvpn.autoconnect = !cfg.expressvpn.autoconnect;
              patch('autoconnect', { expressvpn: { autoconnect: cfg.expressvpn.autoconnect } }) }}></button>
        </div>
      </div>
    </div>

    <div class="card">
      <h3 style="font-size:14px;margin-bottom:4px">{$t('evpn.protections')}</h3>
      {#each protections as p}
        <div class="settings-row">
          <div class="label">{$t('evpn.' + p)}</div>
          <div class="row">
            {#if flash[p] === 'ok'}<span class="applied">✓</span>{/if}
            <button class="toggle {cfg.expressvpn.protections[p] ? 'on' : ''}" aria-label={p}
              onclick={() => { cfg.expressvpn.protections[p] = !cfg.expressvpn.protections[p];
                patch(p, { expressvpn: { protections: { [p]: cfg.expressvpn.protections[p] } } }) }}></button>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>
