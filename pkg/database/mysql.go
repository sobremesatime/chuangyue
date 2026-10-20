// Package database 封装 MySQL（GORM）连接初始化。
package database

import (
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm/schema"

	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/logger"
)

// NewMySQL 依据配置创建 GORM 实例，并配置连接池。
//
// 连接池参数很关键：
//   - MaxOpenConns 控制并发连接上限，防止压垮数据库；
//   - MaxIdleConns 保持一定空闲连接，避免频繁握手；
//   - ConnMaxLifetime 让连接定期重建，规避 MySQL 侧的 wait_timeout 断连。
func NewMySQL(cfg config.MySQLConfig) (*gorm.DB, error) {
	db, err := gorm.Open(mysql.Open(cfg.DSN()), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormLogLevel(cfg.LogLevel)),
		// TranslateError 把驱动层错误翻译成 GORM 的哨兵错误，
		// 例如唯一键冲突 -> gorm.ErrDuplicatedKey，业务层才能识别并重试。
		TranslateError: true,
		// SingularTable=true 保持单数表名，与迁移脚本中的 `user` / `video` 一致，
		// 否则 GORM 默认会把 user 映射为 users。
		NamingStrategy: schema.NamingStrategy{SingularTable: true},
	})
	if err != nil {
		return nil, fmt.Errorf("连接 MySQL 失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层 sql.DB 失败: %w", err)
	}

	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	if cfg.ConnMaxLifetime == 0 {
		sqlDB.SetConnMaxLifetime(time.Hour)
	}

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("MySQL Ping 失败: %w", err)
	}

	logger.L().Info("MySQL 连接成功", zap.String("database", cfg.Database))
	return db, nil
}

// gormLogLevel 将配置中的日志级别映射为 GORM 的日志级别。
func gormLogLevel(level string) gormlogger.LogLevel {
	switch level {
	case "silent":
		return gormlogger.Silent
	case "error":
		return gormlogger.Error
	case "warn":
		return gormlogger.Warn
	default:
		return gormlogger.Info
	}
}
