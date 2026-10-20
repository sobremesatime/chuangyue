package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	videov1 "github.com/sobremesatime/chuangyue/api/gen/video/v1"
	"github.com/sobremesatime/chuangyue/internal/transcode/dto"
	videomodel "github.com/sobremesatime/chuangyue/internal/video/model"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/metrics"
	"github.com/sobremesatime/chuangyue/pkg/storage"
)

// staticURLPrefix 与网关静态目录挂载前缀保持一致，用于把产物路径换算成对外地址。
const staticURLPrefix = "/uploads/"

// maxErrorMsgLen 是落库的失败原因最大长度，与 error_msg 列宽对齐。
const maxErrorMsgLen = 500

// Worker 消费转码任务并回写视频状态。
//
// 读写视频状态一律经视频服务的 gRPC 客户端：Worker 独立成进程后不再直连业务库，
// 状态查询走 GetTranscodeState、结果回写走 ReportTranscodeResult。
type Worker struct {
	upload    config.UploadConfig
	transcode config.TranscodeConfig
	video     videov1.VideoServiceClient
	coder     *Transcoder
	store     storage.Storage
}

// NewWorker 构造 Worker。
func NewWorker(
	upload config.UploadConfig,
	transcode config.TranscodeConfig,
	video videov1.VideoServiceClient,
	coder *Transcoder,
	store storage.Storage,
) *Worker {
	return &Worker{upload: upload, transcode: transcode, video: video, coder: coder, store: store}
}

