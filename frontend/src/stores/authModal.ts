// 登录弹框的显隐与当前页签。
//
// 全站只有一个入口：顶栏右侧的「登录」按钮。登录与注册都在同一个弹框里完成，
// 靠这里的 mode 在两种表单之间切换，不再有独立的 /login、/register 页面。

import { ref } from 'vue'
import { defineStore } from 'pinia'

/** 弹框当前展示的表单：登录 / 注册 / 重置密码（忘记密码） */
export type AuthMode = 'login' | 'register' | 'reset'

export const useAuthModalStore = defineStore('authModal', () => {
  const open = ref(false)
  const mode = ref<AuthMode>('login')

  /** 打开弹框；需要登录才能访问的页面会在守卫里调它 */
  function openModal(next: AuthMode = 'login'): void {
    mode.value = next
    open.value = true
  }

  function close(): void {
    open.value = false
  }

  /** 切换页签时清掉上一张表单留下的报错，由弹框组件自己负责 */
  function setMode(next: AuthMode): void {
    mode.value = next
  }

  return { open, mode, openModal, close, setMode }
})
