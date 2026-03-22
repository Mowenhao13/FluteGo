//go:build !windows
// +build !windows

package io

// ExtractData 从 IO 上下文中提取数据字节 (Unix 版本)
func ExtractData(ctxObj interface{}) ([]byte, bool) {
	switch obj := ctxObj.(type) {
	case []byte:
		// Unix 直接返回的字节数组
		return obj, true
	default:
		return nil, false
	}
}
