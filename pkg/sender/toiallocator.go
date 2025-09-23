package sender

import (
	"Flute_go/pkg/lct"
	t "Flute_go/pkg/type"
	"log"
	"math/rand"
	"sync"
	"time"
)

// TOIMaxLength 枚举，对应 Rust 的 TOIMaxLength
type TOIMaxLength int

const (
	ToiMax16 TOIMaxLength = iota
	ToiMax32
	ToiMax48
	ToiMax64
	ToiMax80
	ToiMax112
)

// ToiAllocatorInternal

type toiAllocatorInternal struct {
	toiReserved  map[string]struct{}
	toi          t.Uint128
	toiMaxLength TOIMaxLength
}

// ToiAllocator

type ToiAllocator struct {
	internal *sync.Mutex
	state    *toiAllocatorInternal
}

// Toi 对象，类似 Rust struct Toi
type Toi struct {
	allocator *ToiAllocator
	value     t.Uint128
}

// ToiAllocatorInternal 方法

func newInternal(toiMaxLength TOIMaxLength, toiInitialValue *t.Uint128) *toiAllocatorInternal {
	var toi t.Uint128
	if toiInitialValue != nil {
		if toiInitialValue.High == 0 && toiInitialValue.Low == 0 {
			toi = t.Uint128{High: 0, Low: 1}
		} else {
			toi = *toiInitialValue
		}
	} else {
		// 随机生成 u128
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		toi = t.Uint128{High: r.Uint64(), Low: r.Uint64()}
	}

	toi = toMaxLength(toi, toiMaxLength)
	if toi == t.FromUint8(lct.TOI_FDT) {
		toi = toi.AddUint64(1)
	}

	return &toiAllocatorInternal{
		toiReserved:  make(map[string]struct{}),
		toi:          toi,
		toiMaxLength: toiMaxLength,
	}
}

// toMaxLength: 按位掩码限制 TOI 长度
func toMaxLength(toi t.Uint128, toiMaxLength TOIMaxLength) t.Uint128 {
	switch toiMaxLength {
	case ToiMax16:
		return toi.And64(0xFFFF)
	case ToiMax32:
		return toi.And64(0xFFFFFFFF)
	case ToiMax48:
		return toi.And64(0xFFFFFFFFFFFF)
	case ToiMax64:
		return t.Uint128{High: 0, Low: toi.Low} // 只保留 64 位
	case ToiMax80:
		// 高 16 位清零
		return t.Uint128{High: toi.High & 0xFFFF, Low: toi.Low}
	case ToiMax112:
		return toi
	default:
		return toi
	}
}

func (i *toiAllocatorInternal) allocate() t.Uint128 {
	ret := i.toi
	key := ret.String()
	if _, ok := i.toiReserved[key]; ok {
		panic("TOI already reserved")
	}
	i.toiReserved[key] = struct{}{}

	for {
		i.toi = toMaxLength(i.toi.AddUint64(1), i.toiMaxLength)
		if i.toi == t.FromUint8(lct.TOI_FDT) {
			i.toi = t.Uint128{High: 0, Low: 1}
		}
		if _, ok := i.toiReserved[i.toi.String()]; !ok {
			break
		}
		log.Printf("warn: TOI %s is already used by a file or reserved", i.toi.String())
	}
	return ret
}

func (i *toiAllocatorInternal) release(toi t.Uint128) {
	delete(i.toiReserved, toi.String())
}

func NewToiAllocator(toiMaxLength TOIMaxLength, toiInitialValue *t.Uint128) *ToiAllocator {
	return &ToiAllocator{
		state: newInternal(toiMaxLength, toiInitialValue),
	}
}

func (a *ToiAllocator) Allocate() *Toi {
	a.internal.Lock()
	defer a.internal.Unlock()
	val := a.state.allocate()
	return &Toi{
		allocator: a,
		value:     val,
	}
}

func (a *ToiAllocator) AllocateToiFDT() *Toi {
	return &Toi{
		allocator: a,
		value:     t.Uint128{High: 0, Low: 0},
	}
}

func (a *ToiAllocator) Release(toi t.Uint128) {
	if toi == t.FromUint8(lct.TOI_FDT) {
		return
	}
	a.internal.Lock()
	defer a.internal.Unlock()
	a.state.release(toi)
}

// Toi 方法

func (t *Toi) Get() t.Uint128 {
	return t.value
}

// Release: 手动释放 TOI（Go 没有析构）
func (t *Toi) Release() {
	t.allocator.Release(t.value)
}
