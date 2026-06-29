/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */
package filedesc

import (
	utils "FluteGo/pkg/utils"
	"fmt"
	"os"
	"time"
)

// FileDesc 封装了传输文件的完整元数据。
//
// # 字段说明
//
//   - `FdtID`: 文件传输唯一标识符
//   - `TOI`: Transport Object Identifier (RFC 6726)
//   - `SendPath`: 发送端文件路径
//   - `Name`: 文件名
//   - `TransferLen`: 文件传输大小
//   - `ContentLength`: 原始内容长度
//   - `ContentType`: MIME 类型
//   - `ContentEncoding`: 内容编码 (如 "identity", "gzip")
//   - `Md5`: 文件 MD5 校验值
//   - `FileETag`: 文件实体标签 (RFC 2616)
//   - `CacheControl`: 缓存控制策略
type FileDesc struct {
	FdtID           uint8
	TOI             uint32
	SendPath        string
	Name            string
	TransferLen     uint64
	ContentLength   uint64
	ContentType     string
	ContentEncoding string
	Md5             string
	FileETag        string
	CacheControl    string
}

// GetFileDesc 从操作系统文件对象构建 FileDesc。
//
// # 参数
//
//   - `file`: 必须可读的文件
//   - `fdtID`: 当前文件的传输标识
//
// # 返回值
//
//   - `*FileDesc`: 包含路径、大小、MD5 等完整描述
//   - `error`: 遇到打开、读取、MD5 计算等错误时返回
//
// # 处理流程
//
//  1. 获取文件属性
//  2. 计算 MD5
//  3. 检测内容类型
//  4. 构建并返回描述结构
func GetFileDesc(file *os.File, fdtID uint8) (*FileDesc, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}

	if info.Size() == 0 {
		return nil, os.ErrInvalid
	}

	md5, err := utils.CalculateMd5(file)
	if err != nil {
		return nil, err
	}

	// 生成 ETag：使用 MD5 和修改时间
	etag := GenerateETag(md5, info.ModTime())

	fd := &FileDesc{
		FdtID:           fdtID,
		TOI:             0, // TOI 将在 FDT 发布时分配
		SendPath:        file.Name(),
		Name:            info.Name(),
		TransferLen:     uint64(info.Size()),
		ContentLength:   uint64(info.Size()),
		ContentType:     utils.GetContentType(file),
		ContentEncoding: "identity", // 默认无编码
		Md5:             md5,
		FileETag:        etag,
		CacheControl:    "", // 可选,由调用方设置
	}

	return fd, nil
}

// GenerateETag 生成文件实体标签
// 参考 RFC 2616 和 ref/flute 的实现
// ETag 格式: "md5-modtime"
func GenerateETag(md5 string, modTime time.Time) string {
	return fmt.Sprintf("%s-%d", md5, modTime.Unix())
}
