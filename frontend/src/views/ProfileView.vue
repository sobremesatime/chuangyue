<script setup lang="ts">
// 个人中心页面：顶栏头像下拉里「个人中心」的去处，只承载个人资料。
//
// 两张卡片：上面是身份（头像 + 个人签名），下面是「个人信息」，
// 点击头像在它正下方吸附出 AvatarUploadDialog（上传图片 + 尺寸提示），选图后由本页负责上传；
// 「个人信息」一行一个字段，点任意一行弹出同一个 ProfileFieldDialog 就地编辑，
// 保存后由 store 用后端返回的资料覆盖本地 profile，页面与顶栏同时刷新。
//
// 账号级信息（用户名 / 邮箱 / 手机号 / 密码 / 账号角色）在「账号设置」页（/settings），
// 退出登录在顶栏头像下拉里，都不在这里重复出现。

import { computed, ref } from 'vue'

import { toErrorMessage } from '@/api/request'
import AppIcon from '@/components/AppIcon.vue'
import AvatarUploadDialog from '@/components/AvatarUploadDialog.vue'
import ProfileFieldDialog from '@/components/ProfileFieldDialog.vue'
import UserAvatar from '@/components/UserAvatar.vue'
import { useUserStore } from '@/stores/user'
import type { UpdateProfileRequest } from '@/types/api'

/** 个人信息里可编辑的字段 */
type ProfileFieldKey = 'nickname' | 'signature' | 'gender' | 'birthday' | 'school' | 'location'

interface ProfileField {
  key: ProfileFieldKey
  icon: 'user' | 'pencil' | 'gender' | 'calendar' | 'school' | 'location'
  label: string
  type: 'text' | 'textarea' | 'date' | 'radio'
  placeholder?: string
  hint?: string
  maxlength?: number
  options?: { label: string; value: string }[]
}

/** 性别候选项：值与后端 model.Gender* 常量一一对应 */
const GENDER_OPTIONS = [
  { label: '男', value: '1' },
  { label: '女', value: '2' },
  { label: '保密', value: '3' },
]

/** 性别展示文案，下标与后端取值对应（0 未设置） */
const GENDER_LABELS = ['未设置', '男', '女', '保密']

/** 字段清单：模板按它渲染行，弹框按它渲染内芯 */
const FIELDS: ProfileField[] = [
  {
    key: 'nickname',
    icon: 'user',
    label: '昵称',
    type: 'text',
    placeholder: '请输入昵称',
    hint: '不超过 32 个字符',
    maxlength: 32,
  },
  {
    key: 'signature',
    icon: 'pencil',
    label: '个人签名',
    type: 'textarea',
    placeholder: '说点什么，让别人认识你',
    hint: '不超过 140 个字符',
    maxlength: 140,
  },
  { key: 'gender', icon: 'gender', label: '性别', type: 'radio', options: GENDER_OPTIONS },
  {
    key: 'birthday',
    icon: 'calendar',
    label: '生日',
    type: 'date',
    hint: '用于展示年龄，不会精确到日对外',
  },
  {
    key: 'school',
    icon: 'school',
    label: '学校',
    type: 'text',
    placeholder: '请输入学校名称',
    maxlength: 64,
  },
  {
    key: 'location',
    icon: 'location',
    label: '现居地',
    type: 'text',
    placeholder: '如：江西 南昌',
    maxlength: 64,
  },
]

const userStore = useUserStore()

const uploadDialogOpen = ref(false)
const uploading = ref(false)
const uploadError = ref('')
const notice = ref('')

// 弹框状态：editingKey 为 null 表示弹框关闭
const editingKey = ref<ProfileFieldKey | null>(null)
const dialogValue = ref('')
const dialogSaving = ref(false)
const dialogError = ref('')

const displayName = computed(() => userStore.profile?.nickname || userStore.profile?.username || '')
const signature = computed(() => userStore.profile?.signature ?? '')

const editingField = computed(() => FIELDS.find((item) => item.key === editingKey.value) ?? null)
const dialogTitle = computed(() => (editingField.value ? `修改${editingField.value.label}` : ''))
const emptyValue = '未填写'

