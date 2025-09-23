package object

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	"Flute_go/pkg/oti"
	"Flute_go/pkg/tools" // 你需要提供 NTP<->time 的转换：NTP(64-bit) -> time.Time
)

// -------- XML 模型 --------

// 顶层 FDT
type FdtInstance struct {
	XMLName xml.Name `xml:"FDT-Instance"`

	// 命名空间属性（可选，通常无需关心；若需要序列化默认值，可在 Marshal 时补）
	XMLNS         *string `xml:"xmlns,attr,omitempty"`
	XMLNSXSI      *string `xml:"xmlns:xsi,attr,omitempty"`
	XMLNSMBMS2005 *string `xml:"xmlns:mbms2005,attr,omitempty"`
	XMLNSMBMS2007 *string `xml:"xmlns:mbms2007,attr,omitempty"`
	XMLNSMBMS2008 *string `xml:"xmlns:mbms2008,attr,omitempty"`
	XMLNSMBMS2009 *string `xml:"xmlns:mbms2009,attr,omitempty"`
	XMLNSMBMS2012 *string `xml:"xmlns:mbms2012,attr,omitempty"`
	XMLNSMBMS2015 *string `xml:"xmlns:mbms2015,attr,omitempty"`
	XMLNSSV       *string `xml:"xmlns:sv,attr,omitempty"`

	// 必要属性
	Expires         string  `xml:"Expires,attr"` // NTP 高 32 位十进制字符串
	Complete        *bool   `xml:"Complete,attr,omitempty"`
	ContentType     *string `xml:"Content-Type,attr,omitempty"`
	ContentEncoding *string `xml:"Content-Encoding,attr,omitempty"`

	// 顶层 FEC OTI
	FECEncID      *uint8  `xml:"FEC-OTI-FEC-Encoding-ID,attr,omitempty"`
	FECInstanceID *uint64 `xml:"FEC-OTI-FEC-Instance-ID,attr,omitempty"`
	FECMaxSBL     *uint64 `xml:"FEC-OTI-Maximum-Source-Block-Length,attr,omitempty"`
	FECESL        *uint64 `xml:"FEC-OTI-Encoding-Symbol-Length,attr,omitempty"`
	FECMaxN       *uint64 `xml:"FEC-OTI-Max-Number-of-Encoding-Symbols,attr,omitempty"`
	FECSchemeInfo *string `xml:"FEC-OTI-Scheme-Specific-Info,attr,omitempty"` // Base64

	// 文件列表
	Files []FdtFile `xml:"File"`

	// 其他可选（示例）
	SchemaVersion *uint32  `xml:"sv:schemaVersion,omitempty"`
	BaseURL1      []string `xml:"mbms2012:Base-URL-1,omitempty"`
	BaseURL2      []string `xml:"mbms2012:Base-URL-2,omitempty"`
}

// 单个文件项
type FdtFile struct {
	// 子元素
	CacheControl *CacheControl `xml:"mbms2007:Cache-Control"`

	// 标识
	ContentLocation string  `xml:"Content-Location,attr"`
	TOI             string  `xml:"TOI,attr"`
	ContentLength   *uint64 `xml:"Content-Length,attr,omitempty"`
	TransferLength  *uint64 `xml:"Transfer-Length,attr,omitempty"`

	// 内容类型
	ContentType     *string `xml:"Content-Type,attr,omitempty"`
	ContentEncoding *string `xml:"Content-Encoding,attr,omitempty"`
	ContentMD5      *string `xml:"Content-MD5,attr,omitempty"`

	// 文件级 FEC OTI
	FECEncID      *uint8  `xml:"FEC-OTI-FEC-Encoding-ID,attr,omitempty"`
	FECInstanceID *uint64 `xml:"FEC-OTI-FEC-Instance-ID,attr,omitempty"`
	FECMaxSBL     *uint64 `xml:"FEC-OTI-Maximum-Source-Block-Length,attr,omitempty"`
	FECESL        *uint64 `xml:"FEC-OTI-Encoding-Symbol-Length,attr,omitempty"`
	FECMaxN       *uint64 `xml:"FEC-OTI-Max-Number-of-Encoding-Symbols,attr,omitempty"`
	FECSchemeInfo *string `xml:"FEC-OTI-Scheme-Specific-Info,attr,omitempty"` // Base64
}

