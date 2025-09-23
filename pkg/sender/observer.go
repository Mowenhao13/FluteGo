package sender

import (
	"sync"
	"time"
)

type FileInfo struct {
	// Object TOI
	Toi string
}

type EventKind int

const (
	EventStartTransfer EventKind = iota
	EventStopTransfer
)

type Event struct {
	Kind EventKind
	File FileInfo
}

// 任何实现该接口的类型都可以订阅 Sender 的事件
type Subscriber interface {
	OnSenderEvent(evt Event, now time.Time)
}

// ObserverList 等价 Rust struct ObserverList
type ObserverList struct {
	mu          sync.RWMutex
	subscribers []Subscriber
}

// NewObserverList 创建一个空的订阅列表, 返回指针
func NewObserverList() *ObserverList {
	return &ObserverList{
		subscribers: make([]Subscriber, 0),
	}
}

// Subscribe 添加订阅者
func (o *ObserverList) Subscribe(s Subscriber) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.subscribers = append(o.subscribers, s)
}

// Unsubscribe 移除订阅者
func (o *ObserverList) Unsubscribe(s Subscriber) {
	o.mu.Lock()
	defer o.mu.Unlock()
	newSubs := make([]Subscriber, 0, len(o.subscribers))
	for _, sub := range o.subscribers {
		if sub != s { // Go 接口比较，直接判断是否同一对象
			newSubs = append(newSubs, sub)
		}
	}
	o.subscribers = newSubs
}

// Dispatch 派发事件
func (o *ObserverList) Dispatch(evt Event, now time.Time) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, sub := range o.subscribers {
		sub.OnSenderEvent(evt, now)
	}
}

// Debug 用 fmt.Printf("%+v", ObserverList) 时展示
func (o *ObserverList) String() string {
	return "ObserverList"
}
