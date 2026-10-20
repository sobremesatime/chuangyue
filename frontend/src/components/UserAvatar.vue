<script setup lang="ts">
// 用户头像：设置了自定义头像时显示图片，否则显示默认头像。
//
// 默认头像是内联 SVG：星紫圆盘上立着一个白色人影，只用到品牌自身的两种颜色；
// 整幅图做极轻的呼吸，用于承载「还没设置头像」的状态，也不额外引入图片资源。

import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    /** 头像地址；为空时显示默认头像 */
    src?: string
    /** 边长（像素） */
    size?: number
    /** 无障碍替代文本 */
    alt?: string
  }>(),
  { src: '', size: 32, alt: '用户头像' },
)

const boxStyle = computed(() => ({
  width: `${props.size}px`,
  height: `${props.size}px`,
}))
</script>

<template>
  <span class="avatar" :style="boxStyle">
    <img v-if="props.src" class="avatar__img" :src="props.src" :alt="props.alt" />
    <svg v-else class="avatar__art" viewBox="0 0 128 128" role="img" :aria-label="props.alt">
      <!-- 圆盘：整块星紫 -->
      <circle class="avatar__disc" cx="64" cy="64" r="64" />
      <!-- 人形：头和肩，超出圆盘的部分由容器自身的圆形裁剪切掉 -->
      <circle class="avatar__figure" cx="64" cy="52" r="17" />
      <path class="avatar__figure" d="M 28 132 C 28 92, 44 78, 64 78 C 84 78, 100 92, 100 132 Z" />
    </svg>
  </span>
</template>

<style scoped>
.avatar {
  display: block;
  flex-shrink: 0;
  line-height: 0;
  border-radius: var(--cy-radius-full);
  overflow: hidden;
}

.avatar__img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  border-radius: var(--cy-radius-full);
}

/* 整幅图极轻地呼吸一次，作为默认头像的动态 */
.avatar__art {
  width: 100%;
  height: 100%;
  border-radius: var(--cy-radius-full);
  animation: avatar-breath var(--cy-dur-moon) var(--cy-ease) infinite;
}

.avatar__disc {
  fill: var(--cy-primary);
}

.avatar__figure {
  fill: var(--cy-on-primary);
}

@keyframes avatar-breath {
  0%,
  100% {
    opacity: 0.85;
  }
  50% {
    opacity: 1;
  }
}
</style>
