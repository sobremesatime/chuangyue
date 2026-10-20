<script setup lang="ts">
// 账号设置页面：顶栏头像下拉里「账号设置」的去处，只承载账号级信息。
//
// 用户名可改，点击弹出 ProfileFieldDialog（与个人中心同一个组件），保存走 PATCH /api/v1/users/me，
// 成功后 store 用后端返回的资料覆盖本地 profile，行内值与顶栏头像立即刷新。
// 密码行显示「已设置」，点击弹出 ChangePasswordDialog（当前密码 / 新密码 / 确认新密码），
// 走 PUT /api/v1/users/me/password 完成修改。
// 邮箱 / 手机号当前版本后端还没有修改接口，点击只给一句「后续版本开放」的提示，不放假的表单。

import { computed, onBeforeUnmount, ref } from 'vue'

import { toErrorMessage } from '@/api/request'
import AppIcon from '@/components/AppIcon.vue'
import ChangePasswordDialog from '@/components/ChangePasswordDialog.vue'
import ProfileFieldDialog from '@/components/ProfileFieldDialog.vue'
import { useUserStore } from '@/stores/user'

/** 账号信息行 */
interface AccountField {
  key: 'username' | 'email' | 'phone' | 'password'
  icon: 'user' | 'mail' | 'phone' | 'key'
  label: string
  value: string
}

const userStore = useUserStore()

/** 已开放编辑的行；其余点击只给提示 */
const EDITABLE_KEYS: AccountField['key'][] = ['username', 'password']

/** 用户名字符上限，与后端 utils.IsValidUsername 及契约 pattern 保持一致 */
const USERNAME_MAX_LENGTH = 24

const usernameDialogOpen = ref(false)
const usernameSaving = ref(false)
const usernameError = ref('')

const passwordDialogOpen = ref(false)
const passwordSaving = ref(false)
const passwordError = ref('')

/** 卡片下方的一次性提示条，3 秒后自动消失 */
const notice = ref('')
let noticeTimer: ReturnType<typeof setTimeout> | null = null

const roleLabel = computed(() => (userStore.isAdmin ? '管理员' : '普通用户'))
const currentUsername = computed(() => userStore.profile?.username ?? '')

const fields = computed<AccountField[]>(() => [
  {
    key: 'username',
    icon: 'user',
    label: '用户名',
    value: currentUsername.value ? `@${currentUsername.value}` : '未设置',
  },
  { key: 'email', icon: 'mail', label: '邮箱', value: userStore.profile?.email || '未绑定' },
  { key: 'phone', icon: 'phone', label: '手机号', value: '未绑定' },
  { key: 'password', icon: 'key', label: '密码', value: '已设置' },
])

function showNotice(message: string): void {
  notice.value = message
  if (noticeTimer) {
    clearTimeout(noticeTimer)
  }
  noticeTimer = setTimeout(() => {
    notice.value = ''
    noticeTimer = null
  }, 3000)
}

function openField(field: AccountField): void {
  if (!EDITABLE_KEYS.includes(field.key)) {
    showNotice('当前版本暂不支持修改，后续版本开放')
    return
  }
  if (field.key === 'password') {
    passwordError.value = ''
    passwordDialogOpen.value = true
    return
  }
  usernameError.value = ''
  usernameDialogOpen.value = true
}

function closeUsernameDialog(): void {
  usernameDialogOpen.value = false
  usernameError.value = ''
}

/**
 * 保存用户名。
 *
 * 先按后端口径归一化（去首尾空白 + 转小写），与原值相同就直接关闭弹框不打扰后端；
 * 其余校验（长度、字符集、唯一性）全部以后端为准，失败原因内联回弹框。
 */
async function submitUsername(value: string): Promise<void> {
  const next = value.trim().toLowerCase()
  if (next === currentUsername.value) {
    closeUsernameDialog()
    return
  }

  usernameError.value = ''
  notice.value = ''
  usernameSaving.value = true
  try {
    await userStore.updateProfile({ username: next })
    usernameDialogOpen.value = false
    showNotice('用户名已更新，下次请用新用户名或邮箱登录')
  } catch (e) {
    usernameError.value = toErrorMessage(e)
  } finally {
    usernameSaving.value = false
  }
}

function closePasswordDialog(): void {
  passwordDialogOpen.value = false
  passwordError.value = ''
}