/** 生日展示：2026 年 4 月 16 日（26 岁）；未填写时回落到「未填写」 */
function formatBirthday(birthday: string | null | undefined, age: number): string {
  if (!birthday) {
    return emptyValue
  }
  const [year, month, day] = birthday.split('-')
  if (!year || !month || !day) {
    return emptyValue
  }
  const ageText = age > 0 ? `（${age} 岁）` : ''
  return `${year} 年 ${Number(month)} 月 ${Number(day)} 日${ageText}`
}

/** 行上展示的当前值 */
function displayValue(field: ProfileField): string {
  const profile = userStore.profile
  if (!profile) {
    return emptyValue
  }
  switch (field.key) {
    case 'nickname':
      return profile.nickname || emptyValue
    case 'signature':
      return profile.signature || emptyValue
    case 'gender':
      return GENDER_LABELS[profile.gender] ?? GENDER_LABELS[0]
    case 'birthday':
      return formatBirthday(profile.birthday, profile.age)
    case 'school':
      return profile.school || emptyValue
    case 'location':
      return profile.location || emptyValue
    default:
      return emptyValue
  }
}

/** 打开某一行：把当前值写进弹框草稿 */
function openField(field: ProfileField): void {
  const profile = userStore.profile
  if (!profile) {
    return
  }
  dialogError.value = ''
  switch (field.key) {
    case 'gender':
      dialogValue.value = String(profile.gender)
      break
    case 'birthday':
      dialogValue.value = profile.birthday ?? ''
      break
    case 'nickname':
      dialogValue.value = profile.nickname
      break
    case 'signature':
      dialogValue.value = profile.signature
      break
    case 'school':
      dialogValue.value = profile.school
      break
    case 'location':
      dialogValue.value = profile.location
      break
  }
  editingKey.value = field.key
}

function closeDialog(): void {
  editingKey.value = null
  dialogError.value = ''
}

/** 提交弹框：留空即清空，昵称与性别不允许留空 */
async function submitField(value: string): Promise<void> {
  const field = editingField.value
  if (!field) {
    return
  }
  const trimmed = value.trim()

  dialogError.value = ''

  if (field.key === 'nickname' && trimmed === '') {
    dialogError.value = '昵称不能为空'
    return
  }
  if (field.type === 'radio' && trimmed === '') {
    dialogError.value = `请选择${field.label}`
    return
  }

  const payload: UpdateProfileRequest = {}
  switch (field.key) {
    case 'nickname':
      payload.nickname = trimmed
      break
    case 'signature':
      payload.signature = trimmed
      break
    case 'gender':
      payload.gender = Number(trimmed) as UpdateProfileRequest['gender']
      break
    case 'birthday':
      payload.birthday = trimmed
      break
    case 'school':
      payload.school = trimmed
      break
    case 'location':
      payload.location = trimmed
      break
  }

  dialogSaving.value = true
  try {
    await userStore.updateProfile(payload)
    editingKey.value = null
    notice.value = '资料已更新'
  } catch (e) {
    dialogError.value = toErrorMessage(e)
  } finally {
    dialogSaving.value = false
  }
}

function openUploadDialog(): void {
  notice.value = ''
  uploadError.value = ''
  uploadDialogOpen.value = true
}

function closeUploadDialog(): void {
  uploadDialogOpen.value = false
  uploadError.value = ''
}

/** 上传头像：弹框已在前端校验过格式与大小，这里只负责发请求与结果提示 */
async function submitAvatar(file: File): Promise<void> {
  uploadError.value = ''
  notice.value = ''
  uploading.value = true
  try {
    await userStore.updateAvatar(file)
    uploadDialogOpen.value = false
    notice.value = '头像已更新'
  } catch (e) {
    uploadError.value = toErrorMessage(e)
  } finally {
    uploading.value = false
  }
}

</script>

