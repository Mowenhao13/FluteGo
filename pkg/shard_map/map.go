package shard_map

import (
	"sync"
)

// ShardedMap is a high-performance concurrent Map optimized for write-intensive scenarios.
// It reduces lock contention by sharding data across multiple buckets.
const shardCount = 256 // Must be a power of 2

type ShardedMap struct {
	shards []*shard
}

type shard struct {
	mu    sync.RWMutex
	items map[uint32]interface{}
}

func NewShardedMap() *ShardedMap {
	sm := &ShardedMap{
		shards: make([]*shard, shardCount),
	}
	for i := 0; i < shardCount; i++ {
		sm.shards[i] = &shard{
			items: make(map[uint32]interface{}),
		}
	}
	return sm
}

func (m *ShardedMap) getShard(key uint32) *shard {
	return m.shards[key%shardCount]
}

// LoadOrStore returns the existing value for the key if present.
// Otherwise, it stores and returns the given value.
// The loaded result is true if the value was loaded, false if stored.
func (m *ShardedMap) LoadOrStore(key uint32, value interface{}) (interface{}, bool) {
	shard := m.getShard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	if v, ok := shard.items[key]; ok {
		return v, true
	}
	shard.items[key] = value
	return value, false
}

// Load returns the value stored in the map for a key, or nil if no
// value is present.
// The ok result indicates whether value was found in the map.
func (m *ShardedMap) Load(key uint32) (interface{}, bool) {
	shard := m.getShard(key)
	shard.mu.RLock()
	defer shard.mu.RUnlock()

	val, ok := shard.items[key]
	return val, ok
}

// Store sets the value for a key.
func (m *ShardedMap) Store(key uint32, value interface{}) {
	shard := m.getShard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	shard.items[key] = value
}

// Delete deletes the value for a key.
func (m *ShardedMap) Delete(key uint32) {
	shard := m.getShard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	delete(shard.items, key)
}

// Count returns the number of items in the map.
func (m *ShardedMap) Count() int {
	count := 0
	for i := 0; i < shardCount; i++ {
		m.shards[i].mu.RLock()
		count += len(m.shards[i].items)
		m.shards[i].mu.RUnlock()
	}
	return count
}

// Range calls f for each key and value present in the map.
// If f returns false, range stops the iteration.
func (m *ShardedMap) Range(f func(key uint32, value interface{}) bool) {
	for i := 0; i < shardCount; i++ {
		shard := m.shards[i]
		shard.mu.RLock()
		for k, v := range shard.items {
			if !f(k, v) {
				shard.mu.RUnlock()
				return
			}
		}
		shard.mu.RUnlock()
	}
}
