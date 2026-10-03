package main

import (
	"testing"
	"time"
)

func TestSummarizeTimesAndProbeRedaction(t *testing.T) {
	start := time.Date(2026,10,3,0,0,0,0,time.UTC)
	obs := []Observation{
		{Time:start, TunnelID:"T9", ISPID:"ISP-A", Kind:"health", Status:"ok"},
		{Time:start.Add(3*time.Hour), TunnelID:"T9", ISPID:"ISP-A", Kind:"health", Status:"disrupted", BytesTotal:1000},
		{Time:start.Add(5*time.Hour), TunnelID:"T9", ISPID:"ISP-A", Kind:"diagnostic", Status:"blocked", BlockType:"port-only", BytesTotal:2000},
		{Time:start.Add(time.Hour), TunnelID:"T9", Kind:"probe_inbound", Status:"observed", RemoteSourceHash:"h1", RemoteASN:"AS64500"},
		{Time:start.Add(2*time.Hour), TunnelID:"T9", Kind:"probe_inbound", Status:"observed", RemoteSourceHash:"h1", RemoteASN:"AS64500"},
	}
	s := summarize(obs)
	if len(s.Cells)!=1 { t.Fatalf("cells=%d",len(s.Cells)) }
	c:=s.Cells[0]
	if c.FirstDisruptionHours==nil || *c.FirstDisruptionHours!=3 { t.Fatalf("first=%v",c.FirstDisruptionHours) }
	if c.FullBlockHours==nil || *c.FullBlockHours!=5 { t.Fatalf("block=%v",c.FullBlockHours) }
	if c.BlockType!="port-only" { t.Fatalf("type=%s",c.BlockType) }
	if len(s.Probes)!=1 || s.Probes[0].UniqueSources!=1 || s.Probes[0].InboundUnknown!=2 {
		t.Fatalf("probe=%+v",s.Probes)
	}
}

func TestValidateRejectsDirectIdentifiersAndBadTunnelShapeByContract(t *testing.T) {
	o:=Observation{Time:time.Now(),TunnelID:"T11",ISPID:"ISP-A",Kind:"health",Status:"ok"}
	if err:=validate(o); err==nil { t.Fatal("expected invalid tunnel") }
}
