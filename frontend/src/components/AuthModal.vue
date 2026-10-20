<script setup lang="ts">
// 登录 / 注册弹框（B 站式）：全站唯一的认证入口，由顶栏的「登录」按钮唤出。
//
// 内芯固定为浅色（.cy-theme-light）：无论用户当前是深色还是浅色模式，弹框都是
// 月白卡片 + 文字墨，与品牌标识「白底 · 首选」的规则一致，两种模式下观感统一。
//
// 可访问性：role="dialog" + aria-modal、Escape 关闭、Tab 焦点圈定在弹框内、
// 打开时锁住页面滚动、关闭后把焦点还给唤出它的按钮。

import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'

import { toErrorMessage } from '@/api/request'
import AppIcon from '@/components/AppIcon.vue'
import { useAuthModalStore, type AuthMode } from '@/stores/authModal'
import { useUserStore } from '@/stores/user'

const SLOGAN = '一扇窗，一轮月，看见更远的世界'

const modal = useAuthModalStore()
const userStore = useUserStore()

const mode = computed(() => modal.mode)

/** 弹框的无障碍标题：三种表单各一句 */
const dialogLabel = computed(() => {
  if (mode.value === 'register') {
    return '注册窗月'
  }
  if (mode.value === 'reset') {
    return '重置密码'
  }
  return '登录窗月'
})

const dialogRef = ref<HTMLElement | null>(null)
/** 当前渲染的那张表单的第一个输入框，打开弹框与切页签后都把焦点落在这里 */
const firstFieldRef = ref<HTMLInputElement | null>(null)

// 表单初始值集中定义：重置时直接展开复用，避免各处手写空对象漏字段
const emptyLoginForm = { account: '', password: '' }
const emptyLoginErrors = { account: '', password: '' }
const emptyRegisterForm = { email: '', code: '', password: '', confirmPassword: '', nickname: '' }
const emptyRegisterErrors = {
  email: '',
  code: '',
  password: '',
  confirmPassword: '',
  nickname: '',
}
const emptyResetForm = { email: '', code: '', password: '', confirmPassword: '' }
const emptyResetErrors = { email: '', code: '', password: '', confirmPassword: '' }

/** 邮箱粗校验：只挡明显无效的输入，最终以服务端为准 */
const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

// ---- 登录表单 ----
const loginForm = ref({ ...emptyLoginForm })
const loginErrors = ref({ ...emptyLoginErrors })
const showLoginPassword = ref(false)
const loginSubmitting = ref(false)
const loginError = ref('')

// ---- 注册表单 ----
const registerForm = ref({ ...emptyRegisterForm })
const registerErrors = ref({ ...emptyRegisterErrors })
const showRegisterPassword = ref(false)
const showConfirmPassword = ref(false)
const registerSubmitting = ref(false)
const registerError = ref('')

// ---- 重置密码表单（忘记密码）----
const resetForm = ref({ ...emptyResetForm })
const resetErrors = ref({ ...emptyResetErrors })
const showResetPassword = ref(false)
const showResetConfirm = ref(false)
const resetSubmitting = ref(false)
const resetError = ref('')

// ---- 邮箱验证码 ----
/** 发送按钮的忙碌态，避免连点发出多次请求 */
const codeSending = ref(false)
/** 重发倒计时剩余秒数，大于 0 时按钮禁用 */
const codeCountdown = ref(0)
let countdownTimer: ReturnType<typeof setInterval> | null = null

/** 获取验证码按钮文案 */
const codeButtonText = computed(() => {
  if (codeCountdown.value > 0) {
    return `${codeCountdown.value}s 后重发`
  }
  return codeSending.value ? '发送中…' : '获取验证码'
})

/** 注册成功或验证码已发送的提示，只留在弹框内，不跳页 */
const notice = ref('')

/** 停止重发倒计时并复位 */
function stopCountdown(): void {
  if (countdownTimer !== null) {
    clearInterval(countdownTimer)
    countdownTimer = null
  }
  codeCountdown.value = 0
}

