package errs

import (
	"context"
	"fmt"
)

type ErrorLevel uint8

const (
	LevelDebug ErrorLevel = iota    // 调试信息，不影响正常流程
	LevelWarning                    // 警告，可恢复的错误
	LevelError                      // 一般错误，需要记录但不需要停止
	LevelFatal                      // 严重错误，需要停止相关流程
)

type LeveledError struct {
	Level   ErrorLevel
	Err     error
	FdtID   uint8
	Context context.Context
}

func (le *LeveledError) Error() string {
	return fmt.Sprintf("[%s] FdtID:%d %s: %v", le.levelString(), le.FdtID, le.Context, le.Err)
}

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
type ErrorChannels struct {
	DebugChan   chan *LeveledError  // 调试信息 
	WarningChan chan *LeveledError  // 警告信息
	ErrorChan   chan *LeveledError  // 一般错误
	FatalChan   chan *LeveledError  // 严重错误
}

func InitErrorChannels() ErrorChannels {
	return ErrorChannels{
		DebugChan:   make(chan *LeveledError, 1024),
		WarningChan: make(chan *LeveledError, 1024),
		ErrorChan:   make(chan *LeveledError, 1024),
		FatalChan:   make(chan *LeveledError, 1024),
	}
}

func InitError(ctx context.Context, level uint8, err error, fdtID uint8) *LeveledError {
	return &LeveledError{
		Context: ctx,
		Level: ErrorLevel(level),
		Err: err,
		FdtID: fdtID,
	}
}