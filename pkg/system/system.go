/*
 * 软件著作权声明：
 * 本文件包含的代码是 FluteGo 软件的组成部分
 * 版权所有 (C) 2025
 * 保留所有权利。
 */

package system

import (
	"FluteGo/constant"
	"FluteGo/pkg/errs"
	meta "FluteGo/pkg/meta"
	"FluteGo/pkg/pool"
	receiver "FluteGo/pkg/receiver"
	"context"
	"fmt"
	"log"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ReceiverSystem 接收端系统
// 功能说明：
//
//	管理文件接收的整个生命周期，包括元数据接收、文件数据接收、错误处理和状态报告
//
// 核心组件：
//   - 元数据接收程序：监听UDP端口接收文件传输的元数据
//   - 文件接收程序：根据元数据启动多端口并行接收文件数据
//   - 错误处理程序：分级处理各种类型的错误
//   - 连接池管理：复用UDP连接，提高性能
//
// 设计模式：
//
//	使用工作池模式控制并发接收任务，避免资源耗尽
type ReceiverSystem struct {
	ctx             context.Context
	cancel          context.CancelFunc
	metaChan        chan *meta.MetaPkt
	errChans        errs.ErrorChannels
	workerPool      chan struct{}
	wg              sync.WaitGroup
	activeReceivers int32
	maxWorkers      int32
	enableMd5       bool
	recvPool        *pool.ConnPool
	metaConn        *pool.WinSocket
	targets         sync.Map
	curReceived     sync.Map

	FileReporter FileReporter
	DestIP       string
	SaveDir      string
}

// FileReporter 文件报告器
// 功能说明：
//
//	收集和分发文件传输的状态报告
//
// 设计特点：
//
//	使用通道机制实现生产者-消费者模式，解耦报告生成和消费
type FileReporter struct {
	ReportChan chan FileReport
	FileChans  map[uint8]chan FileReport
}

// FileReport 文件传输报告
// 字段说明：
//
//	FdtID         - 文件数据传输标识符，唯一标识一个传输任务
//	TotalBytes    - 文件总字节数
//	ReceivedBytes - 已接收字节数
//	Status        - 传输状态：0-传输中，1-已完成，2-错误
//	TotalFiles    - 总文件数（在批量传输中使用）
type FileReport struct {
	FdtID         uint8
	TotalBytes    uint64
	ReceivedBytes uint64
	Status        uint8 // 0-transferring, 1-completed, 2-error
	TotalFiles    uint16
}

// contextKey 上下文键类型
// 用途：
//
//	用于在context.Context中安全地存储和检索值
//	防止键名冲突
type contextKey struct {
	name string
}

// 上下文键定义
// 使用私有结构体类型作为键，避免字符串键的冲突风险
var (
	fdtIDKey    = contextKey{"fdtID"}
	fileSizeKey = contextKey{"fileSize"}
)

// InitReceiverSystem 初始化接收端系统
// 功能说明：
//
//	创建并配置接收端系统的所有组件
//
// 参数：
//
//	maxWorkers - 最大工作协程数，控制并发接收任务数
//	destIP     - 目标IP地址，用于绑定网络连接
//	saveDir    - 文件保存目录路径
//
// 返回值：
//
//	*ReceiverSystem - 初始化完成的接收端系统实例
//	error - 初始化过程中发生的错误
//
// 初始化步骤：
//  1. 创建工作池和错误通道
//  2. 初始化全局连接池
//  3. 设置文件报告器
func InitReceiverSystem(maxWorkers int32, destIP string, saveDir string, enableMd5 bool) (*ReceiverSystem, error) {
	// 参数验证和默认值设置
	if maxWorkers <= 0 {
		maxWorkers = int32(runtime.NumCPU() / 2)
	}

	// 创建可取消的上下文
	ctx, cancel := context.WithCancel(context.Background())

	// 初始化系统实例
	s := &ReceiverSystem{
		ctx:        ctx,
		cancel:     cancel,
		metaChan:   make(chan *meta.MetaPkt, 10),
		errChans:   errs.InitErrorChannels(),
		workerPool: make(chan struct{}, maxWorkers),
		maxWorkers: maxWorkers,
		enableMd5:  enableMd5,
		FileReporter: FileReporter{
			ReportChan: make(chan FileReport, 100),
			FileChans:  make(map[uint8]chan FileReport),
		},
		DestIP:  destIP,
		SaveDir: saveDir,
	}

	// 初始化全局连接池
	pool.InitConnPool(destIP, constant.POOL_RECV)
	s.recvPool = pool.GetConnPool()
	if s.recvPool == nil {
		return nil, fmt.Errorf("pool not initialized")
	}

	return s, nil
}

// StartErrorProgram 启动错误处理程序
// 功能说明：
//
//	在独立协程中启动错误处理循环
//
// 设计模式：
//
//	使用多路复用选择器从不同级别的错误通道读取错误
//
// 错误级别：
//
//	Debug    - 调试信息，仅记录日志
//	Warning  - 警告信息，记录日志并可进行恢复操作
//	Error    - 一般错误，影响单个文件传输
//	Fatal    - 严重错误，可能停止整个系统
func (s *ReceiverSystem) StartErrorProgram() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.handleErrors()
	}()
}

