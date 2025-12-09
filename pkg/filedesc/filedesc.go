/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */
package filedesc

import (
	utils "FluteGo/pkg/utils"
	"os"
)

// FileDesc 封装了传输文件的完整元数据。
//
// # 字段说明
//
//   - `FdtID`: 文件传输唯一标识符
//   - `SendPath`: 发送端文件路径
//   - `Name`: 文件名
//   - `TransferLen`: 文件传输大小
//   - `ContentType`: MIME 类型
//   - `Md5`: 文件 MD5 校验值
type FileDesc struct {
	FdtID       uint8
	SendPath    string
	Name        string
	TransferLen uint64
	ContentType string
	Md5         string
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

	fd := &FileDesc{
		FdtID:       fdtID,
		SendPath:    file.Name(),
		Name:        info.Name(),
		TransferLen: uint64(info.Size()),
		ContentType: utils.GetContentType(file),
		Md5:         md5,
	}

	return fd, nil
}
