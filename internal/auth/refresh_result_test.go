package auth

import (
	"bytes"
	"testing"
)

func TestRefreshResultEncryptionBindsRequest(t *testing.T) {
	Init("encryption-secret")
	plain := []byte("sensitive token pair")
	sealed, err := SealRefreshResult(plain, "old:request:42")
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenRefreshResult(sealed, "old:request:42")
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatal("round trip")
	}
	for _, binding := range []string{"other:request:42", "old:other:42", "old:request:99"} {
		if _, err := OpenRefreshResult(sealed, binding); err == nil {
			t.Fatal("wrong binding accepted")
		}
	}
	for _, bad := range []string{"", sealed[:len(sealed)/2], "!invalid"} {
		if _, err := OpenRefreshResult(bad, "old:request:42"); err == nil {
			t.Fatal("corrupt result accepted")
		}
	}
	Init("another-secret")
	if _, err := OpenRefreshResult(sealed, "old:request:42"); err == nil {
		t.Fatal("wrong key accepted")
	}
}