// handleErrors 错误处理主循环
// 功能说明：
//
//	持续监听各个错误级别的通道，分发处理不同类型的错误
//
// 处理流程：
//  1. 从错误通道接收错误
//  2. 根据错误级别记录日志
//  3. 调用对应的错误处理器
//
// 退出条件：
//
//	当系统上下文被取消时退出循环
func (s *ReceiverSystem) handleErrors() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case debugErr := <-s.errChans.DebugChan:
			// 调试信息，通常只记录日志
			log.Printf("DEBUG: %v", debugErr)
			s.handleDebug(debugErr)
		case warningErr := <-s.errChans.WarningChan:
			// 警告信息，记录日志并可能进行一些恢复操作
			log.Printf("WARNING: %v", warningErr)
			s.handleWarning(warningErr)

		case errorErr := <-s.errChans.ErrorChan:
			// 一般错误，需要记录并可能影响单个文件传输
			log.Printf("ERROR: %v", errorErr)
			s.handleError(errorErr)
		case fatalErr := <-s.errChans.FatalChan:
			// 严重错误，可能需要停止整个系统或重要组件
			log.Printf("FATAL: %v", fatalErr)
			s.handleFatalError(fatalErr)
		}
	}
}

// updateFileStatus 更新文件状态
// 功能说明：
//
//	根据传输状态更新文件状态信息
//
// 参数：
//
//	fdtID      - 文件数据传输标识符
//	status     - 传输状态
//	erroeLevel - 错误级别
//
// 待实现：
//
//	当前为TODO占位符，需要实现具体的状态更新逻辑
func (s *ReceiverSystem) updateFileStatus(fdtID uint8, status uint8, erroeLevel uint8) {
	//TODO:
}

// handleError 处理一般错误
// 功能说明：
//
//	处理级别为Error的错误，通常影响单个文件传输
//
// 典型处理：
//  1. 记录错误日志
//  2. 更新文件状态为错误状态
//  3. 可能的恢复操作（如重试机制）
func (s *ReceiverSystem) handleError(err *errs.LeveledError) {
	// 错误处理：记录错误，可能影响单个文件
	// 例如：重试机制或报告给监控系统
	s.updateFileStatus(err.FdtID, 2, uint8(errs.LevelError))
}

// handleWarning 处理警告
// 功能说明：
//
//	处理级别为Warning的警告，通常是需要注意但不影响流程的情况
func (s *ReceiverSystem) handleWarning(err *errs.LeveledError) {
	s.updateFileStatus(err.FdtID, 2, uint8(errs.LevelWarning))
}

// handleFatalError 处理致命错误
// 功能说明：
//
//	处理级别为Fatal的错误，可能导致系统或组件停止
//
// 典型处理：
//  1. 记录致命错误日志
//  2. 可能需要停止相关服务
//  3. 发送警报通知
func (s *ReceiverSystem) handleFatalError(err *errs.LeveledError) {
	s.updateFileStatus(err.FdtID, 2, uint8(errs.LevelFatal))
}

// handleDebug 处理调试信息
// 功能说明：
//
//	处理级别为Debug的调试信息，通常用于开发调试
func (s *ReceiverSystem) handleDebug(err *errs.LeveledError) {
	s.updateFileStatus(err.FdtID, 2, uint8(errs.LevelDebug))
}

// reportError 报告错误
// 功能说明：
//
//	将错误分类并发送到对应的错误通道
//
// 参数：
//
//	ctx    - 上下文，包含传输任务信息
//	level  - 错误级别
//	err    - 原始错误
//	fdtID  - 关联的文件数据传输标识符
//
// 处理流程：
//  1. 创建分级错误对象
//  2. 根据错误级别发送到对应通道
//  3. 由handleErrors协程统一处理
func (s *ReceiverSystem) reportError(ctx context.Context, level uint8, err error, fdtID uint8) {
	Err := errs.InitError(ctx, level, err, fdtID)
	switch Err.Level {
	case errs.LevelDebug:
		s.errChans.DebugChan <- Err
		return
	case errs.LevelWarning:
		s.errChans.WarningChan <- Err
		return
	case errs.LevelError:
		s.errChans.ErrorChan <- Err
		return
	case errs.LevelFatal:
		s.errChans.FatalChan <- Err
		return
	}
}

