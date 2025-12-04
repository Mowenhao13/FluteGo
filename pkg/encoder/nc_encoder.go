package encoder

import (
	"context"
	"fmt"
)

type NcEncoder struct {
	Config   EncoderConfig
	Callback SendCallback
}

func NewNcEncoder(config EncoderConfig) (*NcEncoder, error) {
	return &NcEncoder{
		Config: config,
	}, nil
}

func (e *NcEncoder) Encode(ctx context.Context, chunkCount uint32, provider DataProvider, cb SendCallback) error {
	callback := cb
	if callback == nil {
		callback = e.Callback
	}

	for chunkIdx := 0; chunkIdx < int(chunkCount); chunkIdx++ {
		data, sz, err := provider(uint32(chunkIdx))
		if err != nil {
			return fmt.Errorf("failed to get data for chunk %d: %w", chunkIdx, err)
		}

		for i := 0; i < sz; i += int(e.Config.SymbolSize) {
			start := i
			end := start + int(e.Config.SymbolSize)
			// end should be capped by the current chunk length (sz), not the whole file size
			if end > sz {
				end = sz
			}
			symbol := data[start:end]
			symID := i / int(e.Config.SymbolSize)
			if err := callback(uint32(chunkIdx), uint32(symID), uint32(sz), symbol); err != nil {
				return fmt.Errorf("callback failed for chunk %d symbol %d: %w", chunkIdx, symID, err)
			}
		}
		data = nil
	}

	return nil
}

func (e *NcEncoder) SetCallback(cb SendCallback) {
	e.Callback = cb
}

func (e *NcEncoder) Close() error {
	return nil
}
