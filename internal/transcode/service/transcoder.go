// Package service 承载转码域的业务逻辑：调用 FFmpeg 产出多码率 HLS，
// 以及消费转码任务并回写视频状态。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sobremesatime/chuangyue/pkg/config"
)

// Transcoder 负责把单个源文件转成多码率 HLS。
type Transcoder struct {
	cfg config.TranscodeConfig
}

// NewTranscoder 构造 Transcoder。
func NewTranscoder(cfg config.TranscodeConfig) *Transcoder {
	return &Transcoder{cfg: cfg}
}

// Result 描述一次转码的产物。
type Result struct {
	// MasterPath 是 master.m3u8 的绝对路径，调用方据此换算对外播放地址。
	MasterPath string
	// Duration 是源视频时长（秒），回写到视频表。
	Duration int
}

// streamInfo 描述已产出的一个清晰度，用于生成 master 清单。
type streamInfo struct {
	uri       string // 相对 master 的清单路径，如 stream_0/index.m3u8
	bandwidth int    // 带宽（bps），视频 + 音频
	width     int
	height    int
}

// Transcode 把源文件转成多码率 HLS。
//
// 按配置逐个档位调用 FFmpeg 产出独立清单，最后手写一份 master.m3u8 汇总。
// 相比一次性 filter_complex + var_stream_map 的写法，逐档执行对「源无音轨」
// 这类情况更健壮，失败也能定位到具体档位。
func (t *Transcoder) Transcode(ctx context.Context, videoID int64, sourcePath string) (*Result, error) {
	if _, err := os.Stat(sourcePath); err != nil {
		return nil, fmt.Errorf("源文件不可读: %w", err)
	}

	meta, err := t.probe(ctx, sourcePath)
	if err != nil {
		return nil, err
	}

	idStr := strconv.FormatInt(videoID, 10)
	outDir := filepath.Join(t.cfg.OutputDir, idStr)
	// 幂等：同一条任务可能被重复投递，先清掉上一次的残留再重新产出
	if err := os.RemoveAll(outDir); err != nil {
		return nil, fmt.Errorf("清理旧输出目录失败: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建输出目录失败: %w", err)
	}

	renditions := planRenditions(t.cfg.Renditions, meta.Height)
	streams := make([]streamInfo, 0, len(renditions))
	for i, r := range renditions {
		dir := filepath.Join(outDir, "stream_"+strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建清晰度目录失败: %w", err)
		}
		if err := t.transcodeOne(ctx, sourcePath, dir, r); err != nil {
			return nil, fmt.Errorf("转码 %s 失败: %w", r.Name, err)
		}

		width := evenWidth(meta.Width, meta.Height, r.Height)
		streams = append(streams, streamInfo{
			uri:       filepath.ToSlash(filepath.Join("stream_"+strconv.Itoa(i), "index.m3u8")),
			bandwidth: parseBitrate(r.Bitrate) + audioBitrate,
			width:     width,
			height:    r.Height,
		})
	}

	masterPath := filepath.Join(outDir, "master.m3u8")
	if err := writeMaster(masterPath, streams); err != nil {
		return nil, err
	}
	return &Result{MasterPath: masterPath, Duration: meta.Duration}, nil
}

// audioBitrate 是各档位统一的音频码率（bps），仅用于 master 清单的带宽估算。
const audioBitrate = 128 * 1024

// audioBitrateArg 是传给 FFmpeg 的音频码率参数值。
const audioBitrateArg = "128k"

// transcodeOne 产出单个清晰度的清单与分片。
func (t *Transcoder) transcodeOne(ctx context.Context, sourcePath, outDir string, r config.RenditionConfig) error {
	args := []string{
		"-y",
		"-i", sourcePath,
		// -2 让宽度按源比例自动计算并对齐到偶数，避免出现奇数宽度的编码错误
		"-vf", fmt.Sprintf("scale=-2:%d", r.Height),
		"-c:v", "libx264",
		"-preset", t.cfg.Preset,
		"-b:v", r.Bitrate,
		"-c:a", "aac",
		"-b:a", audioBitrateArg,
		"-ac", "2",
		"-f", "hls",
		"-hls_time", strconv.Itoa(t.cfg.SegmentSec),
		// vod 表示点播：清单一次写全，播放器可任意拖动
		"-hls_playlist_type", "vod",
		"-hls_segment_filename", filepath.Join(outDir, "seg_%03d.ts"),
		filepath.Join(outDir, "index.m3u8"),
	}
	return t.runFFmpeg(ctx, args)
}

