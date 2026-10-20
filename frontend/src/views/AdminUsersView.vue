<script setup lang="ts">
// 管理后台 /admin/users：账号检索、封禁 / 解封、角色调整。
//
// 对应契约 GET /admin/users（分页 + 关键词 + 角色 / 状态过滤）与
// PATCH /admin/users/{id}（status / role 部分更新）。
//
// 两条刻意的交互：
//   - 自己那一行不给操作按钮：服务端会拒绝「改自己」，与其让人点了才报错，
//     不如直接显示「当前账号」；
//   - 改完之后如果该行不再符合当前筛选（比如在「正常」筛选下封禁了一个人），
//     就地移除并修正总数，避免出现「筛正常却显示已禁用」的错位（与视频管理同一处理）。

import { computed, onMounted, ref } from 'vue'

import { listAdminUsers, updateAdminUser } from '@/api/admin'
import { toErrorMessage } from '@/api/request'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import StatePanel from '@/components/StatePanel.vue'
import UserAvatar from '@/components/UserAvatar.vue'
import { useUserStore } from '@/stores/user'
import {
  ROLE_ADMIN,
  ROLE_USER,
  USER_STATUS_DISABLED,
  USER_STATUS_NORMAL,
  type AdminUpdateUserRequest,
  type User,
} from '@/types/api'

const PAGE_SIZE = 20

const userStore = useUserStore()

const users = ref<User[]>([])
const total = ref(0)
const page = ref(1)

/** 输入框里的关键词，回车 / 点「搜索」才生效（避免每敲一个字就发请求） */
const keywordInput = ref('')
const keyword = ref('')
/** 角色 / 状态筛选：undefined 表示不过滤（0 也是合法取值，不能当「全部」） */
const roleFilter = ref<number | undefined>(undefined)
const statusFilter = ref<number | undefined>(undefined)

const loading = ref(true)
const error = ref('')
/** 单行操作失败时的行内提示，不覆盖整页 */
const actionError = ref('')

/** 待确认的操作（封禁 / 解封 / 设为管理员 / 取消管理员） */
type ActionKind = 'ban' | 'unban' | 'promote' | 'demote'
interface PendingAction {
  kind: ActionKind
  user: User
}
const pending = ref<PendingAction | null>(null)
const actionSaving = ref(false)
const actionDialogError = ref('')

const dialogOpen = computed(() => pending.value !== null)

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / PAGE_SIZE)))

const dialogContent = computed(() => {
  const target = pending.value
  if (!target) {
    return { title: '', description: '', confirmLabel: '', danger: false }
  }
  const name = displayName(target.user)
  switch (target.kind) {
    case 'ban':
      return {
        title: '封禁这个账号？',
        description: `「${name}」将无法再登录；已登录的会话在下次刷新令牌时失效（访问令牌最长还能用 15 分钟）。`,
        confirmLabel: '封禁',
        danger: true,
      }
    case 'unban':
      return {
        title: '解除封禁？',
        description: `「${name}」将恢复正常登录。`,
        confirmLabel: '解封',
        danger: false,
      }
    case 'promote':
      return {
        title: '设为管理员？',
        description: `「${name}」将获得全部管理权限（内容治理、用户管理）。`,
        confirmLabel: '设为管理员',
        danger: false,
      }
    default:
      return {
        title: '取消管理员身份？',
        description: `「${name}」将降为普通用户，管理端入口随即关闭。`,
        confirmLabel: '取消管理员',
        danger: true,
      }
  }
})

function displayName(user: User): string {
  return user.nickname || user.username
}

/** 是否是当前登录的管理员自己 */
function isSelf(user: User): boolean {
  return userStore.profile?.id === user.id
}

function roleText(role: number): string {
  return role === ROLE_ADMIN ? '管理员' : '普通用户'
}

function statusText(status: number): string {
  return status === USER_STATUS_NORMAL ? '正常' : '已禁用'
}

