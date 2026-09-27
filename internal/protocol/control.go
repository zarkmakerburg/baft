package protocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type OpenRequest struct {
	RouteID   string `json:"route_id"`
	OpenNonce string `json:"open_nonce"`
}

type ErrorCode string

const (
	ErrorAuthFailed         ErrorCode = "AUTH_FAILED"
	ErrorVersionUnsupported ErrorCode = "VERSION_UNSUPPORTED"
	ErrorInvalidFrame       ErrorCode = "INVALID_FRAME"
	ErrorFlowControl        ErrorCode = "FLOW_CONTROL_ERROR"
	ErrorRouteDenied        ErrorCode = "ROUTE_DENIED"
	ErrorRouteNotFound      ErrorCode = "ROUTE_NOT_FOUND"
	ErrorTargetUnreachable  ErrorCode = "TARGET_UNREACHABLE"
	ErrorTargetTimeout      ErrorCode = "TARGET_TIMEOUT"
	ErrorResourceExhausted  ErrorCode = "RESOURCE_EXHAUSTED"
	ErrorSessionExpired     ErrorCode = "SESSION_EXPIRED"
	ErrorPeerRestarted      ErrorCode = "PEER_RESTARTED"
	ErrorStaleEpoch         ErrorCode = "STALE_EPOCH"
	ErrorStateMismatch      ErrorCode = "STATE_MISMATCH"
	ErrorAdminDrain         ErrorCode = "ADMIN_DRAIN"
	ErrorProtocol           ErrorCode = "PROTOCOL_ERROR"
)

var validErrorCodes = map[ErrorCode]struct{}{
	ErrorAuthFailed: {}, ErrorVersionUnsupported: {}, ErrorInvalidFrame: {},
	ErrorFlowControl: {}, ErrorRouteDenied: {}, ErrorRouteNotFound: {},
	ErrorTargetUnreachable: {}, ErrorTargetTimeout: {}, ErrorResourceExhausted: {},
	ErrorSessionExpired: {}, ErrorPeerRestarted: {}, ErrorStaleEpoch: {},
	ErrorStateMismatch: {}, ErrorAdminDrain: {}, ErrorProtocol: {},
}

type OpenError struct {
	Code ErrorCode `json:"code"`
}

type Reset struct {
	Code ErrorCode `json:"code"`
}

func EncodeControl(v any) ([]byte, error) {
	return json.Marshal(v)
}

func DecodeOpen(payload []byte) (OpenRequest, error) {
	if err := rejectDuplicateTopLevelKeys(payload); err != nil {
		return OpenRequest{}, err
	}
	var v OpenRequest
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return OpenRequest{}, err
	}
	if err := requireEOF(dec); err != nil {
		return OpenRequest{}, err
	}
	if v.RouteID == "" || len(v.RouteID) > 64 {
		return OpenRequest{}, errors.New("route_id must be 1..64 bytes")
	}
	b, err := hex.DecodeString(v.OpenNonce)
	if err != nil || len(b) != 16 || v.OpenNonce != fmt.Sprintf("%x", b) {
		return OpenRequest{}, errors.New("open_nonce must be lowercase 128-bit hex")
	}
	return v, nil
}

func DecodeOpenError(payload []byte) (OpenError, error) {
	if err := rejectDuplicateTopLevelKeys(payload); err != nil {
		return OpenError{}, err
	}
	var v OpenError
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return OpenError{}, err
	}
	if err := requireEOF(dec); err != nil {
		return OpenError{}, err
	}
	if !ValidErrorCode(v.Code) {
		return OpenError{}, errors.New("invalid error code")
	}
	return v, nil
}

func DecodeReset(payload []byte) (Reset, error) {
	if err := rejectDuplicateTopLevelKeys(payload); err != nil {
		return Reset{}, err
	}
	var v Reset
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return Reset{}, err
	}
	if err := requireEOF(dec); err != nil {
		return Reset{}, err
	}
	if !ValidErrorCode(v.Code) {
		return Reset{}, errors.New("invalid reset code")
	}
	return v, nil
}

func ValidErrorCode(code ErrorCode) bool {
	_, ok := validErrorCodes[code]
	return ok
}

func requireEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func rejectDuplicateTopLevelKeys(payload []byte) error {
	dec := json.NewDecoder(bytes.NewReader(payload))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("control payload must be a JSON object")
	}
	seen := map[string]struct{}{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return errors.New("JSON object key must be a string")
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate JSON key: %s", key)
		}
		seen[key] = struct{}{}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	return nil
}
