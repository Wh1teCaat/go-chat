package controller

import (
	"testing"

	"chat_proj/pkg/apperrors"
)

func TestWSErrorDataIncludesClientMsgIDWhenPresent(t *testing.T) {
	data := wsErrorData(apperrors.ErrInvalidInput, "client-1")

	if data["clientMsgID"] != "client-1" {
		t.Fatalf("expected clientMsgID to be echoed, got %+v", data)
	}
}
