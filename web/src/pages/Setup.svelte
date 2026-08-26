<script>
  import { api } from '../lib/api.js'
  import { t } from '../lib/i18n.js'
  import { appState, refreshState, startEvents } from '../lib/store.js'

  let { done } = $props()

  // Шаги онбординга (артборд 10): 1 пароль → 2 вход ExpressVPN →
  // 3 первое подключение → 4 подсказка про прокси.
  let step = $state(1)
  let busy = $state(false)
  let error = $state('')

  let password = $state('')
  let password2 = $state('')
  let code = $state('')

  async function createPassword(e) {
    e.preventDefault()
    error = ''
    if (password !== password2) {
      error = $t('setup.mismatch')
      return
    }
    busy = true
    try {
      await api.setup(password)
      await refreshState()
      startEvents()
      step = 2
    } catch (err) {
      error = err.message
    } finally {
      busy = false
    }
  }

  async function signIn(e) {
    e.preventDefault()
    error = ''
    busy = true
    try {
      await api.runAction('login', { activationCode: code })
      code = ''
      step = 3
    } catch (err) {
      // Код не отображаем повторно (спека) — поле уже очищается при успехе;
      // при ошибке оставляем ввод, показываем причину.
      error = err.body?.error === 'invalid_activation_code' ? 'Invalid activation code' : err.message
    } finally {
      busy = false
    }
  }

  async function firstConnect() {
    error = ''
    busy = true
    try {
      await api.runAction('connect', {})
      step = 4
    } catch (err) {
      error = err.message
    } finally {
      busy = false
    }
  }

  let proxyAddr = $derived(
    `socks5h://${location.hostname}:${$appState?.container?.publishedSocksPort ?? 1080}`)
</script>

<div class="center-page">
  <div class="center-card">
    <div class="row" style="margin-bottom:18px">
      <div class="brand-mark">D</div>
      <div>
        <div style="font-weight:650">{$t('setup.welcome')}</div>
        <div class="small muted">{$t('setup.subtitle')}</div>
      </div>
    </div>

    <div class="row small muted" style="gap:6px;margin-bottom:16px">
      {#each [1, 2, 3, 4] as n}
        <span class="chip {step === n ? 'ok' : ''}">{n}</span>
      {/each}
    </div>

    {#if step === 1}
      <h1>{$t('setup.password')}</h1>
      <p class="muted" style="margin:4px 0 16px">{$t('setup.create')}</p>
      <form onsubmit={createPassword} class="stack" style="gap:12px">
        <input type="password" placeholder={$t('setup.password')} bind:value={password} />
        <input type="password" placeholder={$t('setup.password2')} bind:value={password2} />
        {#if error}<div class="field-error">{error}</div>{/if}
        <button class="btn-primary" disabled={busy || password.length < 8}>{$t('setup.continue')}</button>
      </form>
    {:else if step === 2}
      <h1>{$t('setup.evpn')}</h1>
      <p class="muted" style="margin:4px 0 16px">{$t('setup.evpn.hint')}</p>
      <form onsubmit={signIn} class="stack" style="gap:12px">
        <input type="password" placeholder={$t('setup.code')} bind:value={code} class="mono" />
        {#if error}<div class="field-error">{error}</div>{/if}
        <button class="btn-primary" disabled={busy || !code}>
          {busy ? '…' : $t('setup.signin')}
        </button>
      </form>
    {:else if step === 3}
      <h1>{$t('setup.connect')}</h1>
      <p class="muted" style="margin:4px 0 16px">{$t('setup.connect.hint')}</p>
      {#if error}<div class="field-error" style="margin-bottom:10px">{error}</div>{/if}
      <div class="stack" style="gap:10px">
        <button class="btn-primary" disabled={busy} onclick={firstConnect}>
          {busy ? '…' : $t('setup.connect.smart')}
        </button>
        <button class="btn-secondary" onclick={() => (step = 4)}>{$t('setup.skip')}</button>
      </div>
    {:else}
      <h1>{$t('setup.done')}</h1>
      <p class="muted" style="margin:4px 0 16px">{$t('setup.done.hint')}</p>
      <div class="tile mono" style="margin-bottom:16px">{proxyAddr}</div>
      <button class="btn-primary" onclick={done}>{$t('setup.open')}</button>
    {/if}
  </div>
</div>
