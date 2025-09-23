package tools

import (
	"errors"
	"time"
)

// NTPToSystemTime 将 64-bit NTP 时间戳转换为 time.Time
// NTP 64 位：高 32 位是秒，低 32 位是小数（秒的小数部分，2^-32 单位）
// NTP 纪元：1900-01-01 00:00:00
// Unix 纪元：1970-01-01 00:00:00
// 两者相差 2208988800 秒
func NTPToSystemTime(ntp uint64) (time.Time, error) {
	const ntpUnixDelta = 2208988800 // seconds between 1900 and 1970
	sec := ntp >> 32
	frac := ntp & 0xFFFFFFFF

	// 把 2^-32 秒的小数换算为纳秒
	// nsec = frac * 1e9 / 2^32
	nsec := (frac * 1_000_000_000) >> 32

	// 允许 pre-1970（负的 Unix 秒）：
	unixSec := int64(sec) - ntpUnixDelta
	if nsec >= 1_000_000_000 {
		return time.Time{}, errors.New("invalid NTP fractional part")
	}

	return time.Unix(unixSec, int64(nsec)).UTC(), nil
}

// NTP epoch: 1900-01-01 00:00:00 UTC
var ntpEpoch = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)

// SystemTimeToNTP converts a Go time.Time into a 64-bit NTP timestamp.
//
// High 32 bits = seconds since 1900-01-01
// Low  32 bits = fractional part of second
func SystemTimeToNTP(t time.Time) (uint64, error) {
	// 确保是 UTC
	t = t.UTC()

	// 距离 NTP epoch 的总秒和纳秒
	secs := uint64(t.Sub(ntpEpoch).Seconds())
	nanos := uint64(t.Nanosecond())

	// 小数部分：nanos / 1e9 * 2^32
	frac := (nanos << 32) / 1e9

	return (secs << 32) | frac, nil
}

func DivCeil(a, b uint64) uint64 {
	return (a + b - 1) / b
}

func DivFloor(a, b uint64) uint64 {
	return a / b
}

func DurationDivFloat(d time.Duration, n float64) time.Duration {
	if n <= 0 {
		return 0
	}
	return time.Duration(float64(d) / n)
}

// StrPtr 返回字符串指针
func StrPtr(s string) *string { return &s }

// Uint8Ptr 返回 uint8 指针
func Uint8Ptr(v uint8) *uint8 { return &v }

// MapOrNil: 若 src 为 nil 返回 nil，否则对其值应用 f 并返回结果指针
func MapOrNil[T any, R any](src *T, f func(T) R) *R {
	if src == nil {
		return nil
	}
	val := f(*src)
	return &val
}

// ValueOrDefault: 若 src 为 nil 返回 R 的零值，否则对其值应用 f 并返回
func ValueOrDefault[T any, R any](src *T, f func(T) R) R {
	var zero R
	if src == nil {
		return zero
	}
	return f(*src)
}
