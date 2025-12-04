package main

import (
	"FluteGo/constant"
	"FluteGo/pkg/filedesc"
	"FluteGo/pkg/meta"
	"FluteGo/pkg/oti"
	"FluteGo/pkg/pool"
	"log"
	"time"
)

const (
	destIP = "192.168.1.103"
	port = 3400
)

func main() {
	pool.InitGlobalConnectionPool(20, 100*time.Second, 0)
	globalPool := pool.GetGlobalPool()
	if globalPool == nil {
		log.Panic("Pool not initizalized\n")
	}

	Conn, err := globalPool.GetGlobalConnection(destIP, port)
	if err != nil {
		log.Panic("Failed to get the connection\n")
	}
	defer globalPool.ReturnConnection(Conn)
	conn := Conn.Conn

	const (
		fdtID = 1
		sendPath = "/home/Halllo/Projects/Flute_test_v2/cmd/send_files/test_1024mb.bin"
		saveDir = "/home/Halllo/Projects/Flute_test_v2/cmd/received_files"
		name = "test_1024mb.bin"
		transferLen = 1024 * 1024 * 1024
		contentType = "application/octet-stream"
		md5 = "cd573cfaace07e7949bc0c46028904ff"
	)
	fd := filedesc.FileDesc{
		FdtID: fdtID,
		SendPath: sendPath,
		SaveDir: saveDir,
		Name: name,
		TransferLen: transferLen,
		ContentType: contentType,
		Md5: md5,
	}
	oti := oti.NewRaptorQ(1400)
	basePort := 3400
	numPorts := 20
	metaPkt := meta.MetaPkt{
		File: &fd,
		Oti: oti,
		BasePort: basePort,
		NumPorts: uint16(numPorts),
		MaxPacketSize: constant.MaxPacketSize,
	}
	
	pktData := metaPkt.Serialize()
	writeBytes, err := conn.Write(pktData)
	if err != nil {
		log.Printf("Error: %v", err)
	}
	log.Printf("Sent %d bytes\n", writeBytes)


}