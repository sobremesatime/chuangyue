-- 窗月 · 迁移 0003：用户表增加个人资料字段
-- 变更目的：个人中心支持维护签名 / 性别 / 生日 / 学校 / 现居地。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
ALTER TABLE `user`
  ADD COLUMN `signature` VARCHAR(140) NOT NULL DEFAULT '' COMMENT '个人签名' AFTER `avatar`,
  ADD COLUMN `gender`    TINYINT      NOT NULL DEFAULT 0  COMMENT '0 未设置 1 男 2 女 3 保密' AFTER `signature`,
  ADD COLUMN `birthday`  DATE         NULL                COMMENT '生日' AFTER `gender`,
  ADD COLUMN `school`    VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '学校' AFTER `birthday`,
  ADD COLUMN `location`  VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '现居地' AFTER `school`;

-- ----------------------------
-- 回滚
-- ----------------------------
-- ALTER TABLE `user`
--   DROP COLUMN `location`,
--   DROP COLUMN `school`,
--   DROP COLUMN `birthday`,
--   DROP COLUMN `gender`,
--   DROP COLUMN `signature`;
