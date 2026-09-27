package scheduler

import "testing"

func TestDRRByteFairness(t *testing.T) {
	d:=NewDRR()
	if err:=d.AddFlow(1,1024);err!=nil { t.Fatal(err) }
	if err:=d.AddFlow(2,1024);err!=nil { t.Fatal(err) }
	for i:=0;i<4;i++ {
		if err:=d.Enqueue(Item{FlowID:1,Bytes:1024,Value:i});err!=nil { t.Fatal(err) }
		if err:=d.Enqueue(Item{FlowID:2,Bytes:1024,Value:i});err!=nil { t.Fatal(err) }
	}
	last:=uint64(0)
	for i:=0;i<8;i++ {
		item,ok:=d.Next();if !ok { t.Fatal("scheduler emptied early") }
		if i>0 && item.FlowID==last { t.Fatalf("equal quanta should alternate, got repeated flow %d",item.FlowID) }
		last=item.FlowID
	}
}

func TestDRRDeficitAccumulatesForLargeItem(t *testing.T) {
	d:=NewDRR()
	if err:=d.AddFlow(1,1024);err!=nil { t.Fatal(err) }
	if err:=d.AddFlow(2,1024);err!=nil { t.Fatal(err) }
	if err:=d.Enqueue(Item{FlowID:1,Bytes:3072});err!=nil { t.Fatal(err) }
	if err:=d.Enqueue(Item{FlowID:2,Bytes:1024});err!=nil { t.Fatal(err) }
	first,ok:=d.Next();if !ok||first.FlowID!=2 { t.Fatalf("small eligible item should run first: %#v",first) }
	second,ok:=d.Next();if !ok||second.FlowID!=1 { t.Fatalf("deficit did not accumulate: %#v",second) }
}
