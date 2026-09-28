package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	HeaderSize     = 24
	MaxPayloadSize = 65536
	MaxFrameSize   = HeaderSize + MaxPayloadSize
)

type FrameType uint8

const (
	TypeHello FrameType = 0x01
	TypeHelloAck FrameType = 0x02
	TypeOpen FrameType = 0x10
	TypeOpenOK FrameType = 0x11
	TypeOpenErr FrameType = 0x12
	TypeData FrameType = 0x20
	TypeAck FrameType = 0x21
	TypeWindow FrameType = 0x22
	TypeFin FrameType = 0x23
	TypeFinAck FrameType = 0x24
	TypeReset FrameType = 0x25
	TypeFinAckConfirm FrameType = 0x26
	TypeResumeState FrameType = 0x30
	TypeResumeDone FrameType = 0x31
	TypeReady FrameType = 0x32
	TypePing FrameType = 0x40
	TypePong FrameType = 0x41
	TypeGoAway FrameType = 0x42
	TypeProfilePropose FrameType = 0x50
	TypeProfileAccept FrameType = 0x51
	TypeProfileCommit FrameType = 0x52
	TypePadding FrameType = 0x60
)

type Frame struct { Type FrameType; StreamID uint64; Offset uint64; Payload []byte }

func validType(t FrameType) bool {
	switch t {
	case TypeHello, TypeHelloAck, TypeOpen, TypeOpenOK, TypeOpenErr, TypeData, TypeAck, TypeWindow, TypeFin, TypeFinAck, TypeReset, TypeFinAckConfirm, TypeResumeState, TypeResumeDone, TypeReady, TypePing, TypePong, TypeGoAway, TypeProfilePropose, TypeProfileAccept, TypeProfileCommit, TypePadding:
		return true
	default:
		return false
	}
}

func isSessionFrame(t FrameType) bool {
	switch t {
	case TypeHello, TypeHelloAck, TypeResumeState, TypeResumeDone, TypeReady, TypePing, TypePong, TypeGoAway, TypeProfilePropose, TypeProfileAccept, TypeProfileCommit, TypePadding:
		return true
	default:
		return false
	}
}

func validateFrame(f Frame) error {
	if !validType(f.Type) { return fmt.Errorf("unknown frame type: 0x%02x", uint8(f.Type)) }
	if len(f.Payload) > MaxPayloadSize { return errors.New("payload too large") }
	if uint64(len(f.Payload)) > ^uint64(0)-f.Offset { return errors.New("offset overflow") }
	if isSessionFrame(f.Type) { if f.StreamID != 0 { return errors.New("session frame requires stream_id=0") } } else if f.StreamID == 0 { return errors.New("flow frame requires non-zero stream_id") }
	switch f.Type {
	case TypeHello, TypeHelloAck, TypeOpen, TypeOpenOK, TypeOpenErr, TypeReset,
		TypeResumeState, TypeResumeDone, TypeReady, TypePing, TypePong, TypeGoAway,
		TypeProfilePropose, TypeProfileAccept, TypeProfileCommit, TypePadding:
		if f.Offset != 0 { return errors.New("frame type requires offset=0") }
	}
	switch f.Type {
	case TypeAck, TypeWindow, TypeFin, TypeFinAck, TypeFinAckConfirm:
		if len(f.Payload) != 0 { return errors.New("frame type requires empty payload") }
	case TypePing, TypePong:
		if len(f.Payload) != 8 { return errors.New("PING/PONG payload must be exactly 8 bytes") }
	}
	return nil
}

func Encode(w io.Writer, f Frame) error {
	if err := validateFrame(f); err != nil { return err }
	total := HeaderSize + len(f.Payload)
	var hdr [HeaderSize]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(total)); hdr[4] = byte(f.Type)
	binary.BigEndian.PutUint64(hdr[8:16], f.StreamID); binary.BigEndian.PutUint64(hdr[16:24], f.Offset)
	if err := writeFull(w, hdr[:]); err != nil { return err }
	if len(f.Payload) > 0 { return writeFull(w, f.Payload) }
	return nil
}

func Decode(r io.Reader) (Frame, error) {
	var f Frame; var hdr [HeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil { return f, err }
	n := binary.BigEndian.Uint32(hdr[0:4]); if n < HeaderSize || n > MaxFrameSize { return f, errors.New("invalid frame length") }
	if hdr[5] != 0 || binary.BigEndian.Uint16(hdr[6:8]) != 0 { return f, errors.New("flags/reserved must be zero") }
	f.Type = FrameType(hdr[4]); if !validType(f.Type) { return f, errors.New("unknown frame type") }
	f.StreamID = binary.BigEndian.Uint64(hdr[8:16]); f.Offset = binary.BigEndian.Uint64(hdr[16:24])
	plen := int(n)-HeaderSize; if uint64(plen) > ^uint64(0)-f.Offset { return Frame{}, errors.New("offset overflow") }
	if plen > 0 { f.Payload = make([]byte, plen); if _, err := io.ReadFull(r, f.Payload); err != nil { return Frame{}, err } }
	if err := validateFrame(f); err != nil { return Frame{}, err }
	return f, nil
}

func writeFull(w io.Writer, p []byte) error {
	for len(p)>0 { n,err:=w.Write(p); if n<0||n>len(p){return errors.New("invalid writer count")}; p=p[n:]; if err!=nil{return err}; if n==0{return io.ErrShortWrite} }
	return nil
}
