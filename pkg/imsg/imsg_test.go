package imsg

import (
	"bytes"
	"testing"
)

func TestWriteAndRead(t *testing.T) {
	payload := []byte(`{"user":"admin","host":"10.0.1.1"}`)
	msg := NewImsg(1, payload)

	buf := &bytes.Buffer{}
	if err := WriteMessage(buf, msg); err != nil {
		t.Fatalf("WriteMessage failed: %v", err)
	}

	got, err := ReadMessage(buf)
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}

	if got.Version != Version {
		t.Errorf("Version = %d, want %d", got.Version, Version)
	}
	if got.Type != 1 {
		t.Errorf("Type = %d, want 1", got.Type)
	}
	if string(got.Payload) != string(payload) {
		t.Errorf("Payload = %s, want %s", got.Payload, payload)
	}
}

func TestEmptyPayload(t *testing.T) {
	msg := NewImsg(8, nil)

	buf := &bytes.Buffer{}
	if err := WriteMessage(buf, msg); err != nil {
		t.Fatalf("WriteMessage failed: %v", err)
	}

	got, err := ReadMessage(buf)
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}

	if len(got.Payload) != 0 {
		t.Errorf("Payload length = %d, want 0", len(got.Payload))
	}
}

func TestWrongVersion(t *testing.T) {
	msg := &Imsg{Version: 99, Type: 1, Payload: []byte("test")}
	buf := &bytes.Buffer{}
	WriteMessage(buf, msg)

	_, err := ReadMessage(buf)
	if err == nil {
		t.Error("expected error for wrong version")
	}
}

func TestLargePayload(t *testing.T) {
	large := make([]byte, MaxPayloadSize+1)
	msg := NewImsg(1, large)

	buf := &bytes.Buffer{}
	err := WriteMessage(buf, msg)
	if err == nil {
		t.Error("expected error for payload too large")
	}
}

func TestRoundTripMultipleMessages(t *testing.T) {
	msgs := []*Imsg{
		NewImsg(1, []byte(`{"a":1}`)),
		NewImsg(6, []byte(`{"cmd":"ls"}`)),
		NewImsg(100, []byte(`{"result":"ok"}`)),
	}

	buf := &bytes.Buffer{}
	for _, msg := range msgs {
		if err := WriteMessage(buf, msg); err != nil {
			t.Fatalf("WriteMessage failed: %v", err)
		}
	}

	for i, want := range msgs {
		got, err := ReadMessage(buf)
		if err != nil {
			t.Fatalf("ReadMessage #%d failed: %v", i, err)
		}
		if got.Type != want.Type {
			t.Errorf("msg %d: Type = %d, want %d", i, got.Type, want.Type)
		}
		if string(got.Payload) != string(want.Payload) {
			t.Errorf("msg %d: Payload mismatch", i)
		}
	}
}