/** 启动重发倒计时，秒数用服务端返回的 cooldown */
function startCountdown(seconds: number): void {
  stopCountdown()
  codeCountdown.value = seconds
  countdownTimer = setInterval(() => {
    codeCountdown.value -= 1
    if (codeCountdown.value <= 0) {
      stopCountdown()
    }
  }, 1000)
}

/** 邮箱被改动后，之前收到的验证码已不再对应当前邮箱，清空并放开重发 */
function onEmailChange(): void {
  registerForm.value.code = ''
  registerErrors.value.code = ''
  stopCountdown()
}

function resetForms(): void {
  loginForm.value = { ...emptyLoginForm }
  loginErrors.value = { ...emptyLoginErrors }
  showLoginPassword.value = false
  loginSubmitting.value = false
  loginError.value = ''

  registerForm.value = { ...emptyRegisterForm }
  registerErrors.value = { ...emptyRegisterErrors }
  showRegisterPassword.value = false
  showConfirmPassword.value = false
  registerSubmitting.value = false
  registerError.value = ''

  resetForm.value = { ...emptyResetForm }
  resetErrors.value = { ...emptyResetErrors }
  showResetPassword.value = false
  showResetConfirm.value = false
  resetSubmitting.value = false
  resetError.value = ''

  codeSending.value = false
  stopCountdown()

  notice.value = ''
}

async function switchMode(next: AuthMode): Promise<void> {
  if (mode.value === next) {
    return
  }
  modal.setMode(next)
  notice.value = ''
  loginError.value = ''
  registerError.value = ''
  resetError.value = ''
  loginErrors.value = { ...emptyLoginErrors }
  registerErrors.value = { ...emptyRegisterErrors }
  resetErrors.value = { ...emptyResetErrors }
  // 倒计时是按「上一张表单里那个邮箱」算的，换了表单就不再适用
  stopCountdown()
  await nextTick()
  firstFieldRef.value?.focus()
}

/** 从登录表单进入「忘记密码」；账号框里已经填了邮箱就带过去，省一次输入 */
async function openReset(): Promise<void> {
  const account = loginForm.value.account.trim()
  resetForm.value = { ...emptyResetForm, email: account.includes('@') ? account : '' }
  await switchMode('reset')
}

async function handleLogin(): Promise<void> {
  loginError.value = ''
  loginErrors.value = {
    account: loginForm.value.account.trim() ? '' : '请输入用户名或邮箱',
    password: loginForm.value.password ? '' : '请输入密码',
  }
  if (loginErrors.value.account || loginErrors.value.password) {
    return
  }

  loginSubmitting.value = true
  try {
    await userStore.login(loginForm.value.account.trim(), loginForm.value.password)
    // 登录成功后顶栏立刻变成头像 + 用户名，这就是最直接的反馈
    modal.close()
  } catch (e) {
    loginError.value = toErrorMessage(e)
  } finally {
    loginSubmitting.value = false
  }
}

/** 获取邮箱验证码：成功后按服务端返回的冷却秒数进入倒计时 */
async function handleSendCode(): Promise<void> {
  registerError.value = ''
  const email = registerForm.value.email.trim()
  if (!EMAIL_PATTERN.test(email)) {
    registerErrors.value = { ...emptyRegisterErrors, email: '请输入正确的邮箱地址' }
    return
  }

  registerErrors.value = { ...emptyRegisterErrors }
  codeSending.value = true
  try {
    const result = await userStore.sendEmailCode(email)
    startCountdown(result.cooldown)
    notice.value = '验证码已发送，请查收邮件'
  } catch (e) {
    registerError.value = toErrorMessage(e)
  } finally {
    codeSending.value = false
  }
}

