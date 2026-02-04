package test

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/puzpuzpuz/xsync/v3"
)

// 模拟 Receiver 中的 WriteRequest
type WriteRequest struct {
	FdtID    uint8
	Data     []byte
	Offset   int64
	ChunkIdx uint32
}

const (
	// 队列大小
	QueueSize    = 32768
	NumConsumers = 8 // 模拟 writer worker 数量 (假设8核)
)

// BenchmarkNativeChannel_Throughput 测试原生 Channel 吞吐量
func BenchmarkNativeChannel_Throughput(b *testing.B) {
	ch := make(chan *WriteRequest, QueueSize)
	var wg sync.WaitGroup

	// 预分配对象
	req := &WriteRequest{
		FdtID:    1,
		Data:     make([]byte, 1024),
		Offset:   0,
		ChunkIdx: 0,
	}

	// 启动消费者
	wg.Add(NumConsumers)
	for c := 0; c < NumConsumers; c++ {
		go func() {
			defer wg.Done()
			for range ch {
				// 模拟消费
			}
		}()
	}

	b.ResetTimer()

	// 并发发送 b.N 个消息
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ch <- req
		}
	})

	close(ch)
	wg.Wait()
}

// BenchmarkXsync_Throughput 测试 xsync MPMCQueue 吞吐量
func BenchmarkXsync_Throughput(b *testing.B) {
	q := xsync.NewMPMCQueueOf[*WriteRequest](QueueSize)
	var wg sync.WaitGroup

	req := &WriteRequest{
		FdtID:    1,
		Data:     make([]byte, 1024),
		Offset:   0,
		ChunkIdx: 0,
	}

	var stopped int32

	// 启动消费者
	wg.Add(NumConsumers)
	for c := 0; c < NumConsumers; c++ {
		go func() {
			defer wg.Done()
			for {
				_, ok := q.TryDequeue()
				if ok {
					continue
				}
				// 队列为空，检查是否停止
				if atomic.LoadInt32(&stopped) == 1 {
					// 再次尝试，确认真的为空
					_, ok = q.TryDequeue()
					if !ok {
						return
					}
				}
				runtime.Gosched()
			}
		}()
	}

	b.ResetTimer()

	// 并发发送 b.N 个消息
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			for {
				ok := q.TryEnqueue(req)
				if ok {
					break
				}
				runtime.Gosched()
			}
		}
	})

	atomic.StoreInt32(&stopped, 1)
	wg.Wait()
}
