<script>
  import { onMount } from 'svelte'
  import { api } from '../../lib/api.js'
  import { t, lang, setLang } from '../../lib/i18n.js'
  import { notificationsEnabled, setNotifications } from '../../lib/notify.js'

  let current = $state('')
  let next = $state('')
  let pwFlash = $state('')
  let pwError = $state('')

  let token = $state('')
  let tokenVisible = $state(false)
  let notifyOn = $state(notificationsEnabled())

  async function changePassword(e) {
    e.preventDefault()
    pwError = ''
    try {
      await api.changePassword(current, next)
      current = ''
      next = ''
      pwFlash = 'ok'
      setTimeout(() => (pwFlash = ''), 2500)
    } catch (err) {
      pwError = err.status === 401 ? $t('login.wrong') : err.body?.message || err.message
    }
  }

  async function logoutAll() {
    try { await api.logoutAll() } catch {}
    location.reload()
  }

  async function showToken() {
    if (tokenVisible) { tokenVisible = false; return }
    const r = await api.token()
    token = r.token
    tokenVisible = true
  }

  async function rotate() {
    const r = await api.rotateToken()
    token = r.token
    tokenVisible = true
  }

  async function toggleNotify() {
    notifyOn = await setNotifications(!notifyOn)
  }
</script>

<div class="stack" style="max-width:640px">
  <h2>{$t('access.title')}</h2>

  <div class="card">
    <div class="label">{$t('access.password')}</div>
    <div class="hint" style="margin-bottom:10px">{$t('access.password.hint')}</div>
    <form onsubmit={changePassword} class="row" style="flex-wrap:wrap">
      <input type="password" style="flex:1;min-width:140px" placeholder={$t('access.current')} bind:value={current} />
      <input type="password" style="flex:1;min-width:140px" placeholder={$t('access.new')} bind:value={next} />
      <button class="btn-primary" disabled={!current || next.length < 8}>{$t('access.change')}</button>
    </form>
    {#if pwFlash === 'ok'}<div class="applied" style="margin-top:6px">✓ {$t('access.changed')}</div>{/if}
    {#if pwError}<div class="field-error" style="margin-top:6px">{pwError}</div>{/if}

    <div class="settings-row" style="margin-top:10px;border-bottom:none;border-top:1px solid var(--border)">
      <div>
        <div class="label">{$t('access.logoutall')}</div>
        <div class="hint">{$t('access.logoutall.hint')}</div>
      </div>
      <button class="btn-danger" onclick={logoutAll}>{$t('access.logoutall')}</button>
    </div>
  </div>

  <div class="card">
    <div class="settings-row" style="border-bottom:none">
      <div>
        <div class="label">{$t('access.token')}</div>
        <div class="hint">{$t('access.token.hint')}</div>
      </div>
      <div class="row">
        <button class="btn-secondary" onclick={showToken}>
          {tokenVisible ? $t('access.token.hide') : $t('access.token.show')}
        </button>
        <button class="btn-secondary" onclick={rotate}>{$t('access.token.rotate')}</button>
      </div>
    </div>
    {#if tokenVisible}
      <div class="tile mono" style="margin-top:8px;word-break:break-all">{token}</div>
    {/if}
  </div>

  <div class="card">
    <div class="settings-row">
      <div>
        <div class="label">{$t('access.lang')}</div>
        <div class="hint">{$t('access.lang.hint')}</div>
      </div>
      <div class="segment">
        <button class="{$lang === 'en' ? 'active' : ''}" onclick={() => setLang('en')}>English</button>
        <button class="{$lang === 'ru' ? 'active' : ''}" onclick={() => setLang('ru')}>Русский</button>
      </div>
    </div>
    <div class="settings-row">
      <div>
        <div class="label">{$t('access.notify')}</div>
        <div class="hint">{$t('access.notify.hint')}</div>
      </div>
      <button class="toggle {notifyOn ? 'on' : ''}" aria-label="notifications" onclick={toggleNotify}></button>
    </div>
  </div>
</div>
