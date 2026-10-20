// Package logger 基于 Zap 封装全局日志器。
//
// 目标是让各服务用同一套字段与级别输出日志：
//   - dev 环境用 console 格式，人眼可读；
//   - prod 环境用 json 格式，便于日志采集系统解析。
package logger

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/sobremesatime/chuangyue/pkg/tracing"
)

// 全局日志器。Init 之后通过 L() 获取，避免在各处重复传递。
var global *zap.Logger

// Init 按配置初始化全局日志器。
//
//	level  日志级别：debug / info / warn / error
//	format 输出格式：console / json
//	output 输出目标：stdout 或文件路径
func Init(level, format, output string) error {
	lvl, err := parseLevel(level)
	if err != nil {
		return err
	}

	encoder := newEncoder(format)
	ws := zapcore.AddSync(os.Stdout)
	if output != "" && output != "stdout" {
		f, err := os.OpenFile(output, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("打开日志文件失败: %w", err)
		}
		ws = zapcore.AddSync(f)
	}

	core := zapcore.NewCore(encoder, ws, lvl)
	// AddCaller 记录调用位置，AddCallerSkip(1) 跳过本包的封装层，指向真正的调用点。
	global = zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1), zap.AddStacktrace(zapcore.ErrorLevel))
	return nil
}

// L 返回全局日志器。若未初始化则返回一个默认的开发日志器，保证不会 panic。
func L() *zap.Logger {
	if global == nil {
		global, _ = zap.NewDevelopment()
	}
	return global
}

// Sync 刷新缓冲区，进程退出前调用。
func Sync() {
	if global != nil {
		_ = global.Sync()
	}
}

// TraceField 返回携带 trace_id 的日志字段，用于把请求内的服务日志串到同一条链路上。
//
// trace_id 由网关生成、经 gRPC metadata 透传（见 pkg/tracing）；服务自身（如定时任务）
// 没有链路时值为空串，不影响日志正常输出。
func TraceField(ctx context.Context) zap.Field {
	return zap.String("trace_id", tracing.TraceID(ctx))
}

// parseLevel 将字符串级别映射为 zapcore.Level。
func parseLevel(level string) (zapcore.Level, error) {
	switch strings.ToLower(level) {
	case "debug":
		return zapcore.DebugLevel, nil
	case "info", "":
		return zapcore.InfoLevel, nil
	case "warn", "warning":
		return zapcore.WarnLevel, nil
	case "error":
		return zapcore.ErrorLevel, nil
	default:
		return zapcore.InfoLevel, fmt.Errorf("未知日志级别: %s", level)
	}
}

// newEncoder 根据格式返回对应的编码器。
func newEncoder(format string) zapcore.Encoder {
	if strings.ToLower(format) == "json" {
		return zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	}
	cfg := zap.NewDevelopmentEncoderConfig()
	cfg.EncodeTime = zapcore.ISO8601TimeEncoder
	return zapcore.NewConsoleEncoder(cfg)
}