async function handleRegister(): Promise<void> {
  registerError.value = ''
  notice.value = ''
  const email = registerForm.value.email.trim()

  // 校验规则与后端契约一致，先在前端拦一遍，减少无效请求；错误逐字段挂在输入框下方
  const errors = { ...emptyRegisterErrors }
  if (!EMAIL_PATTERN.test(email)) {
    errors.email = '请输入正确的邮箱地址'
  }
  if (!/^\d{6}$/.test(registerForm.value.code.trim())) {
    errors.code = '请输入 6 位数字验证码'
  }
  if (registerForm.value.nickname.length > 32) {
    errors.nickname = '昵称不能超过 32 个字符'
  }
  if (registerForm.value.password.length < 6 || registerForm.value.password.length > 64) {
    errors.password = '密码需为 6~64 个字符'
  }
  if (registerForm.value.password !== registerForm.value.confirmPassword) {
    errors.confirmPassword = '两次输入的密码不一致'
  }
  registerErrors.value = errors
  if (Object.values(errors).some((msg) => msg !== '')) {
    return
  }

  registerSubmitting.value = true
  try {
    await userStore.register({
      email,
      code: registerForm.value.code.trim(),
      password: registerForm.value.password,
      nickname: registerForm.value.nickname.trim() || undefined,
    })
    // 注册成功不跳页：清空注册表单，切回登录页签并带出刚注册的邮箱
    registerForm.value = { ...emptyRegisterForm }
    registerErrors.value = { ...emptyRegisterErrors }
    stopCountdown()
    modal.setMode('login')
    loginForm.value.account = email
    loginForm.value.password = ''
    notice.value = '注册成功，请登录'
    await nextTick()
    firstFieldRef.value?.focus()
  } catch (e) {
    registerError.value = toErrorMessage(e)
  } finally {
    registerSubmitting.value = false
  }
}

// ---- 忘记密码：发码与重置 ----

/** 重置表单里邮箱被改动后，之前收到的验证码已不再对应当前邮箱，清空并放开重发 */
function onResetEmailChange(): void {
  resetForm.value.code = ''
  resetErrors.value.code = ''
  stopCountdown()
}

async function handleSendResetCode(): Promise<void> {
  resetError.value = ''
  const email = resetForm.value.email.trim()
  if (!EMAIL_PATTERN.test(email)) {
    resetErrors.value = { ...emptyResetErrors, email: '请输入正确的邮箱地址' }
    return
  }

  resetErrors.value = { ...emptyResetErrors }
  codeSending.value = true
  try {
    const result = await userStore.sendResetCode(email)
    startCountdown(result.cooldown)
    // 服务端对未注册的邮箱也返回成功（防枚举），所以文案只能写「若已注册」，
    // 不能写成「验证码已发送，请查收」——那等于把邮箱是否存在告诉了请求方。
    notice.value = '若该邮箱已注册，重置验证码已发送'
  } catch (e) {
    resetError.value = toErrorMessage(e)
  } finally {
    codeSending.value = false
  }
}

async function handleReset(): Promise<void> {
  resetError.value = ''
  notice.value = ''
  const email = resetForm.value.email.trim()

  // 校验口径与后端一致，先在前端拦一遍
  const errors = { ...emptyResetErrors }
  if (!EMAIL_PATTERN.test(email)) {
    errors.email = '请输入正确的邮箱地址'
  }
  if (!/^\d{6}$/.test(resetForm.value.code.trim())) {
    errors.code = '请输入 6 位数字验证码'
  }
  if (resetForm.value.password.length < 6 || resetForm.value.password.length > 64) {
    errors.password = '密码需为 6~64 个字符'
  }
  if (resetForm.value.password !== resetForm.value.confirmPassword) {
    errors.confirmPassword = '两次输入的密码不一致'
  }
  resetErrors.value = errors
  if (Object.values(errors).some((msg) => msg !== '')) {
    return
  }

  resetSubmitting.value = true
  try {
    await userStore.resetPassword({
      email,
      code: resetForm.value.code.trim(),
      new_password: resetForm.value.password,
    })
    // 重置成功不自动登录：服务端刚把该账号所有旧会话吊销，先用新密码登录更符合预期，
    // 也顺带证明了新密码确实可用。
    resetForm.value = { ...emptyResetForm }
    resetErrors.value = { ...emptyResetErrors }
    stopCountdown()
    modal.setMode('login')
    loginForm.value.account = email
    loginForm.value.password = ''
    notice.value = '密码已重置，请用新密码登录'
    await nextTick()
    firstFieldRef.value?.focus()
  } catch (e) {
    resetError.value = toErrorMessage(e)
  } finally {
    resetSubmitting.value = false
  }
}

