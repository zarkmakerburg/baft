package scheduler

import "testing"

func TestPADLPrefersLowerReplayPressure(t *testing.T) {
	p:=NewPADL(8)
	if err:=p.AddFlow(1,1024);err!=nil{t.Fatal(err)}
	if err:=p.AddFlow(2,1024);err!=nil{t.Fatal(err)}
	if err:=p.UpdatePressure(1,64*1024);err!=nil{t.Fatal(err)}
	if err:=p.UpdatePressure(2,0);err!=nil{t.Fatal(err)}
	if err:=p.Enqueue(Item{FlowID:1,Bytes:1024,Value:"pressured"});err!=nil{t.Fatal(err)}
	if err:=p.Enqueue(Item{FlowID:2,Bytes:1024,Value:"clear"});err!=nil{t.Fatal(err)}
	got,ok:=p.Next()
	if !ok||got.FlowID!=2{t.Fatalf("expected lower-pressure flow first, got %#v",got)}
}

func TestPADLAgingBoundsPressureStarvation(t *testing.T) {
	const maxSkips=4
	p:=NewPADL(maxSkips)
	if err:=p.AddFlow(1,1024);err!=nil{t.Fatal(err)}
	if err:=p.AddFlow(2,1024);err!=nil{t.Fatal(err)}
	_ = p.UpdatePressure(1,1<<30)
	_ = p.UpdatePressure(2,0)
	if err:=p.Enqueue(Item{FlowID:1,Bytes:1024});err!=nil{t.Fatal(err)}
	for i:=0;i<maxSkips+2;i++{
		if err:=p.Enqueue(Item{FlowID:2,Bytes:1024});err!=nil{t.Fatal(err)}
	}
	seenAt:=-1
	for i:=0;i<maxSkips+2;i++{
		got,ok:=p.Next();if !ok{t.Fatal("scheduler emptied")}
		if got.FlowID==1{seenAt=i;break}
	}
	if seenAt<0||seenAt>maxSkips{
		t.Fatalf("pressured flow starved beyond bound: selection=%d max=%d",seenAt,maxSkips)
	}
}

func TestPADLEqualPressureRetainsByteFairness(t *testing.T) {
	p:=NewPADL(8)
	if err:=p.AddFlow(1,1024);err!=nil{t.Fatal(err)}
	if err:=p.AddFlow(2,1024);err!=nil{t.Fatal(err)}
	for i:=0;i<3;i++{
		_ = p.Enqueue(Item{FlowID:1,Bytes:1024})
		_ = p.Enqueue(Item{FlowID:2,Bytes:1024})
	}
	last:=uint64(0)
	for i:=0;i<6;i++{
		got,ok:=p.Next();if !ok{t.Fatal("scheduler emptied")}
		if i>0&&got.FlowID==last{t.Fatalf("equal pressure should preserve round-robin fairness, repeated=%d",got.FlowID)}
		last=got.FlowID
	}
}

func TestPADLHighVolumeLivenessAndBoundedStarvation(t *testing.T) {
	const (
		flows = 64
		itemsPerFlow = 256
		bytesPerItem = 1024
		maxSkips = 8
	)
	p:=NewPADL(maxSkips)
	remaining:=make(map[uint64]int,flows)
	for i:=1;i<=flows;i++{
		id:=uint64(i)
		if err:=p.AddFlow(id,bytesPerItem);err!=nil{t.Fatal(err)}
		pressure:=uint64((i%7)*64*1024)
		if err:=p.UpdatePressure(id,pressure);err!=nil{t.Fatal(err)}
		remaining[id]=itemsPerFlow
		for j:=0;j<itemsPerFlow;j++{
			if err:=p.Enqueue(Item{FlowID:id,Bytes:bytesPerItem,Value:j});err!=nil{t.Fatal(err)}
		}
	}
	total:=flows*itemsPerFlow
	served:=make(map[uint64]int,flows)
	for i:=0;i<total;i++{
		item,ok:=p.Next()
		if !ok{t.Fatalf("scheduler stopped at %d/%d",i,total)}
		served[item.FlowID]++
		remaining[item.FlowID]--
		// Simulate changing replay debt instead of a static ranking.
		if served[item.FlowID]%17==0{
			_ = p.UpdatePressure(item.FlowID,uint64((served[item.FlowID]%9)*32*1024))
		}
	}
	if p.Len()!=0{t.Fatalf("queue not drained: %d",p.Len())}
	for id,n:=range served{
		if n!=itemsPerFlow{t.Fatalf("flow %d served=%d want=%d",id,n,itemsPerFlow)}
	}
}

func TestPADLRemoveFlowReturnsEveryQueuedItem(t *testing.T) {
	p:=NewPADL(8)
	if err:=p.AddFlow(7,1024);err!=nil{t.Fatal(err)}
	for i:=0;i<32;i++{
		if err:=p.Enqueue(Item{FlowID:7,Bytes:1024,Value:i});err!=nil{t.Fatal(err)}
	}
	pending:=p.RemoveFlow(7)
	if len(pending)!=32{t.Fatalf("pending=%d",len(pending))}
	if p.Len()!=0{t.Fatalf("queue leak=%d",p.Len())}
	if _,ok:=p.Next();ok{t.Fatal("removed flow still schedulable")}
}