type CacheControlChoice struct {
	// 这几个字段互斥，仅会设置其中之一。
	// XML tag 里加上 name / alias
	NoCache  *bool   `xml:"mbms2007:no-cache,omitempty" json:"no-cache,omitempty"`
	MaxStale *bool   `xml:"mbms2007:max-stale,omitempty" json:"max-stale,omitempty"`
	Expires  *uint32 `xml:"mbms2007:Expires,omitempty"  json:"Expires,omitempty"`
}
type CacheControl struct {
	Value CacheControlChoice `xml:",any"` // 把子元素作为 Value
}

type ObjectCacheControl interface{ isCacheCtl() }

type ObjectCacheControlNoCacheT struct{}

func (ObjectCacheControlNoCacheT) isCacheCtl() {}

var ObjectCacheControlNoCache ObjectCacheControlNoCacheT

type ObjectCacheControlMaxStaleT struct{}

func (ObjectCacheControlMaxStaleT) isCacheCtl() {}

var ObjectCacheControlMaxStale ObjectCacheControlMaxStaleT

type ObjectCacheControlExpiresAt struct{ Time time.Time }

func (ObjectCacheControlExpiresAt) isCacheCtl() {}

type ObjectCacheControlExpiresAtHint struct{ Time time.Time }

func (ObjectCacheControlExpiresAtHint) isCacheCtl() {}

// 自定义 XML 反序列化（简单实现，只解析我们关心的三类标签）
func (c *CacheControl) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type anyElem struct {
		XMLName xml.Name
		Value   string `xml:",chardata"`
	}
	var elems []anyElem

	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch tt := tok.(type) {
		case xml.StartElement:
			var e anyElem
			if err := d.DecodeElement(&e, &tt); err != nil {
				return err
			}
			elems = append(elems, e)
		case xml.EndElement:
			if tt.Name.Local == start.Name.Local && tt.Name.Space == start.Name.Space {
				goto DONE
			}
		}
	}
DONE:
	for _, e := range elems {
		switch e.XMLName.Local {
		case "no-cache":
			b := true
			c.Value.NoCache = &b
		case "max-stale":
			b := true
			c.Value.MaxStale = &b
		case "Expires":
			if v, err := strconv.ParseUint(e.Value, 10, 32); err == nil {
				u := uint32(v)
				c.Value.Expires = &u
			}
		}
	}
	return nil
}

// Parse 从 XML 字节解析 FdtInstance
func ParseFdtInstance(buf []byte) (FdtInstance, error) {
	var inst FdtInstance
	if err := xml.Unmarshal(buf, &inst); err != nil {
		return FdtInstance{}, fmt.Errorf("parse FDT failed: %w", err)
	}
	return inst, nil
}

// GetExpirationDate 把 Expires(高32秒) 转成 time.Time
func (f FdtInstance) GetExpirationDate() *time.Time {
	sec, err := strconv.ParseUint(f.Expires, 10, 32)
	if err != nil {
		return nil
	}
	// Rust 做法：sec << 32 形成 64-bit NTP，调用 tools::ntp_to_system_time
	ntp := uint64(sec) << 32
	tm, err := tools.NTPToSystemTime(ntp) // 需要你提供此函数
	if err != nil {
		return nil
	}
	return &tm
}

// GetFile 根据 TOI 查找文件（传入 toi 十进制字符串或先把 u128 → 十进制）
func (f FdtInstance) GetFile(toiStr string) *FdtFile {
	for i := range f.Files {
		if f.Files[i].TOI == toiStr {
			return &f.Files[i]
		}
	}
	return nil
}

// GetOtiForFile：优先文件级 OTI，否者回退顶层
func (f FdtInstance) GetOtiForFile(file *FdtFile) *oti.Oti {
	if o := file.GetOti(); o != nil {
		return o
	}
	return f.GetOti()
}

