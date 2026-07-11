package constant

// UDP Param
const (
	MAX_PACKET_SIZE = 1048 // LCT header (24) + SymbolSize (1024)
	BASE_FILE_PORT  = 3400
	NUM_PORTS       = 1
)


// Sender param
const (
	SendFileDir_unix         = "cmd/send_files/"
	SaveFileDir_unix         = "Downloads/"
	SendFileDir_win          = "cmd\\send_files\\"
	SaveFileDir_win          = "Downloads\\"
	SendRedundancyRatio      = 1.15
	DefaultSendRateLimitMbps = 0 // default send rate limit; 0 disables throttling
	WindowsSize              = 20  // 符号级交织窗口大小。增大此值可更充分地利用并行 worker，提高发送管道利用率

	START_SEND_WAIT = 2 // seconds to wait before starting to send data (给接收端足够时间创建 Receiver)
)

// Arp param
const (
	DestIP          = "192.168.0.10"
)

// Multicast param
const (
	MulticastAddr = "239.1.1.1"
	MulticastTTL  = 2
)

const (
	RsWithSSE2                   = true
	RsWithSSSE3                  = true
	RsWithAVX2                   = true
	RsWithAVX512                 = true
	RsWithAVXGFNI                = true
	RsWithGFNI                   = true
	RsWithConcurrentStreamReads  = true
	RsWithConcurrentStreamWrites = true
	RsWithConcurrentStreams      = true
	RsWithInversionCache         = true

	RsTmpSendOutDir = "../tmp/rs_send/"
	RsTmpRecvInDir  = "../tmp/rs_recv/"
)

// system param
const (
	ReceiverWorkers = 5
)

// Oti param
const (
	MaxNoCodeChunkSize  = 256 // NoCode 模式下每个 source block 最多 256 个 symbol (256KB)
	MaxRaptorQChunkSize = 256 // RaptorQ 模式下每个 source block 最多 256 个 symbol (256KB)
)

const (
	TX_BUF = 32 * 1024 * 1024 // 32 MB send buffer (compatible with macOS kern.ipc.maxsockbuf=32MB, Windows=64MB)
	RX_BUF = 32 * 1024 * 1024 // 32 MB recv buffer (compatible with macOS kern.ipc.maxsockbuf=32MB, Windows=64MB)
)

const (
	CONN_TIMEOUT = 1500 // in seconds
)

const (
	HEALTH_CHECK_INTERVAL      = 10 // in seconds
	IDLE_SENDER_CHECK_INTERVAL = 5  // in seconds
	IDLE_SENDER_TIMEOUT        = 60 // in seconds
	IDLE_DATA_TIMEOUT          = 30 // in seconds，接收端无数据超时（大文件尾部解码需要时间）

	META_PORT = 3400
	POOL_SEND = 0
	POOL_RECV = 1

	META_BUF     = 65535 // 接收缓冲区，需容纳 LCT 头部(24) + 最大符号数据
	META_TIMEOUT = 120 // in seconds
)

// FDT (File Delivery Table) 相关常量
const (
	FDT_EXPIRES = 4294967295 // FDT 过期时间 (Unix 时间戳)，默认永不过期
)

// Receiver param rs
const (
	DefaultChunkSize = 256 // 默认每个 source block 包含 256 个 symbol
)