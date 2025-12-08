package test

import (
	pool "FluteGo/pkg/pool"
	"testing"
)

// Starting pprof server on :6060
// === RUN   TestAllocatePorts
//     /home/Halllo/Projects/Flute_test_v2/test/pool_test.go:24: Active conns before: 60
//     /home/Halllo/Projects/Flute_test_v2/test/pool_test.go:34: Active conns now: 0
// --- PASS: TestAllocatePorts (0.00s)
// PASS
// ok      FluteGo/test    0.107s

func TestAllocatePorts(t *testing.T) {
	globalPool := pool.GetGlobalPool()
	const (
		basePort = 3399
		numPorts = 20
		ip = "192.168.1.103"
	)

	for i := 0; i < 3; i++ {
		for j := 0; j < numPorts; j++ {
			port := basePort + i * numPorts + j
			globalPool.GetGlobalConnection(ip, port)
		}
	}

	activeConns := globalPool.GetStats().ActiveConns
	t.Logf("Active conns before: %d", activeConns)

	for i := 0; i < 3; i++ {
		for j := 0; j < numPorts; j++ {
			port := basePort + i * numPorts + j
			globalPool.CloseConnection(ip, port)
		}
	}

	activeConns = globalPool.GetStats().ActiveConns
	t.Logf("Active conns now: %d", activeConns)
}

// Starting pprof server on :6060
// === RUN   TestFilePorts
// Created connection for fdtID(0): 192.168.1.103:3400
// Created connection for fdtID(1): 192.168.1.103:3401
// Created connection for fdtID(2): 192.168.1.103:3402
// Created connection for fdtID(3): 192.168.1.103:3403
// Created connection for fdtID(4): 192.168.1.103:3404
// TotalConns: 5
// ActiveConns: 5
// CreatedConns: 5
// DestoryedConns: 0
// LastPort: 3404
// TotalConns: 0
// ActiveConns: 0
// CreatedConns: 5
// DestoryedConns: 5
// LastPort: 3404
// --- PASS: TestFilePorts (0.00s)
// PASS
// ok      FluteGo/test    0.105s

func TestFilePorts(t *testing.T) {
	globalPool := pool.GetGlobalPool()
	for fdtID := uint8(0); fdtID < uint8(5); fdtID++ {
		_, _, err := globalPool.GetGlobalFileConn(fdtID)
		if err != nil {
			globalPool.CreateNewFileConn(fdtID, 1)
		}
	}

	globalPool.ShowInfo()
	for fdtID := uint8(0); fdtID < uint8(5); fdtID++ {
		globalPool.CloseFileConn(fdtID)
	}
	globalPool.ShowInfo()

}