<template>
  <div class="profile cy-container">
    <h1 class="cy-title profile__title">个人中心</h1>

    <section class="cy-card profile__card">
      <div class="profile__head">
        <div class="profile__avatar-wrap">
          <button
            class="profile__avatar"
            type="button"
            aria-label="更换头像"
            aria-haspopup="dialog"
            :aria-expanded="uploadDialogOpen"
            @click="openUploadDialog"
          >
            <UserAvatar :src="userStore.profile?.avatar" :size="88" :alt="displayName" />
            <span class="profile__avatar-mask">
              <AppIcon name="upload" :size="20" />
              <span>更换头像</span>
            </span>
          </button>

          <AvatarUploadDialog
            :open="uploadDialogOpen"
            :uploading="uploading"
            :error="uploadError"
            @close="closeUploadDialog"
            @submit="submitAvatar"
          />
        </div>

        <div v-if="signature" class="profile__id">
          <p class="profile__signature cy-clamp-1">{{ signature }}</p>
        </div>
      </div>

      <p v-if="notice" class="cy-alert cy-alert--success profile__alert">{{ notice }}</p>
    </section>

    <section class="cy-card profile__card profile__info">
      <h2 class="profile__section-title">个人信息</h2>

      <ul class="profile__list">
        <li v-for="field in FIELDS" :key="field.key">
          <button class="profile__row" type="button" @click="openField(field)">
            <AppIcon class="profile__row-icon" :name="field.icon" :size="18" />
            <span class="profile__row-label">{{ field.label }}</span>
            <span class="profile__row-value cy-clamp-1">{{ displayValue(field) }}</span>
            <AppIcon class="profile__row-arrow" name="chevron-right" :size="18" />
          </button>
        </li>
      </ul>
    </section>

    <ProfileFieldDialog
      :open="editingKey !== null"
      :title="dialogTitle"
      :label="editingField?.label ?? ''"
      :type="editingField?.type ?? 'text'"
      :value="dialogValue"
      :options="editingField?.options ?? []"
      :placeholder="editingField?.placeholder"
      :hint="editingField?.hint"
      :maxlength="editingField?.maxlength"
      :saving="dialogSaving"
      :error="dialogError"
      @close="closeDialog"
      @submit="submitField"
    />
  </div>
</template>

<style scoped>
/* 内容收成一列并居中：cy-container 的 1800px 上限对资料页太宽 */
.profile {
  max-width: 800px;
  padding-bottom: var(--cy-space-7);
}

.profile__title {
  padding: var(--cy-space-5) 0 var(--cy-space-4);
}

.profile__card {
  padding: var(--cy-space-5);
}

.profile__info {
  margin-top: var(--cy-space-4);
}

.profile__section-title {
  font-size: 15px;
  line-height: 22px;
  font-weight: 600;
}

.profile__list {
  margin-top: var(--cy-space-2);
}

/* 行之间用发丝线分隔，首行不加 */
.profile__list li + li {
  border-top: 1px solid var(--cy-border);
}

/* 整行即按钮：点击打开该字段的编辑弹框 */
.profile__row {
  display: flex;
  align-items: center;
  gap: var(--cy-space-3);
  width: 100%;
  padding: var(--cy-space-3) var(--cy-space-2);
  border-radius: var(--cy-radius-md);
  text-align: left;
  transition: background-color var(--cy-dur-fast) var(--cy-ease);
}

.profile__row:hover {
  background: var(--cy-surface-2);
}

.profile__row-icon {
  color: var(--cy-text-muted);
}

.profile__row-label {
  flex: none;
  width: 72px;
  color: var(--cy-text-muted);
  font-size: 13px;
  line-height: 20px;
}

.profile__row-value {
  flex: 1;
  min-width: 0;
  color: var(--cy-text);
  font-size: 14px;
  line-height: 20px;
}

.profile__row-arrow {
  color: var(--cy-text-muted);
}

/* 头像居中放在卡片顶部，签名排在它下方居中 */
.profile__head {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--cy-space-3);
  text-align: center;
}

/* 头像 + 更换头像弹层：弹层绝对定位挂在头像正下方 */
.profile__avatar-wrap {
  position: relative;
}

/* 头像即「更换头像」的入口：悬停 / 聚焦时浮出遮罩，避免额外多一个按钮 */
.profile__avatar {
  position: relative;
  flex-shrink: 0;
  border-radius: var(--cy-radius-full);
}

.profile__avatar-mask {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  border-radius: var(--cy-radius-full);
  background: var(--cy-overlay);
  color: var(--cy-media-badge-text);
  font-size: 12px;
  line-height: 18px;
  opacity: 0;
  transition: opacity var(--cy-dur-fast) var(--cy-ease);
}

.profile__avatar:hover .profile__avatar-mask,
.profile__avatar:focus-visible .profile__avatar-mask {
  opacity: 1;
}

.profile__id {
  min-width: 0;
}


.profile__signature {
  margin-top: var(--cy-space-1);
  color: var(--cy-text-secondary);
  font-size: 13px;
  line-height: 20px;
}

.profile__alert {
  margin-top: var(--cy-space-3);
}
</style>
