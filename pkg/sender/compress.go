package sender

import (
	"Flute_go/pkg/lct"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
)

// CompressBuffer 压缩内存数据
func CompressBuffer(data []byte, cenc lct.Cenc) ([]byte, error) {
	switch cenc {
	case lct.CencNull:
		return nil, errors.New("Null compression ?")
	case lct.CencZlib:
		return compressZlib(data)
	case lct.CencDeflate:
		return compressDeflate(data)
	case lct.CencGzip:
		return compressGzip(data)
	default:
		return nil, errors.New("unsupported compression type")
	}
}

// CompressStream 压缩流数据
func CompressStream(input io.Reader, cenc lct.Cenc, output io.Writer) error {
	switch cenc {
	case lct.CencNull:
		return errors.New("Null compression ?")
	case lct.CencZlib:
		return streamCompressZlib(input, output)
	case lct.CencDeflate:
		return streamCompressDeflate(input, output)
	case lct.CencGzip:
		return streamCompressGzip(input, output)
	default:
		return errors.New("unsupported compression type")
	}
}

// 内部实现：Buffer 压缩

func compressGzip(data []byte) ([]byte, error) {
	var buf []byte
	b := NewBufferWriter(&buf)

	w := gzip.NewWriter(b)
	_, err := w.Write(data)
	if err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf, nil
}

func compressDeflate(data []byte) ([]byte, error) {
	var buf []byte
	b := NewBufferWriter(&buf)

	w, err := flate.NewWriter(b, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}
	_, err = w.Write(data)
	if err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf, nil
}

func compressZlib(data []byte) ([]byte, error) {
	var buf []byte
	b := NewBufferWriter(&buf)

	w := zlib.NewWriter(b)
	_, err := w.Write(data)
	if err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf, nil
}

// 内部实现：Stream 压缩

func streamCompressGzip(input io.Reader, output io.Writer) error {
	w := gzip.NewWriter(output)
	defer w.Close()
	_, err := io.Copy(w, input)
	return err
}

func streamCompressZlib(input io.Reader, output io.Writer) error {
	w := zlib.NewWriter(output)
	defer w.Close()
	_, err := io.Copy(w, input)
	return err
}

func streamCompressDeflate(input io.Reader, output io.Writer) error {
	w, err := flate.NewWriter(output, flate.DefaultCompression)
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = io.Copy(w, input)
	return err
}

// 小工具：把 []byte 当作 Writer

type BufferWriter struct {
	buf *[]byte
}

func NewBufferWriter(buf *[]byte) *BufferWriter {
	return &BufferWriter{buf: buf}
}

func (b *BufferWriter) Write(p []byte) (int, error) {
	*b.buf = append(*b.buf, p...)
	return len(p), nil
}
