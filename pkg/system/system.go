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
	"FluteGo/pkg/filedesc"
	meta "FluteGo/pkg/meta"
	"FluteGo/pkg/oti"
	"FluteGo/pkg/pool"
	receiver "FluteGo/pkg/receiver"
	"FluteGo/pkg/sock"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
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
	metaConn        *sock.MsSocket
	targets         sync.Map
	curReceived     sync.Map

	// 单端口架构下活跃的 Receiver 映射（fdtID -> *receiver.Receiver）
	activeReceiverMap sync.Map

	FileReporter FileReporter
	DestIP       string
	SaveDir      string
	saveDirMu    sync.RWMutex // protects SaveDir for concurrent reads/writes

	// OnMetaReceived is an optional callback invoked once per unique FdtID when
	// a MetaPkt is accepted. Set this before calling StartMetaProgram.
	OnMetaReceived func(fdtID uint8, fileName string, fileSize int64, fecType, md5 string)
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
	return InitReceiverSystemWithMulticast(maxWorkers, destIP, saveDir, enableMd5, "")
}

// InitReceiverSystemWithMulticast 初始化接收端系统（支持多播）
// 功能说明：
//
//	创建并配置接收端系统的所有组件，支持加入多播组
//
// 参数：
//
//	maxWorkers  - 最大工作协程数，控制并发接收任务数
//	destIP      - 目标IP地址，用于绑定网络连接
//	saveDir     - 文件保存目录路径
//	enableMd5   - 是否启用MD5校验
//	multicastIP - 多播地址，为空时表示单播模式
//
// 返回值：
//
//	*ReceiverSystem - 初始化完成的接收端系统实例
//	error - 初始化过程中发生的错误
func InitReceiverSystemWithMulticast(maxWorkers int32, destIP string, saveDir string, enableMd5 bool, multicastIP string) (*ReceiverSystem, error) {
	// 参数验证和默认值设置
	if maxWorkers <= 0 {
		maxWorkers = 2 // 单文件传输，避免并行解码拖慢速度
	}

	// 创建可取消的上下文
	ctx, cancel := context.WithCancel(context.Background())

	// 初始化系统实例
	s := &ReceiverSystem{
		ctx:        ctx,
		cancel:     cancel,
		metaChan:   make(chan *meta.MetaPkt, 20),
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
	pool.InitConnPoolWithMulticast(destIP, constant.POOL_RECV, multicastIP)
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

// registerReceiver 注册一个活跃的文件接收器，用于单端口数据包分发。
func (s *ReceiverSystem) registerReceiver(fdtID uint8, recv *receiver.Receiver) {
	s.activeReceiverMap.Store(fdtID, recv)
	log.Printf("[MetaReceiver] Receiver registered for fdtID=%d", fdtID)
}

// unregisterReceiver 注销文件接收器。
func (s *ReceiverSystem) unregisterReceiver(fdtID uint8) {
	s.activeReceiverMap.Delete(fdtID)
}

// dispatchFilePacket 根据 TOI 将文件数据包分发给对应的 Receiver。
// 通过 Receiver 的异步队列分发，避免阻塞 MetaReceiver 主接收循环。
func (s *ReceiverSystem) dispatchFilePacket(ctx context.Context, toi uint32, data []byte) {
	fdtID := uint8(toi)
	if recvVal, ok := s.activeReceiverMap.Load(fdtID); ok {
		recv := recvVal.(*receiver.Receiver)
		// 通过带缓冲的队列异步处理，避免 AddSymbol 或写入文件阻塞接收循环
		if err := recv.EnqueuePacket(ctx, data); err != nil {
			// 队列满时丢弃包（日志在 EnqueuePacket 内部）
		}
	} else {
		// Receiver 尚未注册（可能 FDT 还在处理中），打印日志帮助诊断
		log.Printf("[MetaReceiver] No active receiver for TOI=%d (fdtID=%d), dropping packet (%d bytes)", toi, fdtID, len(data))
	}
}

// StartMetaProgram 启动元数据接收程序（RFC 6726 统一端口架构）
// 功能说明：
//
//	在统一端口接收 FDT XML（TOI=0）和文件数据（TOI>0）
//
// 核心流程：
//  1. 初始化文件连接（统一端口）
//  2. 循环读取UDP数据报
//  3. 解析 LCT 头部
//  4. 根据 TOI 路由：TOI=0 是 FDT XML，TOI>0 是文件数据
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

		// 使用 file port 接收所有数据（统一端口架构）
		// 创建临时连接用于接收 FDT XML（TOI=0）
		// 注意：这里使用 baseFilePort，需要在初始化时设置
		baseFilePort := constant.BASE_FILE_PORT // 默认 3400
		tempConn, err := s.recvPool.CreateFileConn(0, 1, baseFilePort)
		if err != nil || len(tempConn) == 0 {
			errMsg := fmt.Errorf("failed to create temp connection on port %d: %v", baseFilePort, err)
			s.reportError(s.ctx, uint8(errs.LevelFatal), errMsg, 0)
			return
		}

		conn := tempConn[0]
		s.metaConn = conn
		log.Printf("[MetaReceiver] Unified port receiver listening on port %d", baseFilePort)

		buf := make([]byte, constant.META_BUF)

		// 主接收循环
		for {
			select {
			case <-s.ctx.Done():
				return
			default:
			}

			// 从UDP读取数据
			bytesRecvd, err := conn.Socket.ReadFromUDP(buf)
			if err != nil {
				select {
				case <-s.ctx.Done():
					return
				default:
					// 检查错误是否由于连接关闭引起
					if opErr, ok := err.(*net.OpError); ok && opErr.Err.Error() == "use of closed network connection" {
						return
					}
					// 超时错误，继续循环
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						continue
					}
					log.Printf("Unified port read error: %v", err)
					continue
				}
			}

			if bytesRecvd == 0 {
				continue
			}

			log.Printf("[MetaReceiver] Received %d bytes from unified port", bytesRecvd)
			// 复制数据以避免缓冲区竞争条件
			data := make([]byte, bytesRecvd)
			copy(data, buf[:bytesRecvd])

			// 解析 LCT 头部
			if len(data) < meta.LCTHeaderLength {
				log.Printf("[MetaReceiver] Packet too short for LCT header: %d bytes", len(data))
				continue
			}

			var lctHeader meta.LCTHeader
			if err := lctHeader.Decode(data[:meta.LCTHeaderLength]); err != nil {
				log.Printf("[MetaReceiver] Failed to decode LCT header: %v", err)
				continue
			}

			// 根据 TOI 路由
			if lctHeader.TOI == meta.TOIFDT {
				// TOI=0: FDT XML
				fdtXML := data[meta.LCTHeaderLength:]
				log.Printf("[MetaReceiver] Received FDT XML (%d bytes)", len(fdtXML))

				// 解析 FDT XML
				fdt, err := meta.DeserializeFDT(fdtXML)
				if err != nil {
					log.Printf("[MetaReceiver] Failed to deserialize FDT XML: %v", err)
					continue
				}

				log.Printf("[MetaReceiver] Successfully parsed FDT XML: FdtID=%d, Files=%d",
					fdt.FdtID, len(fdt.Files))

				// 将 FDT 转换为 MetaPkt 以兼容现有代码
				for _, file := range fdt.Files {
					mt := &meta.MetaPkt{
						File: &filedesc.FileDesc{
							FdtID:           uint8(fdt.FdtID),
							Name:            file.ContentLocation,
							TransferLen:     file.TransferLength,
							ContentLength:   file.ContentLength,
							ContentType:     file.ContentType,
							ContentEncoding: file.ContentEncoding,
							Md5:             file.ContentMD5,
							FileETag:        file.FileETag,
						},
						Oti: oti.Oti{
							FECEncodingID:        fdt.FECOTIFECEncodingID,
							SymbolSize:           fdt.FECOTIEncodingSymbolLength,
							MaximumChunkSize:     fdt.FECOTIMaxSourceBlockLength,
						},
						TotalFiles: uint16(len(fdt.Files)),
					}

					// 从文件级 FEC-OTI 获取（如果存在）
					if file.FECOTIFECEncodingID != 0 {
						mt.Oti.FECEncodingID = file.FECOTIFECEncodingID
					}
					if file.FECOTIEncodingSymbolLength != 0 {
						mt.Oti.SymbolSize = file.FECOTIEncodingSymbolLength
					}
					if file.FECOTIMaxSourceBlockLength != 0 {
						mt.Oti.MaximumChunkSize = file.FECOTIMaxSourceBlockLength
					}

					log.Printf("[MetaReceiver] Processing file: %s (FdtID: %d, TOI: %d)",
						mt.File.Name, mt.File.FdtID, file.TOI)

					// 将元数据包发送到处理通道
					select {
					case s.metaChan <- mt:
					case <-s.ctx.Done():
						return
					}
				}
			} else {
				// TOI>0: 文件数据，根据 TOI 分发给对应 Receiver
				s.dispatchFilePacket(s.ctx, lctHeader.TOI, data)
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

	// Invoke optional meta-received callback (once per unique FdtID).
	if s.OnMetaReceived != nil {
		s.OnMetaReceived(
			metaPkt.File.FdtID,
			metaPkt.File.Name,
			int64(metaPkt.File.TransferLen),
			fecTypeName(metaPkt.Oti.FECEncodingID),
			metaPkt.File.Md5,
		)
	}

	// 通过工作池控制并发
	log.Printf("[processMeta] fdtID=%d: acquiring workerPool slot (%d/%d)", metaPkt.File.FdtID, len(s.workerPool)+1, cap(s.workerPool))
	s.workerPool <- struct{}{}
	log.Printf("[processMeta] fdtID=%d: acquired workerPool slot", metaPkt.File.FdtID)
	s.wg.Add(1)
	go func(ctx context.Context, task *meta.MetaPkt) {
		defer s.wg.Done()
		defer func() {
			<-s.workerPool
			log.Printf("[processMeta] fdtID=%d: released workerPool slot", task.File.FdtID)
		}()
		defer s.targets.Delete(task.File.FdtID) // 传输完成后释放 FDT ID 槽位

		atomic.AddInt32(&s.activeReceivers, 1)
		defer atomic.AddInt32(&s.activeReceivers, -1)

		log.Printf("[processMeta] goroutine started for fdtID=%d", task.File.FdtID)
		s.runReceiver(ctx, task)
		log.Printf("[processMeta] goroutine finished for fdtID=%d", task.File.FdtID)
	}(mainCtx, metaPkt)
}

// fecTypeName maps a FECEncodingID to a human-readable string.
func fecTypeName(id uint8) string {
	switch id {
	case 0:
		return "NoCode"
	case 1:
		return "RaptorQ"
	case 2:
		return "ReedSolomon"
	default:
		return "Unknown"
	}
}

// SetSaveDir atomically updates the directory where received files are saved.
// Returns an error if the path does not exist or is not a directory.
// Ensure a trailing slash is added to the directory path.
func (s *ReceiverSystem) SetSaveDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("directory not accessible: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}

	// Ensure dir ends with a slash
	if dir != "" && dir[len(dir)-1] != '/' {
		dir += "/"
	}

	s.saveDirMu.Lock()
	s.SaveDir = dir
	s.saveDirMu.Unlock()
	return nil
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
			// 非阻塞发送，防止 FileReporter.ReportChan 满时级联阻塞
			select {
			case s.FileReporter.ReportChan <- FileReport{
				FdtID:         r.FdtID,
				TotalBytes:    uint64(r.Total),
				ReceivedBytes: uint64(r.Received),
				Status:        r.Status,
				TotalFiles:    task.TotalFiles,
			}:
			default:
				// 通道满时丢弃
			}
			if r.Status == 1 {
				return
			}
		}
	}()

	log.Printf("[runReceiver] fdtID=%d, file=%s (unified port mode)",
		fdtID, task.File.Name)

	// 初始化接收器
	s.saveDirMu.RLock()
	saveDir := s.SaveDir
	s.saveDirMu.RUnlock()
	recv, err := receiver.InitReceiver(task, saveDir, s.enableMd5)
	if err != nil {
		log.Printf("[runReceiver] fdtID=%d: InitReceiver failed: %v", fdtID, err)
		s.reportError(ctx, uint8(errs.LevelError), err, fdtID)
		close(recvReportChan)
		return
	}
	recv.ShowBasicInfo()

	// 注册到单端口分发器
	s.registerReceiver(fdtID, recv)
	defer s.unregisterReceiver(fdtID)

	// 设置完成回调，关闭报告通道
	recv.OnComplete = func() {
		actualBytes := recv.GetBytesWritten()
		if actualBytes <= 0 {
			actualBytes = int64(task.File.TransferLen)
		}
		// 超时时报告失败，而非虚假完成
		status := uint8(1) // Completed
		if recv.IsTimedOut() {
			status = 2 // Failed
			log.Printf("fdtID(%d): transfer timed out, reporting as failed (%d/%d bytes)\n", fdtID, actualBytes, task.File.TransferLen)
		}
		// 非阻塞发送完成报告（防止阻塞导致 close(recvReportChan) 不执行）
		select {
		case s.FileReporter.ReportChan <- FileReport{
			FdtID:         fdtID,
			Status:        status,
			TotalFiles:    task.TotalFiles,
			TotalBytes:    task.File.TransferLen,
			ReceivedBytes: uint64(actualBytes),
		}:
		default:
			log.Printf("WARNING: report channel full, completion for fdtId=%d dropped\n", fdtID)
		}
		close(recvReportChan)
	}

	// 启动接收器（单端口被动模式，由 StartMetaProgram 统一分发数据包）
	if err := recv.RunPassive(ctx); err != nil {
		s.reportError(ctx, uint8(errs.LevelError), err, fdtID)
		// 确保错误时通道被关闭
		select {
		case <-recvReportChan:
		default:
			close(recvReportChan)
		}
	}
}