// StartMetaProgram 启动元数据接收程序
// 功能说明：
//
//	启动独立的协程监听UDP端口接收文件传输的元数据
//
// 核心流程：
//  1. 初始化元数据连接
//  2. 循环读取UDP数据报
//  3. 解析元数据包
//  4. 将有效的元数据包发送到元数据通道
//
// 网络处理：
//
//	支持优雅关闭，检测连接关闭错误
//
// 错误处理：
//
//	初始化失败时报告致命错误
func (s *ReceiverSystem) StartMetaProgram() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(s.metaChan)

		metaConn, merr := s.recvPool.InitMetaConn()
		if metaConn == nil {
			err := fmt.Errorf("Failed to initialize meta connections\n")
			s.reportError(s.ctx, uint8(errs.LevelFatal), err, 0)
			return
		}
		if merr != nil {
			s.reportError(s.ctx, uint8(errs.LevelFatal), merr, 0)
			return
		}

		s.metaConn = metaConn
		log.Printf("[MetaReceiver] Meta receiver listening on %s",
			s.metaConn.Addr.IP.String())

		buf := make([]byte, constant.META_BUF)

		var wsaBuf windows.WSABuf
		wsaBuf.Len = uint32(len(buf))
		wsaBuf.Buf = &buf[0]
		var bytesRecvd uint32

		from := s.metaConn.From.FromAny
		fromLen := s.metaConn.FromLen
		flags := s.metaConn.Flags
		// 设置接收超时
		windows.SetsockoptInt(s.metaConn.Socket, windows.SOL_SOCKET, windows.SO_RCVTIMEO, constant.META_TIMEOUT)

		// 主接收循环
		for {
			select {
			case <-s.ctx.Done():
				return
			default:
			}

			fromLen = int32(unsafe.Sizeof(*from))

			// 从UDP读取数据
			err := windows.WSARecvFrom(s.metaConn.Socket, &wsaBuf, 1, &bytesRecvd, &flags, from, &fromLen, nil, nil)
			if err != nil {
				select {
				case <-s.ctx.Done():
					return
				default:
					// 检查错误是否由于连接关闭引起
					if opErr, ok := err.(*net.OpError); ok && opErr.Err.Error() == "use of closed network connection" {
						return
					}
					if err == windows.WSAETIMEDOUT {
						continue
					}
					if err == windows.WSAENOTSOCK {
						return
					}
					if err == windows.WSAEINTR {
						return
					}
					log.Printf("Meta read error: %v", err)
					continue
				}
			}

			if bytesRecvd == 0 {
				continue
			}

			log.Printf("[MetaReceiver] Received %d bytes from sender", bytesRecvd)
			// 复制数据以避免缓冲区竞争条件
			data := make([]byte, bytesRecvd)
			copy(data, buf[:bytesRecvd])

			// 解析元数据包
			mt, merr := meta.DeserializeMetaPkt(data)
			if merr != nil {
				log.Printf("[MetaReceiver] Failed to deserialize meta packet: %v", merr)
				continue
			}

			log.Printf("[MetaReceiver] Successfully parsed meta packet for file: %s (FdtID: %d)",
				mt.File.Name, mt.File.FdtID)

			if mt.File == nil {
				log.Printf("Meta packet missing file descriptor, skipping")
				continue
			}

			log.Printf("Received meta packet for FdtID: %d", mt.File.FdtID)
			// mt.ShowPktInfo()

			// 将元数据包发送到处理通道
			select {
			case s.metaChan <- mt:
			case <-s.ctx.Done():
				return
			}
		}
	}()
}

// StartFileProgram 启动文件接收管线。
//
// # 描述
//
//	初始化固定数量的 receiverWorker，以并行方式消费 `metaChan` 中的元数据包。
//
// # 并发控制
//
//	通过常量 `constant.ReceiverWorkers` 控制工作协程数量。
func (s *ReceiverSystem) StartFileProgram() {
	log.Printf("Using %d receiverWorkers", constant.ReceiverWorkers)
	for i := 0; i < constant.ReceiverWorkers; i++ {
		s.wg.Add(1)
		go s.receiverWorker(i)
	}
}

