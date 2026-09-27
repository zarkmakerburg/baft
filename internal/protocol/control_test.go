package protocol

import "testing"

func TestDecodeOpenStrict(t *testing.T) {
	v, err := DecodeOpen([]byte(`{"route_id":"service-main","open_nonce":"00112233445566778899aabbccddeeff"}`))
	if err != nil {
		t.Fatal(err)
	}
	if v.RouteID != "service-main" {
		t.Fatalf("route=%q", v.RouteID)
	}
}

func TestDecodeOpenRejectsTargetInjection(t *testing.T) {
	_, err := DecodeOpen([]byte(`{"route_id":"service-main","open_nonce":"00112233445566778899aabbccddeeff","target":"1.2.3.4:22"}`))
	if err == nil {
		t.Fatal("expected unknown field rejection")
	}
}

func TestDecodeOpenRejectsDuplicateKey(t *testing.T) {
	_, err := DecodeOpen([]byte(`{"route_id":"a","route_id":"b","open_nonce":"00112233445566778899aabbccddeeff"}`))
	if err == nil {
		t.Fatal("expected duplicate key rejection")
	}
}

func TestDecodeOpenErrorRejectsUnknownCode(t *testing.T) {
	if _, err := DecodeOpenError([]byte(`{"code":"SOMETHING_ELSE"}`)); err == nil {
		t.Fatal("expected unknown error code rejection")
	}
}

func TestDecodeResetAcceptsOnlyFixedCodeList(t *testing.T) {
	v, err := DecodeReset([]byte(`{"code":"FLOW_CONTROL_ERROR"}`))
	if err != nil { t.Fatal(err) }
	if v.Code != ErrorFlowControl { t.Fatalf("code=%q", v.Code) }
	if _, err := DecodeReset([]byte(`{"code":"FLOW_CONTROL_ERROR","detail":"secret path"}`)); err == nil {
		t.Fatal("expected unknown reset field rejection")
	}
}
