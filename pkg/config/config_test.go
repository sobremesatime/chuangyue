package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testConfig = `
app:
  name: chuangyue
  env: dev
  addr: ":8080"
  mode: debug

log:
  level: info
  format: console
  output: stdout

mysql:
  host: 127.0.0.1
  port: 3306
  username: root
  password: file-password
  database: chuangyue
  charset: utf8mb4
  conn_max_lifetime: 1h

redis:
  host: 127.0.0.1
  port: 6379

jwt:
  access_secret: a
  refresh_secret: r
  access_expire: 15m
  refresh_expire: 168h
  issuer: chuangyue

upload:
  driver: local
  local_dir: ./uploads
  max_size_mb: 2048
  allowed_ext: [".mp4", ".mov"]
`

// TestLoad 验证配置文件解析，包括 time.Duration 与字符串切片的解码。
func TestLoad(t *testing.T) {
	path := writeTempConfig(t)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	if cfg.App.Name != "chuangyue" || cfg.App.Addr != ":8080" {
		t.Errorf("app 解析异常: %+v", cfg.App)
	}
	if cfg.MySQL.ConnMaxLifetime != time.Hour {
		t.Errorf("conn_max_lifetime 期望 1h，实际 %v", cfg.MySQL.ConnMaxLifetime)
	}
	if cfg.JWT.AccessExpire != 15*time.Minute {
		t.Errorf("access_expire 期望 15m，实际 %v", cfg.JWT.AccessExpire)
	}
	if len(cfg.Upload.AllowedExt) != 2 || cfg.Upload.AllowedExt[0] != ".mp4" {
		t.Errorf("allowed_ext 解析异常: %v", cfg.Upload.AllowedExt)
	}
	if want := "root:file-password@tcp(127.0.0.1:3306)/chuangyue?charset=utf8mb4&parseTime=True&loc=Local"; cfg.MySQL.DSN() != want {
		t.Errorf("DSN 拼接异常:\n期望 %s\n实际 %s", want, cfg.MySQL.DSN())
	}
}

// TestEnvOverride 验证环境变量能覆盖配置文件中的值。
func TestEnvOverride(t *testing.T) {
	path := writeTempConfig(t)

	t.Setenv("CHUANGYUE_MYSQL_PASSWORD", "env-password")
	t.Setenv("CHUANGYUE_APP_ADDR", ":9090")
	t.Setenv("CHUANGYUE_REDIS_PORT", "6380")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	if cfg.MySQL.Password != "env-password" {
		t.Errorf("环境变量未覆盖 mysql.password，实际 %q", cfg.MySQL.Password)
	}
	if cfg.App.Addr != ":9090" {
		t.Errorf("环境变量未覆盖 app.addr，实际 %q", cfg.App.Addr)
	}
	if cfg.Redis.Port != 6380 {
		t.Errorf("环境变量未覆盖 redis.port，实际 %d", cfg.Redis.Port)
	}
}

// writeTempConfig 把测试配置写入临时文件，返回其路径。
func writeTempConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(testConfig), 0o644); err != nil {
		t.Fatalf("写入临时配置失败: %v", err)
	}
	return path
}
