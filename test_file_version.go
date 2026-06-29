package main

import (
	"FluteGo/pkg/filedesc"
	"FluteGo/pkg/meta"
	"FluteGo/pkg/receiver"
	"fmt"
	"log"
	"os"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	fmt.Println("=== 文件版本判断机制测试 ===")

	// 测试 1: ETag 生成
	fmt.Println("\n=== 测试 1: ETag 生成 ===")
	testETagGeneration()

	// 测试 2: 文件版本变化检测
	fmt.Println("\n=== 测试 2: 文件版本变化检测 ===")
	testFileVersionChange()

	// 测试 3: 缓存控制更新
	fmt.Println("\n=== 测试 3: 缓存控制更新 ===")
	testCacheControlUpdate()

	fmt.Println("\n=== 所有测试完成 ===")
}

// 测试 ETag 生成
func testETagGeneration() {
	// 创建临时文件
	tmpFile, err := os.CreateTemp("", "test_*.txt")
	if err != nil {
		log.Fatalf("创建临时文件失败: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// 写入内容
	content := []byte("Hello, World!")
	if _, err := tmpFile.Write(content); err != nil {
		log.Fatalf("写入文件失败: %v", err)
	}
	tmpFile.Close()

	// 重新打开文件
	tmpFile, err = os.Open(tmpFile.Name())
	if err != nil {
		log.Fatalf("打开文件失败: %v", err)
	}
	defer tmpFile.Close()

	// 获取文件描述
	fd, err := filedesc.GetFileDesc(tmpFile, 1)
	if err != nil {
		log.Fatalf("获取文件描述失败: %v", err)
	}

	fmt.Printf("文件: %s\n", fd.Name)
	fmt.Printf("大小: %d bytes\n", fd.TransferLen)
	fmt.Printf("MD5: %s\n", fd.Md5)
	fmt.Printf("ETag: %s\n", fd.FileETag)

	// 验证 ETag 格式
	fmt.Printf("预期 ETag 格式: md5-modtime\n")
	if fd.FileETag != "" {
		fmt.Println("✓ ETag 生成成功")
	} else {
		fmt.Println("✗ ETag 生成失败")
	}
}

// 测试文件版本变化检测
func testFileVersionChange() {
	// 创建接收端 FDT 管理器
	fdtReceiver := receiver.NewFDTReceiver()

	// 设置回调
	fileAdded := false
	fileUpdated := false

	fdtReceiver.SetCallbacks(
		func(toi uint32, file meta.FDTFile) {
			fmt.Printf("✓ 新文件添加: TOI=%d, ETag=%s, MD5=%s\n",
				toi, file.FileETag, file.ContentMD5)
			fileAdded = true
		},
		func(toi uint32) {
			fmt.Printf("✓ 文件移除: TOI=%d\n", toi)
		},
		func(toi uint32, file meta.FDTFile) {
			fmt.Printf("✓ 文件更新: TOI=%d, ETag=%s, MD5=%s\n",
				toi, file.FileETag, file.ContentMD5)
			fileUpdated = true
		},
	)

	fdtReceiver.Start()
	defer fdtReceiver.Stop()

	// 发送第一个 FDT 版本
	fmt.Println("\n--- 发送 FDT 版本 1 ---")
	fdt1 := &meta.FDTInstance{
		XMLNS:    meta.FDTNamespace,
		FdtID:    1,
		Version:  1,
		Expires:  uint32(time.Now().Add(24 * time.Hour).Unix()),
		Complete: true,
	}
	fdt1.AddFile(meta.FDTFile{
		ContentLocation: "/tmp/test.txt",
		TOI:             1,
		TransferLength:  13,
		ContentType:     "text/plain",
		ContentMD5:      "abc123",
		FileETag:        "abc123-1234567890",
	})

	err := fdtReceiver.ProcessFDT(fdt1)
	if err != nil {
		log.Fatalf("处理 FDT1 失败: %v", err)
	}

	if !fileAdded {
		log.Fatal("文件添加回调未触发")
	}

	// 发送第二个 FDT 版本（文件未变化）
	fmt.Println("\n--- 发送 FDT 版本 2 (文件未变化) ---")
	fdt2 := &meta.FDTInstance{
		XMLNS:    meta.FDTNamespace,
		FdtID:    2,
		Version:  2,
		Expires:  uint32(time.Now().Add(24 * time.Hour).Unix()),
		Complete: true,
	}
	fdt2.AddFile(meta.FDTFile{
		ContentLocation: "/tmp/test.txt",
		TOI:             1,
		TransferLength:  13,
		ContentType:     "text/plain",
		ContentMD5:      "abc123",
		FileETag:        "abc123-1234567890", // ETag 未变化
	})

	fileUpdated = false
	err = fdtReceiver.ProcessFDT(fdt2)
	if err != nil {
		log.Fatalf("处理 FDT2 失败: %v", err)
	}

	if !fileUpdated {
		fmt.Println("✓ 文件未变化，更新回调未触发（正确）")
	} else {
		log.Fatal("文件未变化，但更新回调被触发（错误）")
	}

	// 发送第三个 FDT 版本（文件变化）
	fmt.Println("\n--- 发送 FDT 版本 3 (文件变化) ---")
	fdt3 := &meta.FDTInstance{
		XMLNS:    meta.FDTNamespace,
		FdtID:    3,
		Version:  3,
		Expires:  uint32(time.Now().Add(24 * time.Hour).Unix()),
		Complete: true,
	}
	fdt3.AddFile(meta.FDTFile{
		ContentLocation: "/tmp/test.txt",
		TOI:             1,
		TransferLength:  20, // 大小变化
		ContentType:     "text/plain",
		ContentMD5:      "def456", // MD5 变化
		FileETag:        "def456-1234567900", // ETag 变化
	})

	fileUpdated = false
	err = fdtReceiver.ProcessFDT(fdt3)
	if err != nil {
		log.Fatalf("处理 FDT3 失败: %v", err)
	}

	if fileUpdated {
		fmt.Println("✓ 文件变化，更新回调触发（正确）")
	} else {
		log.Fatal("文件变化，但更新回调未触发（错误）")
	}

	// 验证状态
	state := fdtReceiver.GetFileState(1)
	if state != nil {
		fmt.Printf("最终状态: TOI=%d, Size=%d, MD5=%s, ETag=%s\n",
			state.TOI, state.TotalBytes, state.File.ContentMD5, state.File.FileETag)
	}
}

// 测试缓存控制更新
func testCacheControlUpdate() {
	// 测试不同的缓存控制策略
	testCases := []struct {
		name     string
		old      string
		new      string
		expected bool
	}{
		{"no-cache -> max-stale", "no-cache", "max-stale", true},
		{"max-stale -> no-cache", "max-stale", "no-cache", true},
		{"max-age=3600 -> max-age=7200", "max-age=3600", "max-age=7200", true},
		{"max-age=3600 -> max-age=3600", "max-age=3600", "max-age=3600", false},
	}

	fdtExpires := time.Now().Add(24 * time.Hour)

	for _, tc := range testCases {
		oldCC := meta.ParseCacheControl(tc.old, fdtExpires)
		newCC := meta.ParseCacheControl(tc.new, fdtExpires)

		result := oldCC.ShouldUpdate(newCC)

		status := "✓"
		if result != tc.expected {
			status = "✗"
		}

		fmt.Printf("%s %s: %v (预期: %v)\n", status, tc.name, result, tc.expected)
	}
}
