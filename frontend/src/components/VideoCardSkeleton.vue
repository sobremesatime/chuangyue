<!-- 视频卡片骨架屏：形状与真实卡片完全一致，避免加载完成时页面跳动。
     数量由调用方决定，必须与每页真实条数一致。 -->
<template>
  <div class="skeleton-card" aria-hidden="true">
    <div class="skeleton-card__cover cy-skeleton" />
    <div class="skeleton-card__line cy-skeleton" />
    <div class="skeleton-card__line skeleton-card__line--short cy-skeleton" />
  </div>
</template>

<style scoped>
.skeleton-card__cover {
  aspect-ratio: 16 / 9;
  border-radius: var(--cy-radius-md);
}

.skeleton-card__line {
  height: 14px;
  margin-top: var(--cy-space-3);
  border-radius: var(--cy-radius-sm);
}

.skeleton-card__line--short {
  width: 55%;
  margin-top: var(--cy-space-2);
}
</style>
