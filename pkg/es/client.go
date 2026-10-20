// Package es 提供视频检索所需的 Elasticsearch 最小客户端。
//
// 为什么不用官方客户端：本项目只用到四个动作——建索引、写一条、删一条、按关键词查一页。
// 用 net/http 直接说 JSON 反而更透明：请求体、映射、查询 DSL 都能在代码里一眼看全，
// 也避免为一个只被 search 服务使用的依赖引入整套客户端与版本兼容矩阵。
//
// 边界：这里刻意不实现节点发现、重试、连接池调优。单节点部署下这些能力没有收益；
// 换成多节点时应交给前置负载均衡（见 config.ESConfig 的说明）。
package es

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config 是客户端的最小配置。
type Config struct {
	Addresses []string
	Username  string
	Password  string
	Index     string
	Timeout   time.Duration
}

// Client 是 Elasticsearch 的最小 HTTP 客户端，绑定单个索引。
type Client struct {
	addr     *url.URL
	username string
	password string
	index    string
	http     *http.Client
}

// New 构造客户端。
//
// 只做参数校验，不在这里探活：ES 可能比 search 服务晚启动，
// 启动时连不上应表现为「检索退回库内 LIKE」，而不是进程起不来。
func New(cfg Config) (*Client, error) {
	if len(cfg.Addresses) == 0 {
		return nil, errors.New("未配置 Elasticsearch 地址")
	}

	addr, err := url.Parse(strings.TrimRight(cfg.Addresses[0], "/"))
	if err != nil {
		return nil, fmt.Errorf("解析 Elasticsearch 地址失败: %w", err)
	}
	if addr.Scheme != "http" && addr.Scheme != "https" {
		return nil, fmt.Errorf("Elasticsearch 地址必须是 http(s): %s", cfg.Addresses[0])
	}
	if cfg.Index == "" {
		return nil, errors.New("未配置 Elasticsearch 索引名")
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	return &Client{
		addr:     addr,
		username: cfg.Username,
		password: cfg.Password,
		index:    cfg.Index,
		http:     &http.Client{Timeout: timeout},
	}, nil
}

// EnsureIndex 确保索引存在，不存在则按视频检索所需的映射创建。
//
// 映射里只有「可检索字段」与少量过滤 / 排序字段；封面、播放量等展示字段
// 刻意不存——检索只负责给出有序的 video_id，卡片由视频服务补全（见 search 服务），
// 这样计数 / 封面变化不会让索引变成一份需要持续追平的副本。
func (c *Client) EnsureIndex(ctx context.Context) error {
	status, err := c.do(ctx, http.MethodHead, c.path(), nil, nil)
	if err == nil && status == http.StatusOK {
		return nil
	}
	if err != nil && status != http.StatusNotFound {
		return err
	}

	mapping := map[string]any{
		"settings": map[string]any{
			// 单节点部署：副本无处分配，设 0 让集群健康度保持 green；
			// 多节点时应改为 >=1，否则节点故障即丢索引。
			"number_of_shards":   1,
			"number_of_replicas": 0,
		},
		"mappings": map[string]any{
			"properties": map[string]any{
				"id":          map[string]any{"type": "long"},
				"title":       map[string]any{"type": "text"},
				"description": map[string]any{"type": "text"},
				"category_id": map[string]any{"type": "long"},
				"created_at":  map[string]any{"type": "date"},
			},
		},
	}

	status, err = c.do(ctx, http.MethodPut, c.path(), mapping, nil)
	if err != nil {
		// 多副本同时启动时可能都判断「不存在」再一起创建，后到者会收到
		// resource_already_exists_exception。这里按「已存在」处理即可。
		if status == http.StatusBadRequest && strings.Contains(err.Error(), "resource_already_exists_exception") {
			return nil
		}
		return err
	}
	return nil
}

// Index 写入（或覆盖）一条文档。
//
// 用 PUT /_doc/{id} 而不是 bulk：单条写入足够简单，且覆盖写天然幂等——
// Kafka 至少一次投递会把同一条消息重放到这里，结果不变。
func (c *Client) Index(ctx context.Context, id string, doc any) error {
	_, err := c.do(ctx, http.MethodPut, c.path("_doc", id), doc, nil)
	return err
}

// Delete 删除一条文档；文档本来就不存在（404）视为成功。
func (c *Client) Delete(ctx context.Context, id string) error {
	status, err := c.do(ctx, http.MethodDelete, c.path("_doc", id), nil, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

// Count 返回索引中的文档数，供启动回填判断「索引是否为空」。
func (c *Client) Count(ctx context.Context) (int64, error) {
	var resp struct {
		Count int64 `json:"count"`
	}
	if _, err := c.do(ctx, http.MethodGet, c.path("_count"), nil, &resp); err != nil {
		return 0, err
	}
	return resp.Count, nil
}

// DeleteIndex 删除整个索引。
//
// 用途是运维重建：索引是派生数据，出现 mapping 冲突或数据漂移时，
// 删掉索引、重启搜索服务即可从视频服务全量回填。集成测试也用它清理现场。
func (c *Client) DeleteIndex(ctx context.Context) error {
	status, err := c.do(ctx, http.MethodDelete, c.path(), nil, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

// SearchIDs 按关键词与分类检索一页，返回有序的 video_id 与命中总数。
//
// 查询用 multi_match 的 phrase 类型：项目没有安装中文分词插件，standard 分析器会把
// 中文切成单字 token，phrase 要求这些单字在原文中相邻，于是「窗月」只会命中包含
// 连续「窗月」的文档，精度接近短语匹配；英文则按单词匹配。标题权重高于简介。
// 关键词为空时退化为 match_all（按创建时间倒序），与库内检索「空关键词不过滤」一致。
func (c *Client) SearchIDs(ctx context.Context, keyword string, categoryID int64, from, size int) ([]int64, int64, error) {
	must := map[string]any{"match_all": map[string]any{}}
	if keyword != "" {
		must = map[string]any{
			"multi_match": map[string]any{
				"query":  keyword,
				"fields": []string{"title^3", "description"},
				"type":   "phrase",
			},
		}
	}

	filter := make([]any, 0, 1)
	if categoryID > 0 {
		filter = append(filter, map[string]any{
			"term": map[string]any{"category_id": categoryID},
		})
	}

	body := map[string]any{
		"from":             from,
		"size":             size,
		"track_total_hits": true,
		"query": map[string]any{
			"bool": map[string]any{
				"must":   []any{must},
				"filter": filter,
			},
		},
		"sort":    []any{map[string]any{"created_at": map[string]any{"order": "desc"}}},
		"_source": []string{"id"},
	}

	var resp struct {
		Hits struct {
			Total struct {
				Value int64 `json:"value"`
			} `json:"total"`
			Hits []struct {
				ID string `json:"_id"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if _, err := c.do(ctx, http.MethodPost, c.path("_search"), body, &resp); err != nil {
		return nil, 0, err
	}

	ids := make([]int64, 0, len(resp.Hits.Hits))
	for _, hit := range resp.Hits.Hits {
		id, err := strconv.ParseInt(hit.ID, 10, 64)
		if err != nil {
			// 索引里混入了非视频 ID 的文档：跳过而不是让整次检索失败。
			continue
		}
		ids = append(ids, id)
	}
	return ids, resp.Hits.Total.Value, nil
}

// Refresh 立即让最近的写入对检索可见。
//
// 生产路径不调用它：索引由 Kafka 异步追平，1s 的刷新间隔对搜索没有影响。
// 它只服务于集成测试——写完立刻查，不必等刷新周期。
func (c *Client) Refresh(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodPost, c.path("_refresh"), nil, nil)
	return err
}

// path 拼出「/索引/子路径...」形式的请求路径。
func (c *Client) path(parts ...string) string {
	p := "/" + c.index
	for _, part := range parts {
		p += "/" + part
	}
	return p
}

// do 发起一次请求并解析响应；返回的 status 在出错时也有效，便于调用方区分 404。
func (c *Client) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	u := *c.addr
	u.Path = strings.TrimRight(c.addr.Path, "/") + path

	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("序列化 Elasticsearch 请求失败: %w", err)
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return 0, fmt.Errorf("构造 Elasticsearch 请求失败: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("请求 Elasticsearch 失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		// 错误体通常就是 ES 的诊断信息（如 mapping 冲突），截断后带进错误消息，
		// 让排障时不必再去翻 ES 日志。
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, fmt.Errorf("Elasticsearch %s %s 返回 %d: %s",
			method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("解析 Elasticsearch 响应失败: %w", err)
		}
	}
	return resp.StatusCode, nil
}
