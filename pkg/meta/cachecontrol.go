/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package meta

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ObjectCacheControl 对象缓存控制策略
// 参考 ref/flute/src/receiver/writer/mod.rs
type ObjectCacheControl struct {
	// 缓存类型
	Type CacheControlType
	// 过期时间（仅当 Type 为 ExpiresAt 或 ExpiresAtHint 时有效）
	ExpiresAt time.Time
}

// CacheControlType 缓存控制类型
type CacheControlType int

const (
	// CacheControlNoCache 不缓存
	CacheControlNoCache CacheControlType = iota
	// CacheControlMaxStale 永久缓存
	CacheControlMaxStale
	// CacheControlExpiresAt 指定过期时间
	CacheControlExpiresAt
	// CacheControlExpiresAtHint 建议过期时间
	CacheControlExpiresAtHint
)

// ParseCacheControl 解析 Cache-Control 字符串
// 格式: "no-cache", "max-stale", "max-age=3600", "expires=2024-12-31T23:59:59Z"
func ParseCacheControl(cacheControl string, fdtExpires time.Time) ObjectCacheControl {
	if cacheControl == "" {
		// 默认使用 FDT 过期时间
		return ObjectCacheControl{
			Type:      CacheControlExpiresAtHint,
			ExpiresAt: fdtExpires,
		}
	}

	cacheControl = strings.ToLower(strings.TrimSpace(cacheControl))

	if cacheControl == "no-cache" {
		return ObjectCacheControl{Type: CacheControlNoCache}
	}

	if cacheControl == "max-stale" {
		return ObjectCacheControl{Type: CacheControlMaxStale}
	}

	if strings.HasPrefix(cacheControl, "max-age=") {
		seconds, err := strconv.ParseInt(cacheControl[8:], 10, 64)
		if err == nil {
			return ObjectCacheControl{
				Type:      CacheControlExpiresAt,
				ExpiresAt: time.Now().Add(time.Duration(seconds) * time.Second),
			}
		}
	}

	if strings.HasPrefix(cacheControl, "expires=") {
		expiresStr := cacheControl[8:]
		t, err := time.Parse(time.RFC3339, expiresStr)
		if err == nil {
			return ObjectCacheControl{
				Type:      CacheControlExpiresAt,
				ExpiresAt: t,
			}
		}
	}

	// 解析失败，使用 FDT 过期时间
	return ObjectCacheControl{
		Type:      CacheControlExpiresAtHint,
		ExpiresAt: fdtExpires,
	}
}

// ShouldUpdate 判断是否需要更新缓存
// 参考 ref/flute/src/receiver/writer/mod.rs:40-67
func (occ ObjectCacheControl) ShouldUpdate(newCacheControl ObjectCacheControl) bool {
	switch occ.Type {
	case CacheControlNoCache:
		return newCacheControl.Type != CacheControlNoCache

	case CacheControlMaxStale:
		return newCacheControl.Type != CacheControlMaxStale

	case CacheControlExpiresAt:
		if newCacheControl.Type == CacheControlExpiresAt {
			diff := newCacheControl.ExpiresAt.Sub(occ.ExpiresAt)
			if diff < 0 {
				diff = -diff
			}
			return diff > time.Second
		}
		return true

	case CacheControlExpiresAtHint:
		if newCacheControl.Type == CacheControlExpiresAtHint {
			diff := newCacheControl.ExpiresAt.Sub(occ.ExpiresAt)
			if diff < 0 {
				diff = -diff
			}
			return diff > time.Second
		}
		return true
	}

	return true
}

// String 返回 Cache-Control 字符串表示
func (occ ObjectCacheControl) String() string {
	switch occ.Type {
	case CacheControlNoCache:
		return "no-cache"
	case CacheControlMaxStale:
		return "max-stale"
	case CacheControlExpiresAt:
		return fmt.Sprintf("expires=%s", occ.ExpiresAt.Format(time.RFC3339))
	case CacheControlExpiresAtHint:
		return fmt.Sprintf("max-age=%d", int(occ.ExpiresAt.Sub(time.Now()).Seconds()))
	}
	return ""
}
