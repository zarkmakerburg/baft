package protocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

type Hello struct {
	ProtocolMin uint16 `json:"protocol_min"`
	ProtocolMax uint16 `json:"protocol_max"`
	NodeID string `json:"node_id"`
	BootID string `json:"boot_id"`
	SessionID string `json:"session_id"`
	ShardID uint8 `json:"shard_id"`
	Epoch string `json:"epoch"`
	Mode string `json:"mode"`
	ResumeSnapshotID *string `json:"resume_snapshot_id"`
	ProfileID string `json:"profile_id"`
	ProfileVersion uint32 `json:"profile_version"`
	ConfigRevision string `json:"config_revision"`
	Capabilities []string `json:"capabilities"`
}

type NegotiatedLimits struct {
	MaxFramePayloadBytes uint32 `json:"max_frame_payload_bytes"`
	MaxFlowsPerShard uint32 `json:"max_flows_per_shard"`
	ReceiveInitialBytes uint32 `json:"receive_initial_bytes"`
	ReceiveMaxBytes uint32 `json:"receive_max_bytes"`
	RetentionMS uint32 `json:"retention_ms"`
}

type AcceptedProfile struct { ID string `json:"id"`; Version uint32 `json:"version"` }
type HelloAck struct { SelectedProtocol uint16 `json:"selected_protocol"`; SessionID string `json:"session_id"`; Epoch string `json:"epoch"`; PeerBootID string `json:"peer_boot_id"`; AcceptedProfile AcceptedProfile `json:"accepted_profile"`; NegotiatedLimits NegotiatedLimits `json:"negotiated_limits"` }
type Ready struct { SnapshotID *string `json:"snapshot_id"` }

func DecodeHello(payload []byte) (Hello,error) {
	var v Hello; if err:=decodeStrictObject(payload,&v);err!=nil{return Hello{},err}
	if v.ProtocolMin!=1||v.ProtocolMax!=1{return Hello{},errors.New("VERSION_UNSUPPORTED")}
	if v.NodeID==""||len(v.NodeID)>64{return Hello{},errors.New("invalid node_id")}
	if !validHex128(v.BootID)||!validHex128(v.SessionID){return Hello{},errors.New("invalid boot_id/session_id")}
	if v.ShardID>7{return Hello{},errors.New("invalid shard_id")}
	if _,err:=parseDecimalU64(v.Epoch);err!=nil{return Hello{},fmt.Errorf("invalid epoch: %w",err)}
	if v.Mode!="new"&&v.Mode!="resume"{return Hello{},errors.New("invalid mode")}
	if v.Mode=="new"&&v.ResumeSnapshotID!=nil{return Hello{},errors.New("new session must not have resume_snapshot_id")}
	if v.Mode=="resume"&&(v.ResumeSnapshotID==nil||!validHex128(*v.ResumeSnapshotID)){return Hello{},errors.New("resume requires valid snapshot id")}
	if v.ProfileID==""||len(v.ProfileID)>64||v.ConfigRevision==""||len(v.ConfigRevision)>64{return Hello{},errors.New("invalid profile/config revision")}
	if len(v.Capabilities)>16{return Hello{},errors.New("too many capabilities")}
	return v,nil
}

func DecodeHelloAck(payload []byte)(HelloAck,error){var v HelloAck;if err:=decodeStrictObject(payload,&v);err!=nil{return HelloAck{},err};if v.SelectedProtocol!=1||!validHex128(v.SessionID)||!validHex128(v.PeerBootID){return HelloAck{},errors.New("invalid HELLO_ACK identity/protocol")};if _,err:=parseDecimalU64(v.Epoch);err!=nil{return HelloAck{},errors.New("invalid HELLO_ACK epoch")};if v.AcceptedProfile.ID==""||v.NegotiatedLimits.MaxFramePayloadBytes==0||v.NegotiatedLimits.MaxFramePayloadBytes>MaxPayloadSize{return HelloAck{},errors.New("invalid negotiated limits/profile")};return v,nil}
func DecodeReady(payload []byte)(Ready,error){var v Ready;if err:=decodeStrictObject(payload,&v);err!=nil{return Ready{},err};if v.SnapshotID!=nil&&!validHex128(*v.SnapshotID){return Ready{},errors.New("invalid READY snapshot_id")};return v,nil}
func decodeStrictObject(payload []byte,out any)error{if len(payload)==0||len(payload)>MaxPayloadSize{return errors.New("invalid JSON control payload length")};if err:=rejectDuplicateTopLevelKeys(payload);err!=nil{return err};dec:=json.NewDecoder(bytes.NewReader(payload));dec.DisallowUnknownFields();if err:=dec.Decode(out);err!=nil{return err};var extra any;if err:=dec.Decode(&extra);!errors.Is(err,io.EOF){if err==nil{return errors.New("multiple JSON values are not allowed")};return err};return nil}
func validHex128(s string)bool{if len(s)!=32{return false};b,err:=hex.DecodeString(s);return err==nil&&len(b)==16&&s==fmt.Sprintf("%x",b)}
func parseDecimalU64(s string)(uint64,error){if s==""||(len(s)>1&&s[0]=='0'){return 0,errors.New("non-canonical decimal")};return strconv.ParseUint(s,10,64)}
