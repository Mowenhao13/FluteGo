/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package errs

import (
	"context"
	"fmt"
)

// ErrorLevel 定义了分级错误的严重度，用于调度不同的处理策略。
//
// # 枚举值
//
//   - `LevelDebug`: 调试信息
//   - `LevelWarning`: 可恢复警告
//   - `LevelError`: 一般错误
//   - `LevelFatal`: 致命错误
type ErrorLevel uint8

const (
	LevelDebug   ErrorLevel = iota // 调试信息，不影响正常流程
	LevelWarning                   // 警告，可恢复的错误
	LevelError                     // 一般错误，需要记录但不需要停止
	LevelFatal                     // 严重错误，需要停止相关流程
)

// LeveledError 将错误与传输上下文、严重级别及 FDT ID 绑定。
//
// # 字段
//
//   - `Level`: 错误级别
//   - `Err`: 原始错误
//   - `FdtID`: 关联的文件数据传输标识
//   - `Context`: 上下文
type LeveledError struct {
	Level   ErrorLevel
	Err     error
	FdtID   uint8
	Context context.Context
}

func (le *LeveledError) Error() string {
	return fmt.Sprintf("[%s] FdtID:%d %s: %v", le.levelString(), le.FdtID, le.Context, le.Err)
}

// levelString 返回错误级别的字符串表示。
func (le *LeveledError) levelString() string {
	switch le.Level {
	case LevelDebug:
		return "DEBUG"
	case LevelWarning:
		return "WARNING"
	case LevelError:
		return "ERROR"
	case LevelFatal:
		return "FATAL"
	default:
		return "UNKNOWN"
	}
}

// 错误通道定义
// ErrorChannels 保存各级错误通道，供系统模块分发错误。
type ErrorChannels struct {
	DebugChan   chan *LeveledError // 调试信息
	WarningChan chan *LeveledError // 警告信息
	ErrorChan   chan *LeveledError // 一般错误
	FatalChan   chan *LeveledError // 严重错误
}

func InitErrorChannels() ErrorChannels {
	return ErrorChannels{
		DebugChan:   make(chan *LeveledError, 1024),
		WarningChan: make(chan *LeveledError, 1024),
		ErrorChan:   make(chan *LeveledError, 1024),
		FatalChan:   make(chan *LeveledError, 1024),
	}
}

// InitError 构造带等级的错误对象。
//
// # 参数
//
//   - `ctx`: 传输上下文
//   - `level`: 错误级别
//   - `err`: 原始错误
//   - `fdtID`: 文件数据传输标识
//
// # 返回值
//
//	`LeveledError` 包含所有附加信息，便于错误分类与报告
func InitError(ctx context.Context, level uint8, err error, fdtID uint8) *LeveledError {
	return &LeveledError{
		Context: ctx,
		Level:   ErrorLevel(level),
		Err:     err,
		FdtID:   fdtID,
	}
}
