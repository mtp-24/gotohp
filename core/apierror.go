package core

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

// google.rpc.Code values the uploader acts on.
const (
	rpcCodePermissionDenied  = 7
	rpcCodeResourceExhausted = 8
	rpcCodeUnauthenticated   = 16
)

// APIError is a non-2xx response from Google. When the body is a protobuf
// google.rpc.Status, Code and Message come from it; otherwise Message holds
// the body text.
type APIError struct {
	StatusCode int
	// Code is the google.rpc.Code, or 0 when the body was not a status.
	Code    int
	Message string
}

func (e *APIError) Error() string {
	// Keep the "status <n>" wording: callers and the frontend match on it.
	if e.Message == "" {
		return fmt.Sprintf("request failed with status %d", e.StatusCode)
	}
	return fmt.Sprintf("request failed with status %d: %s", e.StatusCode, e.Message)
}

// StorageFull reports that the account has no room left. Rate limiting uses
// the same code but always arrives as HTTP 429.
func (e *APIError) StorageFull() bool {
	return e.Code == rpcCodeResourceExhausted && e.StatusCode != http.StatusTooManyRequests
}

// CredentialRejected reports that Google refused the account's credential.
func (e *APIError) CredentialRejected() bool {
	return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden ||
		e.Code == rpcCodeUnauthenticated || e.Code == rpcCodePermissionDenied
}

// RateLimited reports an HTTP 429.
func (e *APIError) RateLimited() bool {
	return e.StatusCode == http.StatusTooManyRequests
}

// asAPIError returns the APIError wrapped in err, if any.
func asAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	ok := errors.As(err, &apiErr)
	return apiErr, ok
}

// newAPIError reads a failed response's body into an APIError.
func newAPIError(resp *http.Response) *APIError {
	body, _ := ReadResponseBody(resp)
	apiErr := &APIError{StatusCode: resp.StatusCode}
	if code, message, ok := parseRPCStatus(body); ok {
		apiErr.Code = code
		apiErr.Message = message
		if message == "" {
			apiErr.Message = fmt.Sprintf("google.rpc code %d", code)
		}
		return apiErr
	}
	apiErr.Message = readableBody(body)
	return apiErr
}

// parseRPCStatus decodes a google.rpc.Status: code in field 1, message in
// field 2. It fails for anything else, including a zero (OK) code.
func parseRPCStatus(body []byte) (code int, message string, ok bool) {
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			return 0, "", false
		}
		body = body[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(body)
			if m < 0 {
				return 0, "", false
			}
			code = int(v)
			body = body[m:]
		case num == 2 && typ == protowire.BytesType:
			v, m := protowire.ConsumeBytes(body)
			if m < 0 || !utf8.Valid(v) {
				return 0, "", false
			}
			message = string(v)
			body = body[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, body)
			if m < 0 {
				return 0, "", false
			}
			body = body[m:]
		}
	}
	return code, message, code > 0
}

// readableBody returns body as trimmed text when it is printable, and a byte
// count otherwise, so binary protobuf never ends up in an error message.
func readableBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}
	if !utf8.ValidString(text) || strings.ContainsFunc(text, func(r rune) bool {
		return r < 0x20 && r != '\n' && r != '\r' && r != '\t'
	}) {
		return fmt.Sprintf("%d-byte response", len(body))
	}
	const limit = 300
	if len(text) > limit {
		text = text[:limit] + "..."
	}
	return text
}