// ---- 弹框行为：滚动锁定、焦点圈定、Escape 关闭 ----

let lastActiveElement: HTMLElement | null = null
let scrollLocked = false

function lockScroll(): void {
  document.body.style.overflow = 'hidden'
  scrollLocked = true
}

function unlockScroll(): void {
  if (scrollLocked) {
    document.body.style.overflow = ''
    scrollLocked = false
  }
}

function focusableItems(): HTMLElement[] {
  if (!dialogRef.value) {
    return []
  }
  return Array.from(
    dialogRef.value.querySelectorAll<HTMLElement>(
      'a[href], button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])',
    ),
  ).filter((el) => el.offsetWidth > 0 || el.offsetHeight > 0)
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.stopPropagation()
    modal.close()
    return
  }
  if (event.key !== 'Tab') {
    return
  }
  // 焦点圈定在弹框内，Tab 到两端时回绕
  const items = focusableItems()
  if (items.length === 0) {
    return
  }
  const first = items[0]
  const last = items[items.length - 1]
  const active = document.activeElement as HTMLElement | null

  if (event.shiftKey && (active === first || !dialogRef.value?.contains(active))) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && active === last) {
    event.preventDefault()
    first.focus()
  }
}

watch(
  () => modal.open,
  async (isOpen) => {
    if (isOpen) {
      lastActiveElement = document.activeElement as HTMLElement | null
      resetForms()
      lockScroll()
      // 捕获阶段监听：先于顶栏的同名监听处理 Escape
      document.addEventListener('keydown', onKeydown, true)
      await nextTick()
      firstFieldRef.value?.focus()
      return
    }
    unlockScroll()
    document.removeEventListener('keydown', onKeydown, true)
    lastActiveElement?.focus?.()
    lastActiveElement = null
  },
)

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown, true)
  unlockScroll()
  stopCountdown()
})
</script>

