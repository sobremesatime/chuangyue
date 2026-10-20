package dto

// VideoStatChanged 是互动域经 Kafka 回写给视频域的互动计数快照。
//
// 拆库后互动计数（点赞 / 收藏）的真源在互动域的 video_stat 表；视频库的
// like_count / favorite_count 降级为只读副本，仅用于热度排序。这里传「全量快照」
// 而不是增量：消费是至少一次语义，快照天然幂等——重复消费或乱序都只是把同一行
// 覆写成同一个值，不会像增量那样累加出错。
type VideoStatChanged struct {
	VideoID       int64 `json:"video_id"`
	LikeCount     int64 `json:"like_count"`
	FavoriteCount int64 `json:"favorite_count"`
}

// VideoDeleted 是「视频已被删除」事件，由视频域投递、互动域消费。
//
// 只带 video_id：消费侧要清的表都以它为条件，不需要其他字段——结构越小，事件契约越稳定。
// 消费必须幂等：重复消费只是把同一批行再删一遍（DELETE ... WHERE video_id = ?）。
type VideoDeleted struct {
	VideoID int64 `json:"video_id"`
}
