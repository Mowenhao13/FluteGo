package test

import (
	"FluteGo/pkg/filedesc"
	"FluteGo/pkg/meta"
	"FluteGo/pkg/oti"
	"encoding/binary"
	"fmt"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
)

type MetaPktLayer struct {
	layers.BaseLayer
	File             filedesc.FileDesc
	Oti              oti.Oti
	BasePort         int
	NumPorts         uint16
	MaxPacketSize    uint16
	TotalFiles       uint16
	CurrentFileIndex uint16
}

var LayerTypeMetaPkt = newMetaLayer()
var LayerTypeSymbol = newSymLayer()

func (m *MetaPktLayer) LayerType() gopacket.LayerType {
	return LayerTypeMetaPkt
}

func (m *MetaPktLayer) CanDecode() gopacket.LayerClass {
	return LayerTypeMetaPkt
}

func (m *MetaPktLayer) NextLayerType() gopacket.LayerType {
	return gopacket.LayerTypePayload
}

func newMetaLayer() gopacket.LayerType {
	layerType := gopacket.RegisterLayerType(1001, gopacket.LayerTypeMetadata{
		Name:    "MetaPktLayer",
		Decoder: gopacket.DecodeFunc(decodeMeta),
	})

	return layerType
}

func decodeMeta(data []byte, p gopacket.PacketBuilder) error {
	// 使用你的 DeserializeMetaPkt 逻辑解析数据
	mt, err := meta.DeserializeMetaPkt(data)
	if err != nil {
		return err
	}

	// 创建 MetaPktLayer 实例
	layer := &MetaPktLayer{
		File:             *mt.File,
		Oti:              mt.Oti,
		BasePort:         mt.BasePort,
		NumPorts:         mt.NumPorts,
		MaxPacketSize:    mt.MaxPacketSize,
		TotalFiles:       mt.TotalFiles,
		CurrentFileIndex: mt.CurrentFileIndex,
	}

	// 设置 BaseLayer 的 Contents 和 Payload
	layer.Contents = data
	layer.Payload = nil // MetaPktLayer 通常是最后一层

	// 将层添加到数据包构建器
	p.AddLayer(layer)

	// 返回下一个解码器（通常是 Payload）
	return p.NextDecoder(gopacket.LayerTypePayload)
}

type SymbolLayer struct {
	layers.BaseLayer
	SymbolData []byte
	SeqNum     uint64
}

func (s *SymbolLayer) LayerType() gopacket.LayerType {
	return LayerTypeSymbol
}

func (s *SymbolLayer) LayerContents() []byte {
	contents := make([]byte, 8)
	binary.BigEndian.PutUint64(contents, s.SeqNum)
	return contents
}

func (s *SymbolLayer) LayerPayload() []byte {
	return s.SymbolData
}

func newSymLayer() gopacket.LayerType {
	layerType := gopacket.RegisterLayerType(1002, gopacket.LayerTypeMetadata{
		Name:    "SymbolLayer",
		Decoder: gopacket.DecodeFunc(decodeSymbol),
	})
	return layerType
}

func decodeSymbol(data []byte, p gopacket.PacketBuilder) error {
	if len(data) < 8 {
		return fmt.Errorf("Symbol layer too short: %d bytes", len(data))
	}

	seqNum := binary.BigEndian.Uint64(data[:8])
	layer := &SymbolLayer{
		SeqNum:     seqNum,
		SymbolData: data[8:],
	}

	// 设置 BaseLayer
	layer.Contents = data[:8]
	layer.Payload = data[8:]

	p.AddLayer(layer)
	return p.NextDecoder(gopacket.LayerTypePayload)
}