// Handle 处理一条 Kafka 消息。返回 nil 表示可以提交位移。
//
// 幂等性来自两道闸：解析失败直接丢弃；视频状态不再是「转码中」则跳过，
// 因此同一条任务被重复投递也不会产生重复转码或状态错乱。
func (w *Worker) Handle(ctx context.Context, payload []byte) error {
	var task dto.Task
	if err := json.Unmarshal(payload, &task); err != nil {
		logger.L().Warn("丢弃无法解析的转码任务", zap.ByteString("payload", payload), zap.Error(err))
		return nil
	}

	state, err := w.video.GetTranscodeState(ctx, &videov1.GetTranscodeStateRequest{VideoId: task.VideoID})
	if status.Code(err) == codes.NotFound {
		// 视频已被删除，任务作废
		return nil
	}
	if err != nil {
		return err
	}
	// 幂等闸：状态不再是「转码中」说明已有处理结果，直接跳过
	if int8(state.GetStatus()) != videomodel.StatusTranscoding {
		return nil
	}

	sourceURL := state.GetSourceUrl()
	if sourceURL == "" {
		sourceURL = task.SourceURL
	}

	// 源文件可能落在本地磁盘，也可能在对象存储：统一经 storage 抽象取到本地路径。
	sourcePath, cleanup, err := w.materializeSource(ctx, task.VideoID, sourceURL)
	if err != nil {
		logger.L().Error("获取源文件失败",
			zap.Int64("video_id", task.VideoID),
			zap.String("source", sourceURL),
			zap.Error(err),
		)
		// 位移不提交：Worker 重启或分区再均衡后该任务会重新投递
		return err
	}
	defer cleanup()

	logger.L().Info("开始转码",
		zap.Int64("video_id", task.VideoID),
		zap.String("source", sourcePath),
	)

	var lastErr error
	// MaxRetry 表示「重试次数」，总尝试次数为其加一（首次执行也算一次）
	for attempt := 1; attempt <= w.transcode.MaxRetry+1; attempt++ {
		res, err := w.coder.Transcode(ctx, task.VideoID, sourcePath)
		if err == nil {
			playURL := w.toStaticURL(res.MasterPath)
			if _, err := w.video.ReportTranscodeResult(ctx, &videov1.ReportTranscodeResultRequest{
				VideoId:  task.VideoID,
				PlayUrl:  playURL,
				Duration: int32(res.Duration),
				Status:   int32(videomodel.StatusPublished),
			}); err != nil {
				return err
			}
			// 回写成功后才计数：回写失败的任务会重投，提前计数会把同一次转码算成多次。
			metrics.Default().ObserveTranscodeResult("success")
			logger.L().Info("转码完成",
				zap.Int64("video_id", task.VideoID),
				zap.String("play_url", playURL),
				zap.Int("duration", res.Duration),
			)
			return nil
		}

		lastErr = err
		logger.L().Warn("转码尝试失败",
			zap.Int64("video_id", task.VideoID),
			zap.Int("attempt", attempt),
			zap.Error(err),
		)
		// Worker 收到退出信号时不必继续重试，把任务留给下一次启动
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	// 重试耗尽：清理半成品并标记失败，任务不再重投
	_ = os.RemoveAll(filepath.Join(w.transcode.OutputDir, strconv.FormatInt(task.VideoID, 10)))
	logger.L().Error("转码最终失败", zap.Int64("video_id", task.VideoID), zap.Error(lastErr))
	if _, err := w.video.ReportTranscodeResult(ctx, &videov1.ReportTranscodeResultRequest{
		VideoId:  task.VideoID,
		Status:   int32(videomodel.StatusFailed),
		ErrorMsg: truncate(lastErr.Error(), maxErrorMsgLen),
	}); err != nil {
		return err
	}
	// 同上：只有失败结果落库成功后才计入失败数，避免重投造成重复计数。
	metrics.Default().ObserveTranscodeResult("failed")
	return nil
}

// materializeSource 把源文件准备成本地可读路径，并返回清理回调。
//
// 本地静态资源直接给本地路径（零拷贝）；对象存储地址则流式下载到临时目录，
// 因为 FFmpeg 需要可 seek 的本地输入，无法直接消费流。
func (w *Worker) materializeSource(ctx context.Context, videoID int64, ref string) (string, func(), error) {
	if strings.HasPrefix(ref, staticURLPrefix) {
		return w.localPath(ref), func() {}, nil
	}

	key, ok := w.store.KeyFromURL(ref)
	if !ok {
		return "", nil, fmt.Errorf("无法解析源文件地址: %s", ref)
	}
	rc, err := w.store.Open(ctx, key)
	if err != nil {
		return "", nil, fmt.Errorf("打开源文件失败: %w", err)
	}
	// 只读流的关闭错误无需处理，读取失败在上面的 io.Copy 已能捕获
	defer func() { _ = rc.Close() }()

	dir := filepath.Join(w.upload.LocalDir, "tmp", "transcode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("创建临时目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, fmt.Sprintf("src-%d-*.bin", videoID))
	if err != nil {
		return "", nil, fmt.Errorf("创建临时文件失败: %w", err)
	}
	if _, err := io.Copy(tmp, rc); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", nil, fmt.Errorf("下载源文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", nil, fmt.Errorf("关闭临时文件失败: %w", err)
	}
	return tmp.Name(), func() { _ = os.Remove(tmp.Name()) }, nil
}

// localPath 把对外地址还原为本地磁盘路径。
func (w *Worker) localPath(url string) string {
	if !strings.HasPrefix(url, staticURLPrefix) {
		return url
	}
	return filepath.Join(w.upload.LocalDir, strings.TrimPrefix(url, staticURLPrefix))
}

// toStaticURL 把产物绝对路径换算成对外播放地址。
//
// 用 filepath.Rel 求相对上传根目录的路径，避免因 "./uploads" 与 "uploads"
// 这类写法差异导致前缀匹配失败（filepath.Join 会把 "./" 清理掉）。
func (w *Worker) toStaticURL(absPath string) string {
	rel, err := filepath.Rel(w.upload.LocalDir, absPath)
	if err != nil {
		rel = filepath.Base(absPath)
	}
	return staticURLPrefix + filepath.ToSlash(rel)
}

// truncate 截断字符串到最多 n 个字符。
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}