/** 表格里的时间用绝对时间，相对时间在管理场景里不够精确 */
function formatDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return '—'
  }
  const pad = (n: number): string => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(
    date.getHours(),
  )}:${pad(date.getMinutes())}`
}

/** 该行是否仍符合当前筛选（改完之后用来决定「替换」还是「移除」） */
function matchesFilter(user: User): boolean {
  if (roleFilter.value !== undefined && user.role !== roleFilter.value) {
    return false
  }
  if (statusFilter.value !== undefined && user.status !== statusFilter.value) {
    return false
  }
  return true
}

async function load(targetPage: number): Promise<void> {
  loading.value = true
  error.value = ''
  actionError.value = ''

  try {
    const result = await listAdminUsers({
      page: targetPage,
      page_size: PAGE_SIZE,
      keyword: keyword.value || undefined,
      role: roleFilter.value,
      status: statusFilter.value,
    })
    users.value = result.list ?? []
    total.value = result.total ?? 0
    page.value = result.page ?? targetPage
  } catch (e) {
    error.value = toErrorMessage(e)
    users.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function submitSearch(): void {
  keyword.value = keywordInput.value.trim()
  void load(1)
}

function selectRole(value: number | undefined): void {
  if (value === roleFilter.value) {
    return
  }
  roleFilter.value = value
  void load(1)
}

function selectStatus(value: number | undefined): void {
  if (value === statusFilter.value) {
    return
  }
  statusFilter.value = value
  void load(1)
}

function ask(kind: ActionKind, user: User): void {
  if (isSelf(user)) {
    return
  }
  actionDialogError.value = ''
  pending.value = { kind, user }
}

function closeDialog(): void {
  if (actionSaving.value) {
    return
  }
  pending.value = null
  actionDialogError.value = ''
}

/** 确认执行：按操作类型只提交需要改的那一个字段 */
async function confirmAction(): Promise<void> {
  if (!pending.value || actionSaving.value) {
    return
  }
  const { kind, user } = pending.value
  // 显式标注类型：契约里 status / role 是字面量联合 0 | 1，
  // 不加标注时对象字面量会把 0 拓宽成 number，无法赋给该类型。
  const payload: AdminUpdateUserRequest =
    kind === 'ban'
      ? { status: USER_STATUS_DISABLED }
      : kind === 'unban'
        ? { status: USER_STATUS_NORMAL }
        : kind === 'promote'
          ? { role: ROLE_ADMIN }
          : { role: ROLE_USER }

  actionSaving.value = true
  actionDialogError.value = ''
  try {
    const updated = await updateAdminUser(user.id, payload)
    const index = users.value.findIndex((item) => item.id === updated.id)
    if (index >= 0) {
      if (matchesFilter(updated)) {
        users.value[index] = updated
      } else {
        users.value.splice(index, 1)
        total.value = Math.max(0, total.value - 1)
      }
    }
    pending.value = null

    // 该行被筛掉后本页可能空了：回退一页，避免停在空页上（与视频管理同一处理）
    if (users.value.length === 0 && page.value > 1) {
      await load(page.value - 1)
    }
  } catch (e) {
    // 失败时保留弹框，让管理员能看到原因（常见：改自己 / 用户已被删）
    actionDialogError.value = toErrorMessage(e)
  } finally {
    actionSaving.value = false
  }
}

onMounted(() => void load(1))
</script>

<template>
  <div class="users cy-container">
    <header class="users__head">
      <h1 class="cy-title">用户管理</h1>
      <p v-if="!loading && !error" class="cy-caption">
        共 <span class="cy-num">{{ total }}</span> 个账号
      </p>
    </header>

    <form class="users__search" role="search" @submit.prevent="submitSearch">
      <input
        v-model="keywordInput"
        class="cy-input users__search-input"
        type="search"
        placeholder="搜索用户名 / 邮箱 / 昵称"
        aria-label="搜索用户名、邮箱或昵称"
        @keydown.enter.prevent="submitSearch"
      />
      <button class="cy-btn cy-btn--primary cy-btn--sm" type="submit">搜索</button>
    </form>

    <div class="users__filters">
      <div class="users__filter-group" role="group" aria-label="角色筛选">
        <span class="users__filter-label cy-caption">角色</span>
        <button
          v-for="item in [
            { label: '全部', value: undefined },
            { label: '普通用户', value: ROLE_USER },
            { label: '管理员', value: ROLE_ADMIN },
          ]"
          :key="item.label"
          class="cy-btn cy-btn--secondary cy-btn--pill cy-btn--sm"
          :class="{ 'is-active': roleFilter === item.value }"
          type="button"
          :aria-pressed="roleFilter === item.value"
          @click="selectRole(item.value)"
        >
          {{ item.label }}
        </button>
      </div>

      <div class="users__filter-group" role="group" aria-label="状态筛选">
        <span class="users__filter-label cy-caption">状态</span>
        <button
          v-for="item in [
            { label: '全部', value: undefined },
            { label: '正常', value: USER_STATUS_NORMAL },
            { label: '已禁用', value: USER_STATUS_DISABLED },
          ]"
          :key="item.label"
          class="cy-btn cy-btn--secondary cy-btn--pill cy-btn--sm"
          :class="{ 'is-active': statusFilter === item.value }"
          type="button"
          :aria-pressed="statusFilter === item.value"
          @click="selectStatus(item.value)"
        >
          {{ item.label }}
        </button>
      </div>
    </div>

    <p v-if="actionError" class="cy-alert cy-alert--error users__alert">{{ actionError }}</p>

    <!-- 加载中：占位行与真实行同高，避免加载完成后表格高度突跳 -->
    <div v-if="loading" class="users__table" aria-hidden="true">
      <div v-for="index in 6" :key="index" class="users__row users__row--skeleton">
        <span class="cy-skeleton users__sk-line" />
        <span class="cy-skeleton users__sk-line users__sk-line--short" />
      </div>
    </div>

    <StatePanel
      v-else-if="error"
      variant="error"
      title="用户加载失败"
      :description="error"
      action-label="重新加载"
      @action="load(page)"
    />

    <StatePanel
      v-else-if="users.length === 0"
      title="没有匹配的账号"
      :description="
        keyword || roleFilter !== undefined || statusFilter !== undefined
          ? '换个关键词或筛选条件试试。'
          : '系统里还没有注册用户。'
      "
    />

    <template v-else>
      <div class="users__table">
        <div class="users__row users__row--head">
          <span>账号</span>
          <span>角色</span>
          <span>状态</span>
          <span class="users__cell--time">注册时间</span>
          <span class="users__cell--action">操作</span>
        </div>

        <div v-for="user in users" :key="user.id" class="users__row">
          <div class="users__main">
            <UserAvatar :src="user.avatar" :size="36" :alt="`${displayName(user)} 的头像`" />
            <div class="users__info">
              <p class="users__name">
                {{ displayName(user) }}
                <span v-if="isSelf(user)" class="users__self">当前账号</span>
              </p>
              <p class="users__meta cy-caption">
                @{{ user.username }}
                <span v-if="user.email" class="users__email">· {{ user.email }}</span>
              </p>
            </div>
          </div>

          <span
            class="users__tag"
            :class="user.role === ROLE_ADMIN ? 'is-admin' : 'is-user'"
          >
            {{ roleText(user.role) }}
          </span>

          <span
            class="users__tag"
            :class="user.status === USER_STATUS_NORMAL ? 'is-normal' : 'is-disabled'"
          >
            {{ statusText(user.status) }}
          </span>

          <span class="users__cell--time cy-caption cy-num">
            {{ formatDate(user.created_at) }}
          </span>

          <span class="users__cell--action users__actions">
            <template v-if="!isSelf(user)">
              <button
                class="cy-btn cy-btn--outline cy-btn--sm"
                :class="{ 'users__danger': user.status === USER_STATUS_NORMAL }"
                type="button"
                @click="
                  ask(user.status === USER_STATUS_NORMAL ? 'ban' : 'unban', user)
                "
              >
                {{ user.status === USER_STATUS_NORMAL ? '封禁' : '解封' }}
              </button>
              <button
                class="cy-btn cy-btn--outline cy-btn--sm"
                :class="{ 'users__danger': user.role === ROLE_ADMIN }"
                type="button"
                @click="ask(user.role === ROLE_ADMIN ? 'demote' : 'promote', user)"
              >
                {{ user.role === ROLE_ADMIN ? '取消管理员' : '设为管理员' }}
              </button>
            </template>
            <span v-else class="cy-caption">不能修改自己</span>
          </span>
        </div>
      </div>

      <div v-if="totalPages > 1" class="users__pager">
        <button
          class="cy-btn cy-btn--secondary cy-btn--sm cy-btn--pill"
          type="button"
          :disabled="page <= 1"
          @click="load(page - 1)"
        >
          上一页
        </button>
        <span class="cy-caption cy-num">第 {{ page }} / {{ totalPages }} 页</span>
        <button
          class="cy-btn cy-btn--secondary cy-btn--sm cy-btn--pill"
          type="button"
          :disabled="page >= totalPages"
          @click="load(page + 1)"
        >
          下一页
        </button>
      </div>
    </template>

    <ConfirmDialog
      :open="dialogOpen"
      :title="dialogContent.title"
      :description="dialogContent.description"
      :confirm-label="dialogContent.confirmLabel"
      :danger="dialogContent.danger"
      :busy="actionSaving"
      :error="actionDialogError"
      @close="closeDialog"
      @confirm="confirmAction"
    />
  </div>
</template>

<style scoped>
.users {
  padding-bottom: var(--cy-space-7);
}

.users__head {
  display: flex;
  align-items: baseline;
  gap: var(--cy-space-3);
  padding: var(--cy-space-5) 0 var(--cy-space-4);
}

/* 搜索行：输入框占满剩余宽度，按钮不参与收缩 */
.users__search {
  display: flex;
  align-items: center;
  gap: var(--cy-space-2);
  padding-bottom: var(--cy-space-3);
}

.users__search-input {
  flex: 1;
  min-width: 0;
  max-width: 420px;
}

.users__filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--cy-space-2) var(--cy-space-5);
  padding-bottom: var(--cy-space-4);
}

.users__filter-group {
  display: flex;
  align-items: center;
  gap: var(--cy-space-2);
}

.users__filter-label {
  flex-shrink: 0;
}

.users__alert {
  margin-bottom: var(--cy-space-4);
}

/* 表格：五列网格，表头与数据行共用同一套列宽 */
.users__table {
  border-top: 1px solid var(--cy-border);
}

.users__row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 96px 88px 150px 220px;
  align-items: center;
  gap: var(--cy-space-4);
  padding: var(--cy-space-3) 0;
  border-bottom: 1px solid var(--cy-border);
}

.users__row--head {
  padding: var(--cy-space-2) 0;
  color: var(--cy-text-muted);
  font-size: 12px;
  line-height: 18px;
  font-weight: 600;
}

.users__main {
  display: flex;
  align-items: center;
  gap: var(--cy-space-3);
  min-width: 0;
}

.users__info {
  min-width: 0;
}

.users__name {
  display: flex;
  align-items: center;
  gap: var(--cy-space-2);
  color: var(--cy-text);
  font-size: 14px;
  line-height: 20px;
  font-weight: 500;
}

/* 「当前账号」只是标记，不做成按钮：自己那一行本来就没有可做的操作 */
.users__self {
  flex-shrink: 0;
  padding: 0 8px;
  border-radius: var(--cy-radius-full);
  background: var(--cy-surface-2);
  color: var(--cy-text-muted);
  font-size: 11px;
  line-height: 20px;
  font-weight: 400;
}

.users__meta {
  margin-top: 2px;
  display: flex;
  gap: 4px;
  min-width: 0;
}

.users__email {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 标签：只有这里带语义色 */
.users__tag {
  justify-self: start;
  height: 24px;
  padding: 0 10px;
  border-radius: var(--cy-radius-full);
  font-size: 12px;
  line-height: 24px;
  white-space: nowrap;
}

.users__tag.is-user {
  background: var(--cy-surface-2);
  color: var(--cy-text-secondary);
}

.users__tag.is-admin {
  background: var(--cy-primary-soft);
  color: var(--cy-accent);
}

.users__tag.is-normal {
  background: var(--cy-primary-soft);
  color: var(--cy-accent);
}

.users__tag.is-disabled {
  background: var(--cy-danger-soft);
  color: var(--cy-danger);
}

.users__cell--time {
  color: var(--cy-text-secondary);
  font-size: 13px;
}

/* 操作列：两个按钮左对齐排成一行 */
.users__actions {
  display: flex;
  justify-content: flex-start;
  gap: var(--cy-space-2);
}

/* 破坏性操作只把文字换成危险色，不铺底色（与视频管理一致） */
.users__danger {
  color: var(--cy-danger);
}

.users__actions .users__danger:hover:not(:disabled) {
  border-color: var(--cy-danger);
  color: var(--cy-danger);
}

/* 骨架行：与真实行同高（头像 36px + 上下内边距） */
.users__row--skeleton {
  grid-template-columns: minmax(0, 1fr) 120px;
}

.users__sk-line {
  height: 14px;
  border-radius: var(--cy-radius-xs);
}

.users__sk-line--short {
  width: 60%;
}

.users__pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--cy-space-4);
  margin-top: var(--cy-space-5);
}

/* 窄屏：隐藏注册时间与角色列，保留「账号 + 状态 + 操作」 */
@media (max-width: 899px) {
  .users__row {
    grid-template-columns: minmax(0, 1fr) auto auto;
    gap: var(--cy-space-3);
  }

  .users__row--head,
  .users__cell--time {
    display: none;
  }

  /* 角色列在窄屏同样让位：状态比角色更常看 */
  .users__row:not(.users__row--head) > .users__tag:first-of-type {
    display: none;
  }
}
</style>
