package resources

import (
	"errors"
	"testing"
)

func TestControlQueueMessageCap(t *testing.T) {
	q:=NewDefaultControlQueue()
	for i:=0;i<256;i++ {
		if err:=q.Enqueue(ControlItem{WireBytes:24});err!=nil { t.Fatalf("enqueue %d: %v",i,err) }
	}
	if err:=q.Enqueue(ControlItem{WireBytes:24});!errors.Is(err,ErrResourceExhausted) {
		t.Fatalf("expected message cap, got %v",err)
	}
}

func TestControlQueueByteCap(t *testing.T) {
	q,err:=NewControlQueue(1024,1<<20)
	if err!=nil { t.Fatal(err) }
	if err:=q.Enqueue(ControlItem{WireBytes:(1<<20)-1});err!=nil { t.Fatal(err) }
	if err:=q.Enqueue(ControlItem{WireBytes:2});!errors.Is(err,ErrResourceExhausted) {
		t.Fatalf("expected byte cap, got %v",err)
	}
	item,ok:=q.Dequeue()
	if !ok||item.WireBytes!=(1<<20)-1 { t.Fatalf("unexpected dequeue: %#v %v",item,ok) }
	if n,b:=q.LenBytes();n!=0||b!=0 { t.Fatalf("queue accounting leak: n=%d b=%d",n,b) }
}
