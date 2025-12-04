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
)

type ReceiverSystem struct {
	ctx             context.Context
	cancel          context.CancelFunc
	metaChan        chan *meta.MetaPkt
	errChans        errs.ErrorChannels
	workerPool      chan struct{}
	wg              sync.WaitGroup
	activeReceivers int32
	maxWorkers      int32

	recvPool    *pool.GlobalConnectionPool
	metaConn    *pool.UDPConnWrapper
	targets     sync.Map
	curReceived sync.Map

	FileReporter FileReporter
}

type FileReporter struct {
	ReportChan chan FileReport
	FileChans  map[uint8]chan FileReport
}

type FileReport struct {
	FdtID         uint8
	TotalBytes    uint64
	ReceivedBytes uint64
	Status        uint8 // 0-transferring, 1-completed, 2-error
	TotalFiles    uint16
}

type contextKey struct {
	name string
}

var (
	fdtIDKey    = contextKey{"fdtID"}
	fileSizeKey = contextKey{"fileSize"}
)

func InitReceiverSystem(maxWorkers int32) (*ReceiverSystem, error) {
	if maxWorkers <= 0 {
		maxWorkers = int32(runtime.NumCPU() / 2)
	}

	ctx, cancel := context.WithCancel(context.Background())

	s := &ReceiverSystem{
		ctx:        ctx,
		cancel:     cancel,
		metaChan:   make(chan *meta.MetaPkt, 10),
		errChans:   errs.InitErrorChannels(),
		workerPool: make(chan struct{}, maxWorkers),
		maxWorkers: maxWorkers,
		FileReporter: FileReporter{
			ReportChan: make(chan FileReport, 100),
			FileChans:  make(map[uint8]chan FileReport),
		},
	}

	pool.InitGlobalConnectionPool(int(maxWorkers), constant.MaxMetaConnTimeout, 1)
	s.recvPool = pool.GetGlobalPool()
	if s.recvPool == nil {
		return nil, fmt.Errorf("pool not initialized")
	}

	return s, nil
}

func (s *ReceiverSystem) StartErrorProgram() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.handleErrors()
	}()
}

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

func (s *ReceiverSystem) updateFileStatus(fdtID uint8, status uint8, erroeLevel uint8) {
	//TODO:
}

func (s *ReceiverSystem) handleError(err *errs.LeveledError) {
	// 错误处理：记录错误，可能影响单个文件
	// 例如：重试机制或报告给监控系统
	s.updateFileStatus(err.FdtID, 2, uint8(errs.LevelError))
}

func (s *ReceiverSystem) handleWarning(err *errs.LeveledError) {
	s.updateFileStatus(err.FdtID, 2, uint8(errs.LevelWarning))
}

func (s *ReceiverSystem) handleFatalError(err *errs.LeveledError) {
	s.updateFileStatus(err.FdtID, 2, uint8(errs.LevelFatal))
}

func (s *ReceiverSystem) handleDebug(err *errs.LeveledError) {
	s.updateFileStatus(err.FdtID, 2, uint8(errs.LevelDebug))
}

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

func (s *ReceiverSystem) StartMetaProgram() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(s.metaChan)

		metaConns, merr := s.recvPool.InitMetaConn()
		if len(metaConns) == 0 {
			err := fmt.Errorf("Failed to initialize meta connections\n")
			s.reportError(s.ctx, uint8(errs.LevelFatal), err, 0)
			return
		}
		if len(merr) == 1 {
			err := merr[0]
			s.reportError(s.ctx, uint8(errs.LevelFatal), err, 0)
			return
		}

		s.metaConn = metaConns[0]
		log.Printf("Meta receiver listening on %s", s.metaConn.Conn.LocalAddr())

		for {
			select {
			case <-s.ctx.Done():
				return
			default:
			}

			// Read from UDP
			n, _, err := s.metaConn.Conn.ReadFromUDP(s.metaConn.Buffer)
			if err != nil {
				select {
				case <-s.ctx.Done():
					return
				default:
					// Check if error is due to closed connection
					if opErr, ok := err.(*net.OpError); ok && opErr.Err.Error() == "use of closed network connection" {
						return
					}
					log.Printf("Meta read error: %v", err)
					continue
				}
			}

			if n == 0 {
				continue
			}

			// Copy data to avoid buffer race conditions if buffer is reused immediately
			data := make([]byte, n)
			copy(data, s.metaConn.Buffer[:n])

			mt, merr := meta.DeserializeMetaPkt(data)
			if merr != nil {
				log.Printf("Failed to deserialize meta packet: %v", merr)
				continue
			}

			if mt.File == nil {
				log.Printf("Meta packet missing file descriptor, skipping")
				continue
			}

			log.Printf("Received meta packet for FdtID: %d", mt.File.FdtID)
			// mt.ShowPktInfo() // Optional: too verbose?

			select {
			case s.metaChan <- mt:
			case <-s.ctx.Done():
				return
			}
		}
	}()
}

func (s *ReceiverSystem) StartFileProgram() {
	log.Printf("Using %d receiverWorkers", constant.ReceiverWorkers)
	for i := 0; i < constant.ReceiverWorkers; i++ {
		s.wg.Add(1)
		go s.receiverWorker(i)
	}
}

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

func (s *ReceiverSystem) processMeta(mainCtx context.Context, metaPkt *meta.MetaPkt) {
	// Deduplicate based on FdtID
	if _, loaded := s.targets.LoadOrStore(metaPkt.File.FdtID, true); loaded {
		return
	}

	select {
	case s.workerPool <- struct{}{}:
		s.wg.Add(1)
		go func(ctx context.Context, task *meta.MetaPkt) {
			defer s.wg.Done()
			defer func() { <-s.workerPool }()

			atomic.AddInt32(&s.activeReceivers, 1)
			defer atomic.AddInt32(&s.activeReceivers, -1)

			s.runReceiver(ctx, task)
		}(mainCtx, metaPkt)
	}
}

func (s *ReceiverSystem) runReceiver(mainCtx context.Context, task *meta.MetaPkt) {
	fdtID := task.File.FdtID
	ctx := context.WithValue(mainCtx, fdtIDKey, fdtID)
	if task.File.TransferLen > 0 {
		ctx = context.WithValue(ctx, fileSizeKey, task.File.TransferLen)
	}

	// Create a bridge channel to convert receiver.Report to system.FileReport
	recvReportChan := make(chan receiver.Report, 100)
	ctx = receiver.WithReportChan(ctx, recvReportChan)

	// Start a goroutine to forward reports
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

	// Create connections for the file
	conns, _ := s.recvPool.CreateNewFileConnWithBasePort(fdtID, uint8(task.NumPorts), task.BasePort)
	if len(conns) == 0 {
		err := fmt.Errorf("failed to create connections for fdtID %d", fdtID)
		s.reportError(ctx, uint8(errs.LevelError), err, fdtID)
		close(recvReportChan)
		return
	}
	defer s.recvPool.CloseFileConn(fdtID)

	recv, err := receiver.InitReceiver(task)
	if err != nil {
		s.reportError(ctx, uint8(errs.LevelError), err, fdtID)
		close(recvReportChan)
		return
	}
	recv.ShowBasicInfo()

	// Set OnComplete to close the report channel
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

	if err := recv.Start(ctx); err != nil {
		s.reportError(ctx, uint8(errs.LevelError), err, fdtID)
		// Ensure channel is closed on error if OnComplete wasn't called
		select {
		case <-recvReportChan:
		default:
			close(recvReportChan)
		}
	}
}
