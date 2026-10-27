-- 窗月 · 迁移 0017：新增视频合集（跨视频的播放列表）
-- 变更目的：支撑「一个合集按顺序编排多个视频」——合集详情给出有序播放列表，
--           视频详情反查它属于哪些合集并定位当前序号。
-- 设计取舍：
--   - 不加外键：与 0004 起的既有表保持一致。关联完整性由业务层在同一事务内保证
--     （加视频 / 移出 / 删除视频），既避免了外键带来的锁范围放大与分库演进的阻碍，
--     也让测试环境清表不必关心删除顺序。
--   - video_count 是冗余计数：合集列表要展示条数，若逐条 COUNT(*) 会让列表页退化成 N+1；
--     这里在读侧只读该列，由增删视频、删除视频的同一事务里「全量重算」维护
--     （不按 ±1 增量，避免多条写入路径漏算后长期漂移）。
--   - sort 由管理端显式维护：Add 追加到末尾、Reorder 按传入顺序整体重写，
--     不依赖自增或时间戳——播放顺序是业务语义，必须能被人为调整。
--     sort 允许重复（并发追加时可能撞号），读侧统一按 (sort, id) 排序兜底，顺序稳定可复现。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
CREATE TABLE IF NOT EXISTS `collection` (
  `id`          BIGINT UNSIGNED NOT NULL               COMMENT '主键（雪花 ID）',
  `title`       VARCHAR(128)    NOT NULL               COMMENT '合集标题',
  `description` VARCHAR(512)    NOT NULL DEFAULT ''    COMMENT '合集简介',
  `cover_url`   VARCHAR(255)    NOT NULL DEFAULT ''    COMMENT '封面地址；为空时读取侧用第一个视频的封面兜底',
  `uploader_id` BIGINT UNSIGNED NOT NULL               COMMENT '创建者 ID',
  `status`      TINYINT         NOT NULL DEFAULT 1     COMMENT '0草稿 1公开',
  `video_count` BIGINT          NOT NULL DEFAULT 0     COMMENT '冗余的视频数，由写入路径在同一事务内重算',
  `created_at`  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_status_created` (`status`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='视频合集表';

CREATE TABLE IF NOT EXISTS `collection_video` (
  `id`            BIGINT UNSIGNED NOT NULL               COMMENT '主键（雪花 ID）',
  `collection_id` BIGINT UNSIGNED NOT NULL               COMMENT '合集 ID',
  `video_id`      BIGINT UNSIGNED NOT NULL               COMMENT '视频 ID',
  `sort`          INT             NOT NULL DEFAULT 0     COMMENT '合集内播放顺序（升序），手工维护',
  `created_at`    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_collection_video` (`collection_id`, `video_id`),
  KEY `idx_video` (`video_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='合集与视频的关联表';

-- ----------------------------
-- 回滚
-- ----------------------------
-- DROP TABLE IF EXISTS `collection_video`;
-- DROP TABLE IF EXISTS `collection`;
