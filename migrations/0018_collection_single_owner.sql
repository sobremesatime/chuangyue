-- 窗月 · 迁移 0018：合集收敛为单归属（一个视频最多属于一个合集）
-- 变更目的：把 0017 的多对多语义改成 B 站式「合集 = 有序剧集列表」，
--           视频详情页只需要展示它所属那一个合集的完整剧集列表。
-- 设计取舍：
--   - 不把 collection_id 塞进 video 表：保留现有两张表与仓储 / 服务代码，
--     用 collection_video.video_id 上的唯一键表达单归属。唯一键是「一个视频只属于一个合集」
--     的唯一事实来源，并发写入时由数据库兜底，而不是依赖应用层先查后写。
--   - 去重保留 id 最小（最早加入）的那条关联：它决定视频最终留在哪个合集；
--     选最早的一条让结果可预期，也不会打乱既有播放顺序。
--   - 去重后重算 video_count：该列是冗余计数，不重算的话，被删掉关联的合集
--     会继续显示比实际多的集数。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
-- 每个 video_id 只保留 id 最小（最早加入）的一条关联。
DELETE cv FROM collection_video cv
  JOIN collection_video keep ON keep.video_id = cv.video_id AND keep.id < cv.id;

ALTER TABLE collection_video ADD UNIQUE KEY uk_video (video_id);
-- 唯一键本身就能服务按 video_id 的查找，idx_video 成了重复索引。
ALTER TABLE collection_video DROP KEY idx_video;

-- 重算冗余计数，避免去重后列值漂移。
UPDATE collection SET video_count =
  (SELECT COUNT(*) FROM collection_video WHERE collection_id = collection.id);

-- ----------------------------
-- 回滚
-- ----------------------------
-- ALTER TABLE collection_video ADD KEY idx_video (video_id);
-- ALTER TABLE collection_video DROP KEY uk_video;
-- 回滚无法恢复被去重删除的关联行，需要时只能从业务数据侧重建。
