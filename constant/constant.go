package constant

// UDP Param
const (
	MaxPacketSize = 1400
	BaseFilePort  = 3400
	MetaPort      = 3399
	NumPorts      = 1
)

// Pool param
const (
	MaxConcurrentSends = 1
	MaxMetaConnTimeout = 500 // in seconds
)

// Receiver param
const (
	RecvRedundancyRatio = 1.01
	DefaultChunkSize    = 10 * 1024 // 50KB chunks
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
	DefaultSendRateLimitMbps = 1000 // default send rate limit; 0 disables throttling
	WindowsSize              = 30

	StartSendWait = 3 // seconds to wait before starting to send data
)

// Arp param
const (
	SourceIP        = "192.168.1.102"
	SourceMAC       = "10:7c:61:10:a5:47"
	SourceInterface = "enp3s0"
	DestIP          = "192.168.1.103"
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
