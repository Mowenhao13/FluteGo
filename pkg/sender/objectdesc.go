package sender

import (
	"Flute_go/pkg/lct"
	"Flute_go/pkg/object"
	"Flute_go/pkg/oti"
	"Flute_go/pkg/tools"

	"bufio"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type CacheControlChoice int

const (
	CacheNoCache CacheControlChoice = iota
	CacheMaxStale
	CacheExpires
	CacheExpireAt
)

type CacheControl struct {
	Choice   CacheControlChoice
	Duration time.Duration // 当 Choice==CacheExpires
	At       time.Time     // 当 Choice==CacheExpiresAt（零值表示未设）
}

func ptr[T any](v T) *T { return &v }

func CreateFdtCacheControl(cc *CacheControl, now time.Time) object.CacheControl {
	switch cc.Choice {
	case CacheNoCache:
		return object.CacheControl{
			Value: object.CacheControlChoice{
				NoCache:  ptr(true),
				MaxStale: nil,
				Expires:  nil,
			},
		}
	case CacheMaxStale:
		return object.CacheControl{
			Value: object.CacheControlChoice{
				NoCache:  nil,
				MaxStale: ptr(true),
				Expires:  nil,
			},
		}
	case CacheExpires:
		expires := now.Add(cc.Duration)
		ntp, err := tools.SystemTimeToNTP(expires) // (uint64, nil on error) -> 这里按你实现返回值改
		var hi uint32
		if ntp != 0 && err == nil {
			hi = uint32(ntp >> 32)
		}
		return object.CacheControl{
			Value: object.CacheControlChoice{
				Expires: &hi,
			},
		}
	case CacheExpireAt:
		ntp, err := tools.SystemTimeToNTP(cc.At)
		var hi uint32
		if ntp != 0 && err == nil {
			hi = uint32(ntp >> 32)
		}
		return object.CacheControl{
			Value: object.CacheControlChoice{
				Expires: &hi,
			},
		}
	default:
		return object.CacheControl{}
	}
}

/// Target Acquisition for Object

type TargetAcquisitionChoice int

const (
	AsFastAsPossible TargetAcquisitionChoice = iota
	WithinDuration
	WithinTime
)

type TargetAcquisition struct {
	Choice   TargetAcquisitionChoice
	Duration time.Duration // Choice == WithinDuration
	At       time.Time     // Choice == WithinTime
}

// 线程安全由上层保证
type ObjectDataStream = io.ReadSeeker

// Md5Base64: 计算 io.ReadSeeker 的 MD5 并返回 base64 编码
func Md5Base64(rs io.ReadSeeker) (string, error) {
	sum, err := md5Digest(rs)
	if err != nil {
		return "", err
	}
	// RFC 2616 Content-MD5 使用 base64
	return base64.StdEncoding.EncodeToString(sum), nil
}

// md5Digest: 计算 MD5 原始字节
func md5Digest(rs io.ReadSeeker) ([]byte, error) {
	// 定位到开头
	_, err := rs.Seek(0, io.SeekStart)
	if err != nil {
		return nil, err
	}

	reader := bufio.NewReader(rs)
	h := md5.New()
	buf := make([]byte, 102400)

	for {
		n, rerr := reader.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}

	// 再 seek 回文件开头
	_, err = rs.Seek(0, io.SeekStart)
	if err != nil {
		return nil, err
	}

	return h.Sum(nil), nil
}

type ObjectDataSourceChoice int

const (
	DataStream ObjectDataSourceChoice = iota
	DataBuffer
)

type ObjectDataSource struct {
	Choice ObjectDataSourceChoice

	/// Source from a stream
	streamMu sync.Mutex
	stream   ObjectDataStream

	/// Source from a buffer
	buffer []byte
}

// FromBuffer: 创建自 buffer
func ObjectDataSourceFromBuffer(buf []byte, cenc lct.Cenc) (ObjectDataSource, error) {
	var data []byte
	switch cenc {
	case lct.CencNull:
		data = append([]byte(nil), buf...) // 拷贝一份
	default:
		var err error
		data, err = CompressBuffer(buf, cenc)
		if err != nil {
			return ObjectDataSource{}, err
		}
	}
	return ObjectDataSource{
		Choice: DataBuffer,
		buffer: data,
	}, nil
}

// FromVec: 创建自 vec
func ObjectDataSourceFromVec(buf []byte, cenc lct.Cenc) (ObjectDataSource, error) {
	return ObjectDataSourceFromBuffer(buf, cenc)
}

// FromStream: 创建自 stream
func ObjectDataSourceFromStream(rs ObjectDataStream) ObjectDataSource {
	return ObjectDataSource{
		Choice: DataStream,
		stream: rs,
	}
}

// Len: 获取数据长度
func (o *ObjectDataSource) Len() (uint64, error) {
	switch o.Choice {
	case DataBuffer:
		return uint64(len(o.buffer)), nil
	case DataStream:
		o.streamMu.Lock()
		defer o.streamMu.Unlock()

		// 保存当前位置
		cur, err := o.stream.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, err
		}

		// 移动到末尾
		end, err := o.stream.Seek(0, io.SeekEnd)
		if err != nil {
			return 0, err
		}

		// 还原位置
		_, err = o.stream.Seek(cur, io.SeekStart)
		if err != nil {
			return 0, err
		}

		return uint64(end), nil
	default:
		return 0, errors.New("unknown ObjectDataSource choice")
	}
}

