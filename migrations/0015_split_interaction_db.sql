-- 窗月 · 迁移 0015：互动域物理拆库 + 计数归位
--
-- 变更目的（两件事一起做，缺一不可）：
--   1) 物理拆库：互动域从主库 chuangyue 拆出独立库 chuangyue_interaction，
--      拥有独立账号，服务只连自己的库，不再读写 video 表（也不再 JOIN 它取标题）。
--   2) 计数归位：新增 video_stat 表作为点赞 / 收藏计数的真源（互动域自持）；
--      主库 video 表的 like_count / favorite_count 降级为只读副本，由互动域经
--      Kafka（topic: chuangyue.video.stat）异步回写，仅供热度排序使用。
--
-- 幂等：全部使用 IF NOT EXISTS / INSERT IGNORE，可在已有库上重复执行。
-- 说明：docker-entrypoint-initdb.d 只在空数据目录时执行；存量 dev 库需手动执行本文件一次：
--   mysql -uroot -p < migrations/0015_split_interaction_db.sql
--
-- 拆出的互动表：video_like / video_favorite / video_watch_later / video_watch_history /
-- video_comment / video_comment_like / video_danmaku / notification。

-- ----------------------------
-- 1. 独立库与账号
-- ----------------------------
CREATE DATABASE IF NOT EXISTS `chuangyue_interaction`
  DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 互动域专用账号：只授予 chuangyue_interaction 库的权限，从权限层面保证它碰不到主库。
CREATE USER IF NOT EXISTS 'chy_interaction'@'%' IDENTIFIED BY 'chy_interaction_pwd';
GRANT ALL PRIVILEGES ON `chuangyue_interaction`.* TO 'chy_interaction'@'%';
FLUSH PRIVILEGES;

-- ----------------------------
-- 2. 互动表结构搬运（含索引）与数据搬迁
-- ----------------------------
-- 用 CREATE TABLE ... LIKE 复制结构（连带索引与列定义），避免在此重写一遍 DDL 造成漂移。
-- 随后 INSERT IGNORE 搬运存量数据；唯一键保证重复执行不产生重复行。

-- video_like
CREATE TABLE IF NOT EXISTS `chuangyue_interaction`.`video_like` LIKE `chuangyue`.`video_like`;
INSERT IGNORE INTO `chuangyue_interaction`.`video_like` SELECT * FROM `chuangyue`.`video_like`;

-- video_favorite
CREATE TABLE IF NOT EXISTS `chuangyue_interaction`.`video_favorite` LIKE `chuangyue`.`video_favorite`;
INSERT IGNORE INTO `chuangyue_interaction`.`video_favorite` SELECT * FROM `chuangyue`.`video_favorite`;

-- video_watch_later
CREATE TABLE IF NOT EXISTS `chuangyue_interaction`.`video_watch_later` LIKE `chuangyue`.`video_watch_later`;
INSERT IGNORE INTO `chuangyue_interaction`.`video_watch_later` SELECT * FROM `chuangyue`.`video_watch_later`;

-- video_watch_history
CREATE TABLE IF NOT EXISTS `chuangyue_interaction`.`video_watch_history` LIKE `chuangyue`.`video_watch_history`;
INSERT IGNORE INTO `chuangyue_interaction`.`video_watch_history` SELECT * FROM `chuangyue`.`video_watch_history`;

-- video_comment
CREATE TABLE IF NOT EXISTS `chuangyue_interaction`.`video_comment` LIKE `chuangyue`.`video_comment`;
INSERT IGNORE INTO `chuangyue_interaction`.`video_comment` SELECT * FROM `chuangyue`.`video_comment`;

-- video_comment_like
CREATE TABLE IF NOT EXISTS `chuangyue_interaction`.`video_comment_like` LIKE `chuangyue`.`video_comment_like`;
INSERT IGNORE INTO `chuangyue_interaction`.`video_comment_like` SELECT * FROM `chuangyue`.`video_comment_like`;

-- video_danmaku
CREATE TABLE IF NOT EXISTS `chuangyue_interaction`.`video_danmaku` LIKE `chuangyue`.`video_danmaku`;
INSERT IGNORE INTO `chuangyue_interaction`.`video_danmaku` SELECT * FROM `chuangyue`.`video_danmaku`;

-- notification
CREATE TABLE IF NOT EXISTS `chuangyue_interaction`.`notification` LIKE `chuangyue`.`notification`;
INSERT IGNORE INTO `chuangyue_interaction`.`notification` SELECT * FROM `chuangyue`.`notification`;

-- ----------------------------
-- 3. 计数真源表 video_stat（互动库）
-- ----------------------------
-- 一个视频一行，video_id 直接作主键（无自增 ID）：互动计数的真源。
CREATE TABLE IF NOT EXISTS `chuangyue_interaction`.`video_stat` (
  `video_id`      BIGINT UNSIGNED NOT NULL            COMMENT '视频 ID（主键）',
  `like_count`    BIGINT          NOT NULL DEFAULT 0  COMMENT '点赞数（真源）',
  `favorite_count` BIGINT         NOT NULL DEFAULT 0  COMMENT '收藏数（真源）',
  `updated_at`    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`video_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='视频互动计数（真源）';

-- 用主库 video 表现有的计数初始化真源，避免拆库后计数从 0 开始。
INSERT IGNORE INTO `chuangyue_interaction`.`video_stat` (`video_id`, `like_count`, `favorite_count`)
  SELECT `id`, `like_count`, `favorite_count` FROM `chuangyue`.`video`;

-- 单库开发模式（interaction_mysql.host 留空时回退主库）也需要这张表；
-- 两种部署方式下互动服务连的都是同一个 video_stat 定义，口径一致。
CREATE TABLE IF NOT EXISTS `chuangyue`.`video_stat` LIKE `chuangyue_interaction`.`video_stat`;
INSERT IGNORE INTO `chuangyue`.`video_stat` SELECT * FROM `chuangyue_interaction`.`video_stat`;

-- ----------------------------
-- 4. 收尾提示（不自动执行）
-- ----------------------------
-- 主库 chuangyue 里的互动表已成历史（数据已搬运到互动库且服务不再读写它们）。
-- 确认互动服务运行正常后，可手动清理以避免误用：
--   DROP TABLE `chuangyue`.`video_like`, `chuangyue`.`video_favorite`,
--     `chuangyue`.`video_watch_later`, `chuangyue`.`video_watch_history`,
--     `chuangyue`.`video_comment`, `chuangyue`.`video_comment_like`,
--     `chuangyue`.`video_danmaku`, `chuangyue`.`notification`,
--     `chuangyue`.`video_stat`;

-- ----------------------------
-- 回滚
-- ----------------------------
-- 回滚前须先把互动服务切回主库（清空 interaction_mysql.host），并确认主库里的互动表仍在。
-- DROP USER IF EXISTS 'chy_interaction'@'%';
-- DROP DATABASE IF EXISTS `chuangyue_interaction`;
-- DROP TABLE IF EXISTS `chuangyue`.`video_stat`;

