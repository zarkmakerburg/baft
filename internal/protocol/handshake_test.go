package protocol

import "testing"

func TestHelloRoundTripValidation(t *testing.T) {
	h := Hello{ProtocolMin:1,ProtocolMax:1,NodeID:"ir-01",BootID:"00112233445566778899aabbccddeeff",SessionID:"ffeeddccbbaa99887766554433221100",ShardID:0,Epoch:"1",Mode:"new",ResumeSnapshotID:nil,ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"test",Capabilities:[]string{}}
	b,err:=EncodeControl(h);if err!=nil{t.Fatal(err)};got,err:=DecodeHello(b);if err!=nil{t.Fatal(err)};if got.NodeID!=h.NodeID||got.Epoch!="1"{t.Fatalf("bad hello: %#v",got)}
}
func TestHelloRejectsNonCanonicalEpoch(t *testing.T){b:=[]byte(`{"protocol_min":1,"protocol_max":1,"node_id":"ir-01","boot_id":"00112233445566778899aabbccddeeff","session_id":"ffeeddccbbaa99887766554433221100","shard_id":0,"epoch":"01","mode":"new","resume_snapshot_id":null,"profile_id":"secure-fast","profile_version":1,"config_revision":"test","capabilities":[]}`);if _,err:=DecodeHello(b);err==nil{t.Fatal("expected invalid epoch")}}