// runFFmpeg 执行 FFmpeg，失败时把 stderr 末尾带进错误信息，便于排查。
func (t *Transcoder) runFFmpeg(ctx context.Context, args []string) error {
	return run(ctx, t.cfg.FFmpegPath, args)
}

// probeInfo 是 ffprobe 读出的源视频信息。
type probeInfo struct {
	Width    int
	Height   int
	Duration int
}

// probe 用 ffprobe 读取源视频的分辨率与时长。
func (t *Transcoder) probe(ctx context.Context, path string) (*probeInfo, error) {
	out, err := runOutput(ctx, t.cfg.FFprobePath, []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height:format=duration",
		"-of", "json",
		path,
	})
	if err != nil {
		return nil, fmt.Errorf("读取视频信息失败: %w", err)
	}

	var parsed struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("解析视频信息失败: %w", err)
	}
	if len(parsed.Streams) == 0 || parsed.Streams[0].Height <= 0 {
		return nil, fmt.Errorf("未读取到视频流信息")
	}

	duration, _ := strconv.ParseFloat(parsed.Format.Duration, 64)
	return &probeInfo{
		Width:    parsed.Streams[0].Width,
		Height:   parsed.Streams[0].Height,
		Duration: int(math.Round(duration)),
	}, nil
}

// planRenditions 结合源分辨率过滤档位：不做放大转码，并按高度去重。
//
// 例如源本身只有 480p 时，720p 档位会被裁掉，最终只产出一档。
func planRenditions(configured []config.RenditionConfig, sourceHeight int) []config.RenditionConfig {
	planned := make([]config.RenditionConfig, 0, len(configured))
	used := make(map[int]bool, len(configured))
	for _, r := range configured {
		height := r.Height
		if sourceHeight > 0 && height > sourceHeight {
			height = sourceHeight
		}
		height = height - height%2
		if height <= 0 || used[height] {
			continue
		}
		used[height] = true
		r.Height = height
		planned = append(planned, r)
	}

	// 全部档位都被裁掉（源分辨率异常小）时，退化成按源高度出一档
	if len(planned) == 0 {
		fallback := configured[len(configured)-1]
		h := sourceHeight - sourceHeight%2
		if h <= 0 {
			h = 480
		}
		fallback.Height = h
		planned = append(planned, fallback)
	}
	return planned
}

// evenWidth 按源宽高比推算目标宽度并对齐到偶数，与 FFmpeg 的 scale=-2:H 口径一致。
func evenWidth(srcWidth, srcHeight, targetHeight int) int {
	if srcWidth <= 0 || srcHeight <= 0 {
		return targetHeight * 16 / 9
	}
	width := int(math.Round(float64(targetHeight) * float64(srcWidth) / float64(srcHeight)))
	width -= width % 2
	if width < 2 {
		width = 2
	}
	return width
}

// parseBitrate 把 "1400k" / "2800K" / "1200000" 这类写法解析为 bps。
func parseBitrate(raw string) int {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return 0
	}
	multiplier := 1
	switch {
	case strings.HasSuffix(s, "k"):
		multiplier = 1024
		s = strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "m")
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return v * multiplier
}

// writeMaster 生成 master.m3u8，按带宽升序列出所有清晰度。
func writeMaster(path string, streams []streamInfo) error {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	for _, s := range streams {
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%dx%d\n", s.bandwidth, s.width, s.height)
		b.WriteString(s.uri)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("写入 master 清单失败: %w", err)
	}
	return nil
}

// run 执行外部命令，失败时附带 stderr 末尾内容。
func run(ctx context.Context, name string, args []string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, tail(stderr.String(), 400))
	}
	return nil
}

// runOutput 执行外部命令并返回标准输出。
func runOutput(ctx context.Context, name string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, tail(stderr.String(), 400))
	}
	return out, nil
}

// tail 截取字符串末尾最多 n 个字符。
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
