package fec

import (
	"testing"
)

func TestEncoder(t *testing.T) {
	// 原始数据
	data := []byte{1, 2, 3, 4, 5}

	// 创建编码器
	encoder, err := NewRSGalois8Codec(2, 3, 4)
	if err != nil {
		t.Fatalf("failed to create encoder: %v", err)
	}

	// 编码数据
	shards, err := encoder.Encode(data)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	if len(shards) == 0 {
		t.Errorf("expected non-empty shards, got %d", len(shards))
	}
}
