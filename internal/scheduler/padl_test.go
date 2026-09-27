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