type CarouselRepeatModeChoice int

const (
	DelayBetweenTransfers CarouselRepeatModeChoice = iota
	IntervalBetweenStartTimes
)

type CarouselRepeatMode struct {
	Choice   CarouselRepeatModeChoice
	Interval time.Duration
}

type ObjectDesc struct {
	ContentLocation *url.URL
	Source          ObjectDataSource

	ContentType      string
	ContentLength    uint64
	TransferLength   uint64
	Cenc             lct.Cenc
	InbandCenc       bool
	MD5              *string
	OTI              *oti.Oti
	MaxTransferCount uint32

	TargetAcquisition                     *TargetAcquisition
	CarouselMode                          *CarouselRepeatMode
	TransferStartTime                     *time.Time
	CacheControl                          *CacheControl
	Groups                                []string
	Toi                                   *Toi
	OptelPropagator                       map[string]string
	ETag                                  *string
	AllowImmediateStopBeforeFirstTransfer *bool
}

// SetToi
func (o *ObjectDesc) SetToi(t *Toi) { o.Toi = t }

func CreateFromFile(
	path string,
	contentLocation *url.URL, // 可为 nil
	contentType string,
	cacheInRAM bool,
	maxTransferCount uint32,
	carouselMode *CarouselRepeatMode,
	targetAcquisition *TargetAcquisition,
	cacheControl *CacheControl,
	groups []string,
	cenc lct.Cenc,
	inbandCenc bool,
	otiOver *oti.Oti,
	withMD5 bool,
) (*ObjectDesc, error) {

	cl := contentLocation
	if cl == nil {
		fn := filepath.Base(path)
		u, _ := url.Parse("file:///")
		if fn != "" && fn != "." {
			u, _ = url.Parse("file:///" + fn)
		}
		cl = u
	}

	if cacheInRAM {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return createWithContent(
			content,
			contentType,
			cl,
			maxTransferCount,
			carouselMode,
			targetAcquisition,
			cacheControl,
			groups,
			cenc,
			inbandCenc,
			otiOver,
			withMD5,
		)
	}

	if cenc != lct.CencNull {
		return nil, errors.New("compressed object is not compatible with file path")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return CreateFromStream(
		f,
		contentType,
		cl,
		maxTransferCount,
		carouselMode,
		targetAcquisition,
		cacheControl,
		groups,
		inbandCenc,
		otiOver,
		withMD5,
	)
}

func CreateFromStream(
	stream io.ReadSeeker,
	contentType string,
	contentLocation *url.URL,
	maxTransferCount uint32,
	carouselMode *CarouselRepeatMode,
	targetAcquisition *TargetAcquisition,
	cacheControl *CacheControl,
	groups []string,
	inbandCenc bool,
	otiOver *oti.Oti,
	withMD5 bool,
) (*ObjectDesc, error) {

	var md5b64 *string
	if withMD5 {
		s, err := Md5Base64(stream)
		if err != nil {
			return nil, err
		}
		md5b64 = &s
	}

	src := ObjectDataSourceFromStream(stream)
	transferLen, err := src.Len()
	if err != nil {
		return nil, err
	}

	return &ObjectDesc{
		ContentLocation:                       contentLocation,
		Source:                                src,
		ContentType:                           contentType,
		ContentLength:                         transferLen,
		TransferLength:                        transferLen,
		Cenc:                                  lct.CencNull,
		InbandCenc:                            inbandCenc,
		MD5:                                   md5b64,
		OTI:                                   otiOver,
		MaxTransferCount:                      maxTransferCount,
		CarouselMode:                          carouselMode,
		TargetAcquisition:                     targetAcquisition,
		TransferStartTime:                     nil,
		CacheControl:                          cacheControl,
		Groups:                                groups,
		Toi:                                   nil,
		OptelPropagator:                       nil,
		ETag:                                  nil,
		AllowImmediateStopBeforeFirstTransfer: nil,
	}, nil
}

func CreateFromBuffer(
	content []byte,
	contentType string,
	contentLocation *url.URL,
	maxTransferCount uint32,
	carouselMode *CarouselRepeatMode,
	targetAcquisition *TargetAcquisition,
	cacheControl *CacheControl,
	groups []string,
	cenc lct.Cenc,
	inbandCenc bool,
	otiOver *oti.Oti,
	withMD5 bool,
) (*ObjectDesc, error) {
	return createWithContent(
		content,
		contentType,
		contentLocation,
		maxTransferCount,
		carouselMode,
		targetAcquisition,
		cacheControl,
		groups,
		cenc,
		inbandCenc,
		otiOver,
		withMD5,
	)
}

func createWithContent(
	content []byte,
	contentType string,
	contentLocation *url.URL,
	maxTransferCount uint32,
	carouselMode *CarouselRepeatMode,
	targetAcquisition *TargetAcquisition,
	cacheControl *CacheControl,
	groups []string,
	cenc lct.Cenc,
	inbandCenc bool,
	otiOver *oti.Oti,
	withMD5 bool,
) (*ObjectDesc, error) {

	contentLen := len(content)

	var md5b64 *string
	if withMD5 {
		sum := md5.Sum(content)
		s := base64.StdEncoding.EncodeToString(sum[:])
		md5b64 = &s
	}

	src, err := ObjectDataSourceFromVec(content, cenc)
	if err != nil {
		return nil, err
	}
	transferLen, err := src.Len()
	if err != nil {
		return nil, err
	}

	return &ObjectDesc{
		ContentLocation:                       contentLocation,
		Source:                                src,
		ContentType:                           contentType,
		ContentLength:                         uint64(contentLen),
		TransferLength:                        transferLen,
		Cenc:                                  cenc,
		InbandCenc:                            inbandCenc,
		MD5:                                   md5b64,
		OTI:                                   otiOver,
		MaxTransferCount:                      maxTransferCount,
		CarouselMode:                          carouselMode,
		TargetAcquisition:                     targetAcquisition,
		TransferStartTime:                     nil,
		CacheControl:                          cacheControl,
		Groups:                                groups,
		Toi:                                   nil,
		OptelPropagator:                       nil,
		ETag:                                  nil,
		AllowImmediateStopBeforeFirstTransfer: nil,
	}, nil
}