<template>
  <Teleport to="body">
    <Transition name="auth-modal">
      <div v-if="modal.open" class="auth-modal" @click.self="modal.close()">
        <div
          ref="dialogRef"
          class="cy-theme-light auth-modal__dialog"
          role="dialog"
          aria-modal="true"
          :aria-label="dialogLabel"
        >
          <button
            class="auth-modal__close"
            type="button"
            aria-label="关闭"
            @click="modal.close()"
          >
            <AppIcon name="close" :size="20" />
          </button>

          <div class="auth-modal__brand">
            <img class="auth-modal__mark" src="/icon.svg" alt="" width="72" height="72" />
            <p class="auth-modal__name">窗月</p>
            <p class="auth-modal__slogan">{{ SLOGAN }}</p>
          </div>

          <div v-if="mode !== 'reset'" class="auth-modal__tabs" role="tablist">
            <button
              class="auth-modal__tab"
              :class="{ 'is-active': mode === 'login' }"
              type="button"
              role="tab"
              :aria-selected="mode === 'login'"
              @click="switchMode('login')"
            >
              登录
            </button>
            <button
              class="auth-modal__tab"
              :class="{ 'is-active': mode === 'register' }"
              type="button"
              role="tab"
              :aria-selected="mode === 'register'"
              @click="switchMode('register')"
            >
              注册
            </button>
          </div>
          <!-- 重置密码不是「登录 / 注册」之一，用同级标题占位，保持卡片节奏一致 -->
          <p v-else class="auth-modal__title">重置密码</p>

          <form v-if="mode === 'login'" class="auth-modal__form" @submit.prevent="handleLogin">
            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': loginErrors.account }"
                for="auth-login-account"
              >
                账号
              </label>
              <input
                id="auth-login-account"
                ref="firstFieldRef"
                v-model="loginForm.account"
                class="cy-input"
                :class="{ 'cy-input--invalid': loginErrors.account }"
                type="text"
                autocomplete="username"
                placeholder="用户名或邮箱"
              />
              <p v-if="loginErrors.account" class="cy-field__error">{{ loginErrors.account }}</p>
            </div>

            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': loginErrors.password }"
                for="auth-login-password"
              >
                密码
              </label>
              <div class="cy-input-wrap">
                <input
                  id="auth-login-password"
                  v-model="loginForm.password"
                  class="cy-input"
                  :class="{ 'cy-input--invalid': loginErrors.password }"
                  :type="showLoginPassword ? 'text' : 'password'"
                  autocomplete="current-password"
                  placeholder="请输入密码"
                />
                <button
                  class="cy-input-wrap__action"
                  type="button"
                  :aria-label="showLoginPassword ? '隐藏密码' : '显示密码'"
                  @click="showLoginPassword = !showLoginPassword"
                >
                  <AppIcon :name="showLoginPassword ? 'eye-off' : 'eye'" :size="18" />
                </button>
              </div>
              <p v-if="loginErrors.password" class="cy-field__error">{{ loginErrors.password }}</p>
            </div>

            <div class="auth-modal__aux">
              <button class="auth-modal__link" type="button" @click="openReset">忘记密码？</button>
            </div>

            <p v-if="notice" class="cy-alert cy-alert--success">{{ notice }}</p>
            <p v-if="loginError" class="cy-alert cy-alert--error">{{ loginError }}</p>

            <button
              class="cy-btn cy-btn--primary cy-btn--lg cy-btn--block"
              type="submit"
              :disabled="loginSubmitting"
            >
              {{ loginSubmitting ? '登录中…' : '登录' }}
            </button>
          </form>

          <form v-else-if="mode === 'register'" class="auth-modal__form" @submit.prevent="handleRegister">
            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': registerErrors.email }"
                for="auth-register-email"
              >
                邮箱
              </label>
              <input
                id="auth-register-email"
                ref="firstFieldRef"
                v-model="registerForm.email"
                class="cy-input"
                :class="{ 'cy-input--invalid': registerErrors.email }"
                type="email"
                autocomplete="email"
                placeholder="用于登录"
                @change="onEmailChange"
              />
              <p v-if="registerErrors.email" class="cy-field__error">
                {{ registerErrors.email }}
              </p>
              <p v-else class="cy-field__hint">用户名将由邮箱前缀自动生成</p>
            </div>

            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': registerErrors.code }"
                for="auth-register-code"
              >
                邮箱验证码
              </label>
              <div class="auth-modal__code">
                <input
                  id="auth-register-code"
                  v-model="registerForm.code"
                  class="cy-input"
                  :class="{ 'cy-input--invalid': registerErrors.code }"
                  type="text"
                  inputmode="numeric"
                  maxlength="6"
                  autocomplete="one-time-code"
                  placeholder="6 位数字"
                />
                <button
                  class="cy-btn cy-btn--ghost auth-modal__code-btn"
                  type="button"
                  :disabled="codeSending || codeCountdown > 0"
                  @click="handleSendCode"
                >
                  {{ codeButtonText }}
                </button>
              </div>
              <p v-if="registerErrors.code" class="cy-field__error">
                {{ registerErrors.code }}
              </p>
            </div>

            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': registerErrors.nickname }"
                for="auth-register-nickname"
              >
                昵称（选填）
              </label>
              <input
                id="auth-register-nickname"
                v-model="registerForm.nickname"
                class="cy-input"
                :class="{ 'cy-input--invalid': registerErrors.nickname }"
                type="text"
                placeholder="不填则与邮箱前缀相同"
              />
              <p v-if="registerErrors.nickname" class="cy-field__error">
                {{ registerErrors.nickname }}
              </p>
            </div>

            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': registerErrors.password }"
                for="auth-register-password"
              >
                密码
              </label>
              <div class="cy-input-wrap">
                <input
                  id="auth-register-password"
                  v-model="registerForm.password"
                  class="cy-input"
                  :class="{ 'cy-input--invalid': registerErrors.password }"
                  :type="showRegisterPassword ? 'text' : 'password'"
                  autocomplete="new-password"
                  placeholder="请输入密码"
                />
                <button
                  class="cy-input-wrap__action"
                  type="button"
                  :aria-label="showRegisterPassword ? '隐藏密码' : '显示密码'"
                  @click="showRegisterPassword = !showRegisterPassword"
                >
                  <AppIcon :name="showRegisterPassword ? 'eye-off' : 'eye'" :size="18" />
                </button>
              </div>
              <p v-if="registerErrors.password" class="cy-field__error">
                {{ registerErrors.password }}
              </p>
              <p v-else class="cy-field__hint">6~64 个字符</p>
            </div>

            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': registerErrors.confirmPassword }"
                for="auth-register-confirm"
              >
                确认密码
              </label>
              <div class="cy-input-wrap">
                <input
                  id="auth-register-confirm"
                  v-model="registerForm.confirmPassword"
                  class="cy-input"
                  :class="{ 'cy-input--invalid': registerErrors.confirmPassword }"
                  :type="showConfirmPassword ? 'text' : 'password'"
                  autocomplete="new-password"
                  placeholder="请再次输入密码"
                />
                <button
                  class="cy-input-wrap__action"
                  type="button"
                  :aria-label="showConfirmPassword ? '隐藏密码' : '显示密码'"
                  @click="showConfirmPassword = !showConfirmPassword"
                >
                  <AppIcon :name="showConfirmPassword ? 'eye-off' : 'eye'" :size="18" />
                </button>
              </div>
              <p v-if="registerErrors.confirmPassword" class="cy-field__error">
                {{ registerErrors.confirmPassword }}
              </p>
            </div>

            <p v-if="notice" class="cy-alert cy-alert--success">{{ notice }}</p>
            <p v-if="registerError" class="cy-alert cy-alert--error">{{ registerError }}</p>

            <button
              class="cy-btn cy-btn--primary cy-btn--lg cy-btn--block"
              type="submit"
              :disabled="registerSubmitting"
            >
              {{ registerSubmitting ? '注册中…' : '注册' }}
            </button>
          </form>

          <!--
            忘记密码：邮箱 → 一次性验证码 → 新密码。
            成功不自动登录（后端刚吊销了该账号的全部旧会话），而是带回登录表单。
          -->
          <form v-else class="auth-modal__form" @submit.prevent="handleReset">
            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': resetErrors.email }"
                for="auth-reset-email"
              >
                邮箱
              </label>
              <input
                id="auth-reset-email"
                ref="firstFieldRef"
                v-model="resetForm.email"
                class="cy-input"
                :class="{ 'cy-input--invalid': resetErrors.email }"
                type="email"
                autocomplete="email"
                placeholder="注册时使用的邮箱"
                @change="onResetEmailChange"
              />
              <p v-if="resetErrors.email" class="cy-field__error">{{ resetErrors.email }}</p>
            </div>

            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': resetErrors.code }"
                for="auth-reset-code"
              >
                重置验证码
              </label>
              <div class="auth-modal__code">
                <input
                  id="auth-reset-code"
                  v-model="resetForm.code"
                  class="cy-input"
                  :class="{ 'cy-input--invalid': resetErrors.code }"
                  type="text"
                  inputmode="numeric"
                  maxlength="6"
                  autocomplete="one-time-code"
                  placeholder="6 位数字"
                />
                <button
                  class="cy-btn cy-btn--ghost auth-modal__code-btn"
                  type="button"
                  :disabled="codeSending || codeCountdown > 0"
                  @click="handleSendResetCode"
                >
                  {{ codeButtonText }}
                </button>
              </div>
              <p v-if="resetErrors.code" class="cy-field__error">{{ resetErrors.code }}</p>
            </div>

            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': resetErrors.password }"
                for="auth-reset-password"
              >
                新密码
              </label>
              <div class="cy-input-wrap">
                <input
                  id="auth-reset-password"
                  v-model="resetForm.password"
                  class="cy-input"
                  :class="{ 'cy-input--invalid': resetErrors.password }"
                  :type="showResetPassword ? 'text' : 'password'"
                  autocomplete="new-password"
                  placeholder="请输入新密码"
                />
                <button
                  class="cy-input-wrap__action"
                  type="button"
                  :aria-label="showResetPassword ? '隐藏密码' : '显示密码'"
                  @click="showResetPassword = !showResetPassword"
                >
                  <AppIcon :name="showResetPassword ? 'eye-off' : 'eye'" :size="18" />
                </button>
              </div>
              <p v-if="resetErrors.password" class="cy-field__error">{{ resetErrors.password }}</p>
              <p v-else class="cy-field__hint">6~64 个字符</p>
            </div>

            <div class="cy-field">
              <label
                class="cy-label"
                :class="{ 'is-invalid': resetErrors.confirmPassword }"
                for="auth-reset-confirm"
              >
                确认新密码
              </label>
              <div class="cy-input-wrap">
                <input
                  id="auth-reset-confirm"
                  v-model="resetForm.confirmPassword"
                  class="cy-input"
                  :class="{ 'cy-input--invalid': resetErrors.confirmPassword }"
                  :type="showResetConfirm ? 'text' : 'password'"
                  autocomplete="new-password"
                  placeholder="请再次输入新密码"
                />
                <button
                  class="cy-input-wrap__action"
                  type="button"
                  :aria-label="showResetConfirm ? '隐藏密码' : '显示密码'"
                  @click="showResetConfirm = !showResetConfirm"
                >
                  <AppIcon :name="showResetConfirm ? 'eye-off' : 'eye'" :size="18" />
                </button>
              </div>
              <p v-if="resetErrors.confirmPassword" class="cy-field__error">
                {{ resetErrors.confirmPassword }}
              </p>
            </div>

            <p v-if="notice" class="cy-alert cy-alert--success">{{ notice }}</p>
            <p v-if="resetError" class="cy-alert cy-alert--error">{{ resetError }}</p>

            <button
              class="cy-btn cy-btn--primary cy-btn--lg cy-btn--block"
              type="submit"
              :disabled="resetSubmitting"
            >
              {{ resetSubmitting ? '重置中…' : '重置密码' }}
            </button>
            <p class="cy-field__hint">重置后，该账号在所有设备上的登录状态都会失效</p>

            <div class="auth-modal__aux auth-modal__aux--center">
              <button class="auth-modal__link" type="button" @click="switchMode('login')">
                返回登录
              </button>
            </div>
          </form>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
