<script>
  import { onMount } from 'svelte'
  import { api } from '../../lib/api.js'
  import { authDisabled } from '../../lib/store.js'
  import { t, lang, setLang } from '../../lib/i18n.js'
  import { notificationsEnabled, setNotifications } from '../../lib/notify.js'

  let current = $state('')
  let next = $state('')
  let next2 = $state('')
  let pwFlash = $state('')
  let pwError = $state('')

  let offConfirm = $state(false)
  let offPw = $state('')
  let offError = $state('')

  // Состояние защиты могло измениться из другой вкладки — освежаем при открытии.
  onMount(async () => {
    try {
      const st = await api.authStatus()
      authDisabled.set(!!st.authDisabled)
    } catch {}
  })

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

  // Выключение защиты (артборд 5b): подтверждение текущим паролем.
  async function turnOff(e) {
    e.preventDefault()
    offError = ''
    try {
      await api.disableAuth(offPw)
      offPw = ''
      offConfirm = false
      authDisabled.set(true)
    } catch (err) {
      offError =
        err.status === 401 ? $t('login.wrong')
        : err.status === 429 ? $t('login.throttled')
        : err.body?.message || err.message
    }
  }

  // Включение защиты обратно (артборд 5c): setup выдаёт сессию сразу.
  async function setPassword(e) {
    e.preventDefault()
    pwError = ''
    if (next !== next2) {
      pwError = $t('setup.mismatch')
      return
    }
    try {
      await api.setup(next)
      next = ''
      next2 = ''
      authDisabled.set(false)
      pwFlash = 'set'
      setTimeout(() => (pwFlash = ''), 2500)
    } catch (err) {
      pwError = err.body?.message || err.message
    }
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

  {#if $authDisabled}
    <div class="banner warn"><b>{$t('access.off.banner')}</b>&nbsp;{$t('access.off.banner.hint')}</div>

    <div class="card">
      <div class="label">{$t('access.password')}</div>
      <div class="hint" style="margin-bottom:10px">{$t('access.setpw.hint')}</div>
      <form onsubmit={setPassword} class="row" style="flex-wrap:wrap">
        <input type="password" style="flex:1;min-width:140px" placeholder={$t('access.new')} bind:value={next} />
        <input type="password" style="flex:1;min-width:140px" placeholder={$t('setup.password2')} bind:value={next2} />
        <button class="btn-primary" disabled={next.length < 8 || !next2}>{$t('access.setpw')}</button>
      </form>
      {#if pwError}<div class="field-error" style="margin-top:6px">{pwError}</div>{/if}
    </div>
  {:else}
    <div class="card">
      <div class="label">{$t('access.password')}</div>
      <div class="hint" style="margin-bottom:10px">{$t('access.password.hint')}</div>
      <form onsubmit={changePassword} class="row" style="flex-wrap:wrap">
        <input type="password" style="flex:1;min-width:140px" placeholder={$t('access.current')} bind:value={current} />
        <input type="password" style="flex:1;min-width:140px" placeholder={$t('access.new')} bind:value={next} />
        <button class="btn-primary" disabled={!current || next.length < 8}>{$t('access.change')}</button>
      </form>
      {#if pwFlash === 'ok'}<div class="applied" style="margin-top:6px">✓ {$t('access.changed')}</div>{/if}
      {#if pwFlash === 'set'}<div class="applied" style="margin-top:6px">✓ {$t('access.setpw.done')}</div>{/if}
      {#if pwError}<div class="field-error" style="margin-top:6px">{pwError}</div>{/if}

      <div class="settings-row" style="margin-top:10px;border-bottom:none;border-top:1px solid var(--border)">
        <div>
          <div class="label">{$t('access.logoutall')}</div>
          <div class="hint">{$t('access.logoutall.hint')}</div>
        </div>
        <button class="btn-danger" onclick={logoutAll}>{$t('access.logoutall')}</button>
      </div>

      <div class="settings-row" style="border-bottom:none;border-top:1px solid var(--border)">
        <div>
          <div class="label">{$t('access.off')}</div>
          <div class="hint">{$t('access.off.hint')}</div>
        </div>
        {#if !offConfirm}
          <button class="btn-danger" onclick={() => (offConfirm = true)}>{$t('access.off.btn')}</button>
        {:else}
          <form onsubmit={turnOff} class="row">
            <input type="password" placeholder={$t('access.current')} bind:value={offPw} />
            <button class="btn-danger" disabled={!offPw}>{$t('access.off.btn')}</button>
          </form>
        {/if}
      </div>
      {#if offError}<div class="field-error" style="margin-top:6px">{offError}</div>{/if}
    </div>
  {/if}

  <div class="card">
    <div class="settings-row" style="border-bottom:none">
      <div>
        <div class="label">{$t('access.token')}</div>
        <div class="hint">{$authDisabled ? $t('access.token.hint.off') : $t('access.token.hint')}</div>
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