/** 保存新密码：成功后关闭弹框并提示；当前登录态不受影响，退出后再用新密码登录 */
async function submitPassword(payload: {
  oldPassword: string
  newPassword: string
}): Promise<void> {
  passwordError.value = ''
  notice.value = ''
  passwordSaving.value = true
  try {
    // 契约字段是下划线风格，在这里做一次边界转换，弹框内部只用 camelCase
    await userStore.changePassword({
      old_password: payload.oldPassword,
      new_password: payload.newPassword,
    })
    passwordDialogOpen.value = false
    showNotice('密码已更新')
  } catch (e) {
    passwordError.value = toErrorMessage(e)
  } finally {
    passwordSaving.value = false
  }
}

onBeforeUnmount(() => {
  if (noticeTimer) {
    clearTimeout(noticeTimer)
  }
})
</script>

<template>
  <div class="settings cy-container">
    <h1 class="cy-title settings__title">账号设置</h1>

    <section class="settings__section">
      <h2 class="settings__section-title">账号信息</h2>

      <div class="cy-card settings__card">
        <ul class="settings__list">
          <li v-for="field in fields" :key="field.key">
            <button class="settings__row" type="button" @click="openField(field)">
              <AppIcon class="settings__row-icon" :name="field.icon" :size="18" />
              <span class="settings__row-label">{{ field.label }}</span>
              <span class="settings__row-value cy-clamp-1">{{ field.value }}</span>
              <AppIcon class="settings__row-arrow" name="chevron-right" :size="18" />
            </button>
          </li>
        </ul>
      </div>

      <p v-if="notice" class="cy-alert cy-alert--info settings__notice">{{ notice }}</p>
    </section>

    <section class="settings__section">
      <h2 class="settings__section-title">账号角色</h2>
      <div class="cy-card settings__card settings__role">
        <span class="cy-label">当前角色</span>
        <span class="settings__role-value">{{ roleLabel }}</span>
      </div>
    </section>

    <ProfileFieldDialog
      :open="usernameDialogOpen"
      title="修改用户名"
      label="用户名"
      type="text"
      :value="currentUsername"
      placeholder="请输入用户名"
      hint="3-24 位，以字母开头，仅可使用小写字母、数字和下划线；改名后旧用户名立即失效，需用新用户名或邮箱登录。"
      :maxlength="USERNAME_MAX_LENGTH"
      :saving="usernameSaving"
      :error="usernameError"
      @close="closeUsernameDialog"
      @submit="submitUsername"
    />

    <ChangePasswordDialog
      :open="passwordDialogOpen"
      :saving="passwordSaving"
      :error="passwordError"
      @close="closePasswordDialog"
      @submit="submitPassword"
    />
  </div>
</template>

<style scoped>
/* 内容收成一列并居中：cy-container 的 1800px 上限对设置页太宽，行内箭头会离标签很远 */
.settings {
  max-width: 800px;
  padding-bottom: var(--cy-space-7);
}

.settings__title {
  padding: var(--cy-space-5) 0 var(--cy-space-4);
}

.settings__section {
  margin-bottom: var(--cy-space-5);
}

.settings__section-title {
  font-size: 15px;
  line-height: 22px;
  font-weight: 600;
}

.settings__card {
  margin-top: var(--cy-space-3);
  padding: 0 var(--cy-space-4);
}

/* 行之间用发丝线分隔，首行不加 */
.settings__list li + li {
  border-top: 1px solid var(--cy-border);
}

/* 整行即按钮：可编辑的行打开弹框，只读的行给出「后续版本开放」的提示 */
.settings__row {
  display: flex;
  align-items: center;
  gap: var(--cy-space-3);
  width: 100%;
  padding: var(--cy-space-4) 0;
  text-align: left;
}

.settings__row-icon {
  color: var(--cy-text-muted);
}

.settings__row-label {
  flex: none;
  width: 72px;
  color: var(--cy-text);
  font-size: 14px;
  line-height: 20px;
  font-weight: 600;
}

.settings__row-value {
  flex: 1;
  min-width: 0;
  color: var(--cy-text-muted);
  font-size: 14px;
  line-height: 20px;
}

.settings__row-arrow {
  color: var(--cy-text-muted);
}

/* 悬停时整行文字变紫，作为可点击的反馈 */
.settings__row:hover .settings__row-label,
.settings__row:hover .settings__row-arrow {
  color: var(--cy-accent);
}

.settings__notice {
  margin-top: var(--cy-space-3);
}

.settings__role {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--cy-space-4);
}

.settings__role-value {
  font-size: 14px;
  line-height: 20px;
}
</style>