/* 遮罩铺满视口；用 flex + margin:auto 居中，弹框高于视口时可滚动而不裁掉顶部 */
.auth-modal {
  position: fixed;
  inset: 0;
  z-index: 200;
  display: flex;
  padding: var(--cy-space-5);
  background: var(--cy-overlay);
  overflow-y: auto;
}

.auth-modal__dialog {
  position: relative;
  margin: auto;
  width: 100%;
  max-width: 400px;
  padding: var(--cy-space-6) var(--cy-space-6) var(--cy-space-5);
  border: 1px solid var(--cy-border);
  border-radius: var(--cy-radius-lg);
  background: var(--cy-surface);
  box-shadow: var(--cy-shadow-pop);
}

.auth-modal__close {
  position: absolute;
  top: var(--cy-space-3);
  right: var(--cy-space-3);
  display: grid;
  place-items: center;
  width: 40px;
  height: 40px;
  border-radius: var(--cy-radius-full);
  color: var(--cy-text-muted);
  transition: background-color var(--cy-dur-fast) var(--cy-ease),
    color var(--cy-dur-fast) var(--cy-ease);
}

.auth-modal__close:hover {
  background: var(--cy-surface-2);
  color: var(--cy-text);
}

.auth-modal__brand {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--cy-space-2);
}

