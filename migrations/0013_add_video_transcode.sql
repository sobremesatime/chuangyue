-- 窗月 · 迁移 0013：新增视频转码相关字段
-- 变更目的：支撑异步转码流水线——上传后先落源文件并置为「转码中」，
--           由独立 Worker 产出 HLS 后再置为「已上架」；失败时记录原因。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
ALTER TABLE `video`
  ADD COLUMN `source_url` VARCHAR(255) NOT NULL DEFAULT '' COMMENT '源文件地址（转码输入，保留以便重转）' AFTER `play_url`,
  ADD COLUMN `error_msg`  VARCHAR(512) NOT NULL DEFAULT '' COMMENT '转码失败原因（仅状态为 3 时有值）' AFTER `status`;

-- 存量数据回填：此前没有「源文件」概念，播放地址即为源文件地址。
-- 回填后删除逻辑仍能定位到原文件，不会因为新增字段而漏删。
UPDATE `video` SET `source_url` = `play_url` WHERE `source_url` = '' AND `play_url` <> '';

-- ----------------------------
-- 回滚
-- ----------------------------
-- ALTER TABLE `video` DROP COLUMN `source_url`, DROP COLUMN `error_msg`;