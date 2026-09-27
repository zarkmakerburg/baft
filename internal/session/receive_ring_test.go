package session

import (
	"context"
	"testing"
)

func TestReceiveRingWrapsWithoutExceedingCapacity(t *testing.T) {
	r,err:=newReceiveRing(8);if err!=nil{t.Fatal(err)}
	if err:=r.Write([]byte("abcdef"));err!=nil{t.Fatal(err)}
	p,err:=r.Peek(context.Background(),4);if err!=nil{t.Fatal(err)}
	if string(p)!="abcd"{t.Fatalf("peek=%q",p)}
	if err:=r.Consume(4);err!=nil{t.Fatal(err)}
	if err:=r.Write([]byte("WXYZ"));err!=nil{t.Fatal(err)}

	p,err=r.Peek(context.Background(),8);if err!=nil{t.Fatal(err)}
	if string(p)!="efWX"{t.Fatalf("first wrapped segment=%q",p)}
	if err:=r.Consume(len(p));err!=nil{t.Fatal(err)}
	p,err=r.Peek(context.Background(),8);if err!=nil{t.Fatal(err)}
	if string(p)!="YZ"{t.Fatalf("second wrapped segment=%q",p)}
}

func TestReceiveRingRejectsOverflow(t *testing.T) {
	r,_:=newReceiveRing(4)
	if err:=r.Write([]byte("1234"));err!=nil{t.Fatal(err)}
	if err:=r.Write([]byte("5"));err==nil{t.Fatal("expected bounded overflow rejection")}
}
