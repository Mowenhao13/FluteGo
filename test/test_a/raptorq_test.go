package test

// import (
// 	"os"
// 	"golang.org/x/sys/unix"
// 	"testing"
// 	raptorq "github.com/xssnick/raptorq"
// )

// func TestRaptorQEncode(t *testing.T) {
// 	const (
// 		filePath = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files"
// 		chunkSize = 1 * 1024 * 1024 // 1MB
// 		symbolSize = 1500
// 	)

// 	file, err := os.Open(filePath)
// 	if err != nil {
// 		t.Fatalf("Failed to open file: %v", err)
// 	}
// 	defer file.Close()

// 	fileInfo, err := file.Stat()
// 	if err != nil {
// 		t.Fatalf("Failed to stat file: %v", err)
// 	}

// 	fileSize := fileInfo.Size()
// 	rq := raptorq.NewRaptorQ(uint32(symbolSize))

// 	for i := uint32(0); i < uint32(fileSize); i += chunkSize {
// 		end := i + chunkSize
// 		if end > uint32(fileSize) {
// 			end = uint32(fileSize)
// 		}

// 		data, err := unix.Mmap(int(file.Fd()), int64(i), int(end - i), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
// 		if err != nil {
// 			t.Fatalf("Failed to mmap file: %v", err)
// 		}
// 		defer unix.Munmap(data)

// 		// Initialize RaptorQ encoder
// 		encoder, err := rq.CreateEncoder(data)
// 		if err != nil {
// 			t.Fatalf("Failed to create RaptorQ encoder: %v", err)
// 		}

// 		baseSymbols := encoder.BaseSymbolsNum()
// 		if baseSymbols == 0 {
// 			t.Fatalf("Base symbols is zero")
// 		}

// 		totalSymbols := uint32(float64(baseSymbols) * 1.2) // 20% redundancy
// 		if totalSymbols < baseSymbols {
// 			totalSymbols = baseSymbols
// 		}

// 		// Generate symbols
// 		for j := uint32(0); j < totalSymbols; j++ {
// 			symbol := encoder.GenSymbol(j)
// 			if symbol == nil {
// 				t.Fatalf("Failed to generate symbol %d for chunk starting at %d", j, i)
// 			}
// 			// Here you would normally send the symbol over the network
// 		}

// 		// release reference explicitly to help GC in long-running sessions
// 		encoder = nil
// 	}
// }

// func TestRaptorQDecode(t *testing.T) {

// }
