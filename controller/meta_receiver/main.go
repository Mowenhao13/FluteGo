package main

import (
	"FluteGo/pkg/meta"
	"FluteGo/pkg/pool"
	"log"
	"time"
)

const (
	sourceIP = "192.168.1.103"
	port     = 3399
)

func main() {
	pool.InitGlobalConnectionPool(20, 100*time.Second, 1)
	globalPool := pool.GetGlobalPool()
	if globalPool == nil {
		log.Panic("Pool not initizalized\n")
	}

	Conn, err := globalPool.GetGlobalConnection(sourceIP, port)
	if err != nil {
		log.Panic("Failed to get the connection\n")
	}
	defer globalPool.ReturnConnection(Conn)
	conn := Conn.Conn

	for {
		n, _, err := conn.ReadFromUDP(Conn.Buffer)
		if err != nil {
			log.Printf("Error: ", err)
		}

		pktData := Conn.Buffer[:n]
		metaPkt, err := meta.DeserializeMetaPkt(pktData)
		if err != nil {
			log.Printf("Failed to deserialize MetaPkt: %v", err)
			continue
		}
		metaPkt.ShowPktInfo()
		break
	}

}
