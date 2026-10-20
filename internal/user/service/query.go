// Package service 提供用户信息的只读查询服务。
package service

import (
	"context"

	"github.com/sobremesatime/chuangyue/internal/user/model"
	"github.com/sobremesatime/chuangyue/internal/user/repository"
)

// QueryService 是用户信息的薄查询服务。
//
// 只做委托，便于被 gRPC 与其它调用方复用，也不与注册登录等写入流程（Service）耦合。
type QueryService struct {
	repo *repository.Repository
}

// NewQueryService 构造 QueryService。
func NewQueryService(repo *repository.Repository) *QueryService {
	return &QueryService{repo: repo}
}

// GetByID 按主键查询单个用户，未找到时返回 gorm.ErrRecordNotFound。
func (s *QueryService) GetByID(ctx context.Context, id int64) (*model.User, error) {
	return s.repo.GetByID(ctx, id)
}

// GetByIDs 批量按主键查询用户；入参为空返回空切片。
//
// 只做入参清洗（去零、去重）与委托，不写 SQL：调用方常把多个来源的 id 拼在一起，
// 重复与零值是常态，清洗后可少查几次，也避免把主键 0 这种无意义条件带进 IN 子句。
func (s *QueryService) GetByIDs(ctx context.Context, ids []int64) ([]model.User, error) {
	return s.repo.GetByIDs(ctx, cleanIDs(ids))
}

// cleanIDs 去掉非正数 id 与重复项，保留首次出现的顺序。
func cleanIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
