package sender

import (
	"Flute_go/pkg/lct"
	"Flute_go/pkg/oti"
	"Flute_go/pkg/transport"
	"net/url"
	"testing"
	"time"
)

// createObj 创建一个指定长度的 ObjectDesc
func createObj(length int) *ObjectDesc {
	buffer := make([]byte, length)
	u, _ := url.Parse("file:///hello")
	obj, err := CreateFromBuffer(
		buffer,
		"text",
		u,
		1,
		nil,
		nil,
		nil,
		nil,
		lct.CencNull,
		true,
		nil,
		true,
	)
	if err != nil {
		panic(err)
	}
	return obj
}

func TestSenderBasic(t *testing.T) {
	// 对应 test_sender
	o := oti.NewOti()
	endpoint := transport.NewUDPEndpoint(nil, "224.0.0.1", 1234)
	sender := NewSender(endpoint, 1, o, nil)

	nbPkt := int(o.EncodingSymbolLength) * 3

	_, err := sender.AddObject(0, createObj(nbPkt))
	if err != nil {
		t.Fatalf("AddObject failed: %v", err)
	}

	err = sender.Publish(time.Now())
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	for {
		data := sender.Read(time.Now())
		if data == nil {
			break
		}
		// 可以在这里检查 data 是否符合预期，但和 Rust 一样只消费掉
	}
}

func TestSenderFileTooLarge(t *testing.T) {
	// 对应 test_sender_file_too_large
	o := oti.NewNoCode(4, 2)
	endpoint := transport.NewUDPEndpoint(nil, "224.0.0.1", 1234)
	// 创建一个比最大传输长度更大的 buffer
	object := createObj(int(o.MaxTransferLength() + 1))
	sender := NewSender(endpoint, 1, o, nil)

	_, err := sender.AddObject(0, object)
	if err == nil {
		t.Fatalf("expected error for too large object, but got nil")
	}
}

func TestSenderRemoveObject(t *testing.T) {
	// 对应 test_sender_remove_object
	o := oti.NewOti()
	endpoint := transport.NewUDPEndpoint(nil, "224.0.0.1", 1234)
	sender := NewSender(endpoint, 1, o, nil)

	if sender.NbObjects() != 0 {
		t.Fatalf("expected 0 objects initially")
	}

	toi, err := sender.AddObject(0, createObj(1024))
	if err != nil {
		t.Fatalf("AddObject failed: %v", err)
	}
	if sender.NbObjects() != 1 {
		t.Fatalf("expected 1 object after add, got %d", sender.NbObjects())
	}

	success := sender.RemoveObject(toi)
	if !success {
		t.Fatalf("expected remove success, got false")
	}
	if sender.NbObjects() != 0 {
		t.Fatalf("expected 0 objects after remove, got %d", sender.NbObjects())
	}
}

func TestSenderComplete(t *testing.T) {
	// 对应 sender_complete
	o := oti.NewOti()
	endpoint := transport.NewUDPEndpoint(nil, "224.0.0.1", 1234)
	sender := NewSender(endpoint, 1, o, nil)

	object1 := createObj(1024)
	object2 := createObj(1024)

	_, err := sender.AddObject(0, object1)
	if err != nil {
		t.Fatalf("AddObject failed: %v", err)
	}

	sender.SetComplete()
	_, err = sender.AddObject(0, object2)
	if err == nil {
		t.Fatalf("expected error when adding after complete, got nil")
	}
}
