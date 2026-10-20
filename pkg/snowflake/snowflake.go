// Package snowflake 封装分布式 ID 生成器（Twitter Snowflake 算法）。
//
// 64 位 ID 结构：1 位符号位 + 41 位时间戳 + 10 位机器位 + 12 位序列号。
// 单节点每毫秒可产生 4096 个不重复 ID，且整体趋势递增，适合做 MySQL 主键。
//
// 多节点部署时，每个进程必须使用不同的 nodeID（0~1023），否则会生成重复 ID。
package snowflake

import (
	"fmt"
	"sync"

	"github.com/bwmarrin/snowflake"
)

var (
	mu   sync.RWMutex
	node *snowflake.Node
)

// Init 初始化全局雪花节点。
//
// nodeID 取值范围 0~1023。单机开发固定传 1 即可；
// 集群部署时应从环境变量或配置中心按实例分配唯一值。
func Init(nodeID int64) error {
	n, err := snowflake.NewNode(nodeID)
	if err != nil {
		return fmt.Errorf("初始化雪花节点失败: %w", err)
	}
	mu.Lock()
	node = n
	mu.Unlock()
	return nil
}

// NextID 生成下一个分布式 ID。
// 未初始化时自动以 nodeID=1 初始化，保证调用方无需关心顺序。
func NextID() int64 {
	mu.RLock()
	n := node
	mu.RUnlock()

	if n == nil {
		_ = Init(1)
		mu.RLock()
		n = node
		mu.RUnlock()
	}
	return n.Generate().Int64()
}