// GetOti 顶层 OTI
func (f FdtInstance) GetOti() *oti.Oti {
	if f.FECEncID == nil || f.FECMaxSBL == nil || f.FECESL == nil {
		return nil
	}
	enc := oti.FECEncodingID(*f.FECEncID)

	var scheme any
	switch enc {
	case oti.ReedSolomonGF2M:
		scheme = decodeRS2m(f.FECSchemeInfo)
	default:
		scheme = nil
	}

	maxN := f.FECMaxN
	if maxN == nil {
		maxN = f.FECMaxSBL
	}
	parity := uint32(0)
	if maxN != nil && *maxN >= *f.FECMaxSBL {
		parity = uint32(*maxN - *f.FECMaxSBL)
	}

	o := &oti.Oti{
		FecEncodingID: enc,
		FecInstanceID: func() uint16 {
			if f.FECInstanceID != nil {
				return uint16(*f.FECInstanceID)
			}
			return 0
		}(),
		MaximumSourceBlockLength: uint32(*f.FECMaxSBL),
		EncodingSymbolLength:     uint16(*f.FECESL),
		MaxNumberOfParitySymbols: parity,
		InBandFti:                false,
	}
	// 如果你有明确的 scheme-specific 类型字段，在这里赋值
	switch v := scheme.(type) {
	case *oti.ReedSolomonGF2MSchemeSpecific:
		o.ReedSolomonGF2MSchemeSpecific = v
	}
	return o
}

// ---- 文件上的方法 ----

func (f *FdtFile) GetObjectCacheControl(fdtExp *time.Time) ObjectCacheControl {
	// 1) 优先看 Cache-Control
	if f.CacheControl != nil {
		if f.CacheControl.Value.NoCache != nil && *f.CacheControl.Value.NoCache {
			return ObjectCacheControlNoCache
		}
		if f.CacheControl.Value.MaxStale != nil && *f.CacheControl.Value.MaxStale {
			return ObjectCacheControlMaxStale
		}
		if f.CacheControl.Value.Expires != nil {
			ntp := uint64(*f.CacheControl.Value.Expires) << 32
			if tm, err := tools.NTPToSystemTime(ntp); err == nil {
				return ObjectCacheControlExpiresAt{Time: tm}
			}
		}
	}
	// 2) 退化为 FDT 实例的过期时间（Hint）
	if fdtExp != nil {
		return ObjectCacheControlExpiresAtHint{Time: *fdtExp}
	}
	// 3) 最后 NoCache
	return ObjectCacheControlNoCache
}

func (f *FdtFile) GetTransferLength() uint64 {
	if f.TransferLength != nil {
		return *f.TransferLength
	}
	if f.ContentLength != nil {
		return *f.ContentLength
	}
	return 0
}

func (f *FdtFile) GetOti() *oti.Oti {
	if f.FECEncID == nil || f.FECMaxSBL == nil || f.FECESL == nil {
		return nil
	}
	enc := oti.FECEncodingID(*f.FECEncID)

	var scheme any
	switch enc {
	case oti.ReedSolomonGF2M:
		scheme = decodeRS2m(f.FECSchemeInfo)
	default:
		scheme = nil
	}

	maxN := f.FECMaxN
	if maxN == nil {
		maxN = f.FECMaxSBL
	}
	parity := uint32(0)
	if maxN != nil && *maxN >= *f.FECMaxSBL {
		parity = uint32(*maxN - *f.FECMaxSBL)
	}

	o := &oti.Oti{
		FecEncodingID: enc,
		FecInstanceID: func() uint16 {
			if f.FECInstanceID != nil {
				return uint16(*f.FECInstanceID)
			}
			return 0
		}(),
		MaximumSourceBlockLength: uint32(*f.FECMaxSBL),
		EncodingSymbolLength:     uint16(*f.FECESL),
		MaxNumberOfParitySymbols: parity,
		InBandFti:                false,
	}
	switch v := scheme.(type) {
	case *oti.ReedSolomonGF2MSchemeSpecific:
		o.ReedSolomonGF2MSchemeSpecific = v
	}
	return o
}

