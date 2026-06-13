package constant

// UDP Param
const (
	MAX_PACKET_SIZE = 1408
	BASE_FILE_PORT  = 3400
	NUM_PORTS       = 1
)

// Receiver param
const (
	DefaultChunkSize = 64 * 1024 // 64KB chunks
)

// Sender param
const (
	SendFileDir_unix         = "cmd/send_files/"
	SaveFileDir_unix         = "cmd/received_files/"
	SendFileDir_win          = "cmd\\send_files\\"
	SaveFileDir_win          = "cmd\\received_files\\"
	SendRedundancyRatio      = 1.15
	DefaultSendRateLimitMbps = 1200 // default send rate limit; 0 disables throttling
	WindowsSize              = 10

	START_SEND_WAIT = 1 // seconds to wait before starting to send data
)

// Arp param
const (
	DestIP          = "192.168.0.12"
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
	MaxNoCodeChunkSize  = 32 * 1024
	MaxRaptorQChunkSize = 32 * 1024
)

const (
	TX_BUF = 64 * 1024 * 1024  // 64 MB send buffer (reduced for Windows compatibility)
	RX_BUF = 64 * 1024 * 1024 // 16 MB recv buffer (reduced for Windows compatibility)
)

const (
	CONN_TIMEOUT = 1500 // in seconds
)

const (
	HEALTH_CHECK_INTERVAL      = 10 // in seconds
	IDLE_SENDER_CHECK_INTERVAL = 5  // in seconds
	IDLE_SENDER_TIMEOUT        = 60 // in seconds
	IDLE_DATA_TIMEOUT          = 5 // in seconds，接收端无数据超时

	META_PORT = 3399
	POOL_SEND = 0
	POOL_RECV = 1

	META_BUF     = 1500
	META_TIMEOUT = 120 // in seconds
)
