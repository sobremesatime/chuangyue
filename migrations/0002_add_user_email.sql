-- 窗月 · 迁移 0002：用户表增加登录邮箱与唯一键
-- 变更目的：注册与登录改为以邮箱为准，username 由邮箱前缀派生。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行，
--       存量 dev 库需手动执行本文件一次。

-- ----------------------------
-- 升级
-- ----------------------------
ALTER TABLE `user`
  ADD COLUMN `email` VARCHAR(128) NOT NULL DEFAULT '' COMMENT '登录邮箱' AFTER `username`;

-- 存量行（老版本只有用户名）用不可达的占位邮箱补齐：
-- 空串会与 uk_email 唯一键冲突，而占位域使用 RFC 2606 保留域 .invalid，永不投递。
UPDATE `user` SET `email` = CONCAT('legacy-', `id`, '@chuangyue.invalid') WHERE `email` = '';

ALTER TABLE `user`
  ADD UNIQUE KEY `uk_email` (`email`);

-- ----------------------------
-- 回滚
-- ----------------------------
-- ALTER TABLE `user` DROP KEY `uk_email`;
-- ALTER TABLE `user` DROP COLUMN `email`;