// 尝试解析 Base64 后的内容为 RS(2^m) 的 scheme-specific (M,G)
// 兼容多种线下落地格式：
// 1) 原始2字节: [M,G]
// 2) 纯文本CSV: "8,1" / "8|1" / "8 1"
// 3) kv 文本:   "m=8;g=1" / "M=8,G=1" / "m:8 g:1"
// 4) JSON:      {"m":8,"g":1} 或 {"M":8,"G":1}
func decodeRS2m(b64 *string) *oti.ReedSolomonGF2MSchemeSpecific {
	if b64 == nil {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(*b64)
	if err != nil {
		return nil
	}

	// 1) 原始2字节 [M,G]
	if len(raw) == 2 {
		m := raw[0]
		g := raw[1]
		if m == 0 {
			m = 8
		}
		if g == 0 {
			g = 1
		}
		return &oti.ReedSolomonGF2MSchemeSpecific{M: m, G: g}
	}

	// 2) JSON
	{
		var j map[string]any
		if json.Unmarshal(raw, &j) == nil {
			var m, g uint8
			// 兼容大小写 key
			if v, ok := j["m"]; ok {
				if n, ok := toUint8(v); ok {
					m = n
				}
			}
			if v, ok := j["M"]; ok && m == 0 {
				if n, ok := toUint8(v); ok {
					m = n
				}
			}
			if v, ok := j["g"]; ok {
				if n, ok := toUint8(v); ok {
					g = n
				}
			}
			if v, ok := j["G"]; ok && g == 0 {
				if n, ok := toUint8(v); ok {
					g = n
				}
			}
			if m != 0 || g != 0 {
				if m == 0 {
					m = 8
				}
				if g == 0 {
					g = 1
				}
				return &oti.ReedSolomonGF2MSchemeSpecific{M: m, G: g}
			}
		}
	}

	// 3) 纯文本：CSV/分隔符
	{
		s := strings.TrimSpace(string(raw))
		// 3.1 kv 风格 "m=8;g=1"
		if strings.ContainsAny(s, "=,:") {
			m := parseKVByte(s, []string{"m", "M"})
			g := parseKVByte(s, []string{"g", "G"})
			if m != 0 || g != 0 {
				if m == 0 {
					m = 8
				}
				if g == 0 {
					g = 1
				}
				return &oti.ReedSolomonGF2MSchemeSpecific{M: m, G: g}
			}
		}
		// 3.2 列表风格 "8,1" / "8|1" / "8 1"
		for _, sep := range []string{",", "|", " "} {
			if strings.Contains(s, sep) {
				parts := strings.Split(s, sep)
				if len(parts) == 2 {
					mu, _ := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 8)
					gu, _ := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 8)
					m := uint8(mu)
					g := uint8(gu)
					if m == 0 {
						m = 8
					}
					if g == 0 {
						g = 1
					}
					if mu != 0 || gu != 0 {
						return &oti.ReedSolomonGF2MSchemeSpecific{M: m, G: g}
					}
				}
			}
		}
	}

	// 都失败则返回 nil（上层可选择忽略或记录日志）
	return nil
}

func toUint8(v any) (uint8, bool) {
	switch x := v.(type) {
	case float64:
		return uint8(x), true
	case int:
		return uint8(x), true
	case int64:
		return uint8(x), true
	case string:
		if n, err := strconv.ParseUint(strings.TrimSpace(x), 10, 8); err == nil {
			return uint8(n), true
		}
	}
	return 0, false
}

func parseKVByte(s string, keys []string) uint8 {
	// 支持 m=8、m:8、"m = 8" 等
	// 简单拆分；更复杂情况可用正则
	for _, k := range keys {
		for _, sep := range []string{"=", ":"} {
			pat := k + sep
			if i := strings.Index(strings.ToLower(s), strings.ToLower(pat)); i >= 0 {
				rest := s[i+len(pat):]
				// 截到下一个分隔符
				for _, stop := range []string{";", ",", " ", "|"} {
					if j := strings.Index(rest, stop); j >= 0 {
						rest = rest[:j]
					}
				}
				if n, err := strconv.ParseUint(strings.TrimSpace(rest), 10, 8); err == nil {
					return uint8(n)
				}
			}
		}
	}
	return 0
}
