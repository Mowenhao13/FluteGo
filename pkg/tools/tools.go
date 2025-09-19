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

func DivCeil(a, b uint64) uint64 {
	return (a + b - 1) / b
}

func DivFloor(a, b uint64) uint64 {
	return a / b
}
