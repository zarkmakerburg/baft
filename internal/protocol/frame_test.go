package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestRoundTrip(t *testing.T) { want:=Frame{Type:TypeData,StreamID:1,Offset:7,Payload:[]byte("abc")}; var b bytes.Buffer; if err:=Encode(&b,want);err!=nil{t.Fatal(err)}; got,err:=Decode(&b); if err!=nil{t.Fatal(err)}; if got.Type!=want.Type||got.StreamID!=want.StreamID||got.Offset!=want.Offset||!bytes.Equal(got.Payload,want.Payload){t.Fatalf("mismatch: %#v",got)} }

func TestGoldenVectors(t *testing.T) {
	tests:=[]struct{name string; f Frame; hex string}{
		{"data",Frame{Type:TypeData,StreamID:1,Offset:7,Payload:[]byte("abc")},"0000001b2000000000000000000000010000000000000007616263"},
		{"ack",Frame{Type:TypeAck,StreamID:1,Offset:10},"00000018210000000000000000000001000000000000000a"},
		{"hello",Frame{Type:TypeHello,StreamID:0,Offset:0,Payload:[]byte("{}")},"0000001a01000000000000000000000000000000000000007b7d"},
	}
	for _,tt:=range tests{t.Run(tt.name,func(t *testing.T){var b bytes.Buffer;if err:=Encode(&b,tt.f);err!=nil{t.Fatal(err)};if got:=hex.EncodeToString(b.Bytes());got!=tt.hex{t.Fatalf("got %s want %s",got,tt.hex)};decoded,err:=Decode(bytes.NewReader(b.Bytes()));if err!=nil{t.Fatal(err)};if decoded.Type!=tt.f.Type||decoded.StreamID!=tt.f.StreamID||decoded.Offset!=tt.f.Offset||!bytes.Equal(decoded.Payload,tt.f.Payload){t.Fatalf("round-trip mismatch: %#v",decoded)}})}
}

type shortWriter struct{ b bytes.Buffer }
func (w *shortWriter) Write(p []byte)(int,error){if len(p)>3{p=p[:3]};return w.b.Write(p)}
func TestEncodeHandlesPartialWrites(t *testing.T){w:=&shortWriter{};want:=Frame{Type:TypeData,StreamID:1,Offset:0,Payload:[]byte("0123456789")};if err:=Encode(w,want);err!=nil{t.Fatal(err)};got,err:=Decode(bytes.NewReader(w.b.Bytes()));if err!=nil{t.Fatal(err)};if !bytes.Equal(got.Payload,want.Payload){t.Fatalf("payload mismatch: %q",got.Payload)}}
func TestRejectOversizeLength(t *testing.T){hdr:=make([]byte,HeaderSize);binary.BigEndian.PutUint32(hdr[:4],uint32(MaxFrameSize+1));hdr[4]=byte(TypeData);if _,err:=Decode(bytes.NewReader(hdr));err==nil{t.Fatal("expected error")}}
func TestRejectUnknownType(t *testing.T){hdr:=make([]byte,HeaderSize);binary.BigEndian.PutUint32(hdr[:4],HeaderSize);hdr[4]=0xff;if _,err:=Decode(bytes.NewReader(hdr));err==nil{t.Fatal("expected error")}}
func TestRejectWrongStreamClass(t *testing.T){var b bytes.Buffer;if err:=Encode(&b,Frame{Type:TypeData,StreamID:0,Payload:[]byte("x")});err==nil{t.Fatal("expected stream_id validation error")};if err:=Encode(&b,Frame{Type:TypeHello,StreamID:1,Payload:[]byte("{}")});err==nil{t.Fatal("expected control stream validation error")}}
