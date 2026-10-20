package service

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/internal/video/model"
	"github.com/sobremesatime/chuangyue/pkg/logger"
)

// cleanupBatchSize 是单次清理处理的会话条数上限，避免一次拉取过多记录。
const cleanupBatchSize = 200

// cleanupLockKey 是清理任务的单飞锁键：video 多副本部署时，同一轮只允许一个实例执行。
const cleanupLockKey = "lock:video:upload-cleanup"

// cleanupLockTTL 是单飞锁的租期。
//
// 取值需「大于单轮清理的最长耗时、小于执行间隔」：前者保证正常跑完时别的实例不会
// 中途插进来；后者保证持锁实例崩溃后，锁最多跨过一轮就自动释放，不会把清理永久卡死。
// 默认间隔为 1h（jobs.upload_cleanup），30min 的租期满足上述两条。
const cleanupLockTTL = 30 * time.Minute

// CleanupUploads 执行一轮过期上传会话与临时分片的回收，是周期任务的入口。
//
// 调度交给 pkg/cron（间隔见 jobs.upload_cleanup），本方法只负责「一轮」该做什么：
// 先抢单飞锁，再回收。进程启动即跑一轮的行为由任务注册侧的 WithRunOnStart 提供，
// 用于覆盖上次退出（含异常退出）遗留的过期会话。
func (s *Service) CleanupUploads(ctx context.Context) {
	if s.cleanupLock != nil {
		ok, err := s.cleanupLock.TryLock(ctx)
		switch {
		case err != nil:
			logger.L().Warn("获取清理锁失败，本轮不加锁执行", zap.Error(err))
		case !ok:
			logger.L().Debug("清理锁被其它实例持有，跳过本轮")
			return
		default:
			// 释放用 WithoutCancel：进程退出时 ctx 已取消，仍需把锁交还，
			// 否则要等到 TTL 到期，下一个实例才能接手。
			defer func() { _, _ = s.cleanupLock.Unlock(context.WithoutCancel(ctx)) }()
		}
	}

	s.cleanupOnce(ctx)
}

// cleanupOnce 执行一轮清理并记录结果。
func (s *Service) cleanupOnce(ctx context.Context) {
	cleaned, err := s.CleanupExpiredUploads(ctx)
	if err != nil {
		logger.L().Error("清理过期上传会话失败", zap.Error(err))
		return
	}
	if cleaned > 0 {
		logger.L().Info("已清理过期上传会话", zap.Int("count", cleaned))
	}
}

// CleanupExpiredUploads 回收全部过期上传会话，返回清理成功的条数。
//
// 分批处理以控制单次内存占用与事务规模；若某一批没有任何一条清理成功，
// 说明存储或数据库异常，停止本轮以避免空转重试。
func (s *Service) CleanupExpiredUploads(ctx context.Context) (int, error) {
	total := 0
	for {
		sessions, err := s.repo.ListExpiredSessions(ctx, time.Now(), cleanupBatchSize)
		if err != nil {
			return total, err
		}
		if len(sessions) == 0 {
			return total, nil
		}

		progressed := false
		for i := range sessions {
			if err := s.cleanupUploadSession(ctx, &sessions[i]); err != nil {
				logger.L().Warn("清理过期上传会话失败",
					zap.Int64("session_id", sessions[i].ID),
					zap.Error(err))
				continue
			}
			total++
			progressed = true
		}
		if !progressed {
			return total, nil
		}
	}
}

// cleanupUploadSession 回收单个过期会话：先释放驱动侧的临时分片，再删除记录。
//
// 释放临时分片是尽力而为：本地驱动删临时目录、对象存储删分片事务，
// 失败也不阻断记录清理——记录必须先回收，残留的临时文件最多占用磁盘，
// 不影响后续上传（新建会话使用新的分片目录 / uploadID）。
func (s *Service) cleanupUploadSession(ctx context.Context, session *model.UploadSession) error {
	if mp, err := s.store.Multipart(); err == nil {
		_ = mp.AbortMultipart(ctx, session.StorageKey, session.UploadID)
	}
	if err := s.repo.DeleteSessionParts(ctx, session.ID); err != nil {
		return err
	}
	return s.repo.DeleteSession(ctx, session.ID)
}
