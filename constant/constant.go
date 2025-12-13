package constant

// UDP Param
const (
	MAX_PACKET_SIZE = 1408
	BASE_FILE_PORT  = 3400
	NUM_PORTS       = 1
)

// Receiver param
const (
	RecvRedundancyRatio = 1.01
	DefaultChunkSize    = 32 * 1024 // 32KB chunks
)

// Sender param
const (
	SendFileDir_unix         = "cmd/send_files/"
	SaveFileDir_unix         = "cmd/received_files/"
	SendFileDir_win_t        = "C:\\Users\\mowen\\Desktop\\FluteGo\\FluteGo\\cmd\\send_files\\"
	SaveFileDir_win_t        = "C:\\Users\\mowen\\Desktop\\FluteGo\\FluteGo\\cmd\\received_files\\"
	SendFileDir_win          = "cmd\\send_files\\"
	SaveFileDir_win          = "cmd\\received_files\\"
	SendRedundancyRatio      = 1.05
	DefaultSendRateLimitMbps = 1400 // default send rate limit; 0 disables throttling
	WindowsSize              = 30

	START_SEND_WAIT = 1 // seconds to wait before starting to send data
)

// Arp param
const (
	SourceIP        = "127.0.0.1"
	SourceMAC       = "10:7c:61:10:a5:47"
	SourceInterface = "enp3s0"
	DestIP          = "127.0.0.1"
	DestMAC         = "88:a2:9e:3f:be:2c"
	DestInterface   = "eth0"
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
	TX_BUF = 64 * 1024 * 1024 // 64 MB
	RX_BUF = 64 * 1024 * 1024 // 64 MB
)

const (
	CONN_TIMEOUT = 30 // in seconds
)

const (
	HEALTH_CHECK_INTERVAL      = 10 // in seconds
	IDLE_SENDER_CHECK_INTERVAL = 1  // in seconds
	IDLE_SENDER_TIMEOUT        = 3  // in seconds

	META_PORT = 3399
	POOL_SEND = 0
	POOL_RECV = 1

	META_BUF     = 1500
	META_TIMEOUT = 120 // in seconds
)
