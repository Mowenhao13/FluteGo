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

// FileDesc 文件描述结构
// 功能说明：
//   描述一个传输文件的完整元数据信息，包含文件属性和传输参数
// 核心字段：
//   FdtID        - 文件传输标识符，用于唯一标识传输中的文件
//   SendPath     - 发送端文件完整路径
//   SaveDir      - 接收端保存目录
//   Name         - 文件名（不包括路径）
//   TransferLen  - 文件传输大小（字节数）
//   ContentType  - 文件内容类型（MIME类型）
//   Md5          - 文件MD5校验和（32位十六进制字符串）
type FileDesc struct {
	FdtID       uint8
	SendPath    string
	SaveDir     string
	Name        string
	TransferLen uint64
	ContentType string
	Md5         string
}

// GetFileDesc 获取文件描述信息
// 功能说明：
//   从操作系统文件对象中提取文件元数据，构建完整的文件描述信息
// 参数：
//   file    - 已打开的文件对象，必须可读
//   fdtID   - 文件传输标识符，用于在传输会话中唯一标识此文件
//   saveDir - 接收端保存目录，用于指定文件最终保存位置
// 返回值：
//   *FileDesc - 完整的文件描述信息
//   error     - 处理过程中的错误，包括文件访问错误、计算错误等
// 处理流程：
//   1. 验证文件存在性和可访问性
//   2. 获取文件基本属性（大小、修改时间等）
//   3. 计算文件MD5校验和
//   4. 检测文件内容类型
//   5. 构建并返回文件描述结构
// 错误处理：
//   - 文件不存在或无读取权限
//   - 文件大小为0
//   - 计算MD5失败
//   - 检测内容类型失败
// 使用场景：
//   发送端在启动传输前，接收端在验证接收文件时	
func GetFileDesc(file *os.File, fdtID uint8, saveDir string) (*FileDesc, error) {
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
		SaveDir:     saveDir,
		Name:        info.Name(),
		TransferLen: uint64(info.Size()),
		ContentType: utils.GetContentType(file),
		Md5:         md5,
	}

	return fd, nil
}
