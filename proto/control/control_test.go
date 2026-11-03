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
		&Hello{Role: RoleTechie, ServerVersion: "v1", ServerTime: now, Name: "alice"},
		&ComputerHello{AppVersion: "dev", UserMachine: "anna@pc1"},
		&Presence{Computers: []ComputerInfo{{Fingerprint: "SHA256:x", Name: "anna@pc1", Online: true,
			Request: &RequestInfo{ID: "r1", Created: now, State: RequestTaken, TakenBy: "alice"}}},
			Techies: []TechieInfo{{Name: "alice", Online: true}}},
		&RegisterComputer{Seq: 1, PublicKey: "ssh-ed25519 AAAA anna@pc1"},
		&UnregisterComputer{Seq: 2, Fingerprint: "SHA256:x"},
		&RegisterTechie{Seq: 5, PublicKey: "ssh-ed25519 AAAA bob@pc2", Name: "bob"},
		&UnregisterTechie{Seq: 6, Name: "bob"},
		&RenameComputer{Seq: 4, Fingerprint: "SHA256:x", Name: "Anna laptop"},
		&Connect{Seq: 4, Fingerprint: "SHA256:x", RequestID: "r2"},
		&Reply{Seq: 3, Error: "request is not open"},
		&Registered{},
		&RequestStateMsg{RequestID: "r1", State: RequestCancelled},
		&SessionReady{RequestID: "r1", TechieName: "alice", TechieKey: "ssh-ed25519 AAAA alice"},
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
	d := TechiesessionData{RequestID: "r1", ComputerName: "anna@pc1", ComputerKey: "ssh-ed25519 AAAA", UserMachine: "anna@pc1"}
	got, err := UnmarshalTechiesessionData(MarshalTechiesessionData(d))
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
