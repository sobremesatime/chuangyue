<script setup lang="ts">
// 应用根组件：路由出口 + 全局登录弹框。
// 弹框挂在根上而不是各页面里，任何页面点顶栏的「登录」都能唤出同一个实例。

import AuthModal from '@/components/AuthModal.vue'
</script>

<template>
  <RouterView />
  <AuthModal />
</template>
