package control

import (
	"bytes"
	"reflect"
	"testing"
	"time"
)

type rw struct{ bytes.Buffer }

func TestRoundTripAll(t *testing.T) {
	now := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	msgs := []any{
		&Hello{Role: RoleHelper, RelayVersion: "v1", ServerTime: now, Name: "alice"},
		&ClientHello{AppVersion: "dev", UserMachine: "anna@pc1"},
		&Presence{Clients: []ClientInfo{{Fingerprint: "SHA256:x", Name: "anna@pc1", Online: true,
			Request: &RequestInfo{ID: "r1", Created: now, State: RequestTaken, TakenBy: "alice"}}},
			Helpers: []HelperInfo{{Name: "alice", Online: true}}},
		&RegisterClient{Seq: 1, PublicKey: "ssh-ed25519 AAAA anna@pc1"},
		&UnregisterClient{Seq: 2, Fingerprint: "SHA256:x"},
		&RegisterHelper{Seq: 5, PublicKey: "ssh-ed25519 AAAA bob@pc2", Name: "bob"},
		&RenameClient{Seq: 4, Fingerprint: "SHA256:x", Name: "Anna laptop"},
		&Connect{Seq: 4, Fingerprint: "SHA256:x", RequestID: "r2"},
		&Reply{Seq: 3, Error: "request is not open"},
		&Registered{},
		&RequestStateMsg{RequestID: "r1", State: RequestCancelled},
		&SessionReady{RequestID: "r1", HelperName: "alice", HelperKey: "ssh-ed25519 AAAA alice"},
		&CancelRequest{RequestID: "r1"},
		&Error{Message: "bye"},
	}
	var buf rw
	c := NewConn(&buf)
	for _, m := range msgs {
		if err := c.Send(m); err != nil {
			t.Fatalf("send %T: %v", m, err)
		}
	}
	for _, want := range msgs {
		got, err := c.Receive()
		if err != nil {
			t.Fatalf("receive %T: %v", want, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
}

func TestSendUnknown(t *testing.T) {
	if err := NewConn(&rw{}).Send(struct{}{}); err == nil {
		t.Fatal("want error for unknown type")
	}
}

func TestChannelData(t *testing.T) {
	d := HelperSessionData{RequestID: "r1", ClientName: "anna@pc1", ClientKey: "ssh-ed25519 AAAA", UserMachine: "anna@pc1"}
	got, err := UnmarshalHelperSessionData(MarshalHelperSessionData(d))
	if err != nil || got != d {
		t.Fatalf("got %+v %v", got, err)
	}
	s := SessionChannelData{RequestID: "r1"}
	gs, err := UnmarshalSessionChannelData(MarshalSessionChannelData(s))
	if err != nil || gs != s {
		t.Fatalf("got %+v %v", gs, err)
	}
}

func FuzzReceive(f *testing.F) {
	var buf rw
	NewConn(&buf).Send(&Presence{})
	f.Add(buf.Bytes())
	f.Fuzz(func(t *testing.T, b []byte) {
		c := NewConn(&rw{*bytes.NewBuffer(b)})
		for i := 0; i < 4; i++ {
			if _, err := c.Receive(); err != nil {
				return
			}
		}
	})
}