// receiverWorker 循环读取元数据包并派发接收任务。
//
// # 参数
//
//   - `id`: 工作协程 ID
//
// # 处理流程
//
//  1. 监听系统上下文
//  2. 从 `metaChan` 读取 `MetaPkt`
//  3. 调用 `processMeta` 启动接收
//
// # 退出条件
//
//	上下文取消或 `metaChan` 关闭
func (s *ReceiverSystem) receiverWorker(id int) {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			return
		case metaPkt, ok := <-s.metaChan:
			if !ok {
				return
			}
			s.processMeta(s.ctx, metaPkt)
		}
	}
}

// processMeta 验证元数据后创建接收任务。
//
// # 步骤
//
//  1. 使用 `targets` 去重
//  2. 将任务送入 `workerPool`，保证并发限制
//  3. 在独立协程中调用 `runReceiver`
//
// # 参数
//
//   - `mainCtx`: 主上下文
//   - `metaPkt`: 待处理的元数据包
func (s *ReceiverSystem) processMeta(mainCtx context.Context, metaPkt *meta.MetaPkt) {
	// 基于FdtID去重
	if _, loaded := s.targets.LoadOrStore(metaPkt.File.FdtID, true); loaded {
		return
	}

	// 通过工作池控制并发
	s.workerPool <- struct{}{}
	s.wg.Add(1)
	go func(ctx context.Context, task *meta.MetaPkt) {
		defer s.wg.Done()
		defer func() { <-s.workerPool }()

		atomic.AddInt32(&s.activeReceivers, 1)
		defer atomic.AddInt32(&s.activeReceivers, -1)

		s.runReceiver(ctx, task)
	}(mainCtx, metaPkt)
}

// runReceiver 执行单个文件的接收生命周期。
//
// # 描述
//
//	为当前 `MetaPkt` 构建上下文、创建连接、初始化 `Receiver`，并在完成时上报状态。
//
// # 核心步骤
//
//  1. 使用 `fdtID` 和 `fileSize` 构建上下文
//  2. 启用报告通道桥接 `receiver.Report`
//  3. 启动文件接收器 `Receiver.Start`
//
// # 错误与资源
//
//	错误通过 `reportError` 上报；所有通道与连接都会在返回前关闭
func (s *ReceiverSystem) runReceiver(mainCtx context.Context, task *meta.MetaPkt) {
	fdtID := task.File.FdtID
	ctx := context.WithValue(mainCtx, fdtIDKey, fdtID)
	if task.File.TransferLen > 0 {
		ctx = context.WithValue(ctx, fileSizeKey, task.File.TransferLen)
	}

	// 创建桥接通道，将 receiver.Report 转换为 system.FileReport
	recvReportChan := make(chan receiver.Report, 100)
	ctx = receiver.WithReportChan(ctx, recvReportChan)

	// 启动协程转发接收报告
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for r := range recvReportChan {
			s.FileReporter.ReportChan <- FileReport{
				FdtID:         r.FdtID,
				TotalBytes:    uint64(r.Total),
				ReceivedBytes: uint64(r.Received),
				Status:        r.Status,
				TotalFiles:    task.TotalFiles,
			}
			if r.Status == 1 {
				return
			}
		}
	}()

	// 为文件创建连接
	conns, _ := s.recvPool.CreateFileConn(fdtID, uint8(task.NumPorts), task.BasePort)
	if len(conns) == 0 {
		err := fmt.Errorf("failed to create connections for fdtID %d", fdtID)
		s.reportError(ctx, uint8(errs.LevelError), err, fdtID)
		close(recvReportChan)
		return
	}
	defer s.recvPool.CloseFileConn(fdtID)

	// 初始化接收器
	recv, err := receiver.InitReceiver(task, s.SaveDir, s.enableMd5)
	if err != nil {
		s.reportError(ctx, uint8(errs.LevelError), err, fdtID)
		close(recvReportChan)
		return
	}
	recv.ShowBasicInfo()

	// 设置完成回调，关闭报告通道
	recv.OnComplete = func() {
		s.FileReporter.ReportChan <- FileReport{
			FdtID:         fdtID,
			Status:        1, // Completed
			TotalFiles:    task.TotalFiles,
			TotalBytes:    task.File.TransferLen,
			ReceivedBytes: task.File.TransferLen,
		}
		close(recvReportChan)
	}

	// 启动接收器
	if err := recv.Start(ctx); err != nil {
		s.reportError(ctx, uint8(errs.LevelError), err, fdtID)
		// 确保错误时通道被关闭
		select {
		case <-recvReportChan:
		default:
			close(recvReportChan)
		}
	}
}