.auth-modal__mark {
  width: 72px;
  height: 72px;
  border-radius: var(--cy-radius-logo);
}

/* 品牌标用专用字体：圆润、手写感，落在文字墨上，由字面本身承担「年轻」的气质 */
.auth-modal__name {
  font-family: var(--cy-font-brand);
  font-size: 26px;
  line-height: 34px;
  letter-spacing: 2px;
  color: var(--cy-text);
}

.auth-modal__slogan {
  color: var(--cy-text-muted);
  font-size: 13px;
  line-height: 20px;
}

/* 页签：登录 / 注册共用同一个弹框，活动项用品牌紫下划线标记 */
.auth-modal__tabs {
  display: flex;
  gap: var(--cy-space-5);
  margin-top: var(--cy-space-5);
  border-bottom: 1px solid var(--cy-border);
}

.auth-modal__tab {
  position: relative;
  height: 40px;
  padding: 0 var(--cy-space-1);
  color: var(--cy-text-secondary);
  font-size: 15px;
  line-height: 20px;
  font-weight: 500;
  transition: color var(--cy-dur-fast) var(--cy-ease);
}

.auth-modal__tab:hover {
  color: var(--cy-text);
}

.auth-modal__tab.is-active {
  color: var(--cy-primary-strong);
  font-weight: 600;
}

.auth-modal__tab.is-active::after {
  content: '';
  position: absolute;
  right: 0;
  bottom: -1px;
  left: 0;
  height: 3px;
  border-radius: var(--cy-radius-full);
  background: var(--cy-primary);
}

.auth-modal__form {
  display: flex;
  flex-direction: column;
  gap: var(--cy-space-4);
  margin-top: var(--cy-space-5);
}

/* 验证码一行：输入框占满剩余宽度，右侧按钮不参与收缩 */
.auth-modal__code {
  display: flex;
  align-items: center;
  gap: var(--cy-space-2);
}

.auth-modal__code-btn {
  flex: none;
  white-space: nowrap;
}

/* 辅助行：忘记密码 / 返回登录这类次要动作，靠右或居中，不与主按钮争视觉重量 */
.auth-modal__aux {
  display: flex;
  justify-content: flex-end;
}

.auth-modal__aux--center {
  justify-content: center;
}

.auth-modal__link {
  color: var(--cy-primary-strong);
  font-size: 13px;
  line-height: 20px;
}

.auth-modal__link:hover {
  text-decoration: underline;
}

/* 重置密码态没有页签（登录 / 注册都不适用），用同高度的标题占位保持卡片节奏 */
.auth-modal__title {
  margin-top: var(--cy-space-5);
  padding-bottom: var(--cy-space-3);
  border-bottom: 1px solid var(--cy-border);
  color: var(--cy-text);
  font-size: 15px;
  line-height: 20px;
  font-weight: 600;
}

/* 浅色底上的紫色文字用深一档的紫，标签出错时切成危险色 */
.cy-label.is-invalid {
  color: var(--cy-danger);
}

/* 弹框淡入 + 轻微收放 */
.auth-modal-enter-active,
.auth-modal-leave-active {
  transition: opacity var(--cy-dur-base) var(--cy-ease);
}

.auth-modal-enter-active .auth-modal__dialog,
.auth-modal-leave-active .auth-modal__dialog {
  transition: transform var(--cy-dur-base) var(--cy-ease);
}

.auth-modal-enter-from,
.auth-modal-leave-to {
  opacity: 0;
}

.auth-modal-enter-from .auth-modal__dialog,
.auth-modal-leave-to .auth-modal__dialog {
  transform: scale(0.96);
}
</style>
