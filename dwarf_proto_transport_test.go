package main

import (
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestDwarfProtoTransportTakePendingPacket(t *testing.T) {
	transport := &DwarfProtoTransport{}
	first := dwarfProtoPacket{Cmd: 15267, Type: 2}
	second := dwarfProtoPacket{Cmd: dwarfProtoNotifyRecordState, Type: 2}

	transport.stashPacket(first)
	transport.stashPacket(second)

	packet, ok := transport.takePendingPacket(func(packet dwarfProtoPacket) bool {
		return uint32(packet.Cmd) == dwarfProtoNotifyRecordState
	})
	if !ok {
		t.Fatal("expected to recover matching pending packet")
	}
	if uint32(packet.Cmd) != dwarfProtoNotifyRecordState {
		t.Fatalf("unexpected packet cmd: %d", packet.Cmd)
	}

	packet, ok = transport.takePendingPacket(func(packet dwarfProtoPacket) bool {
		return packet.Cmd == first.Cmd
	})
	if !ok {
		t.Fatal("expected unmatched pending packet to remain queued")
	}
	if packet.Cmd != first.Cmd {
		t.Fatalf("unexpected packet cmd: %d", packet.Cmd)
	}
}

func TestDwarfProtoTransportCloseDoesNotWaitWhileHoldingMutex(t *testing.T) {
	closed := make(chan struct{})
	pingDone := make(chan struct{})
	transport := &DwarfProtoTransport{
		url:  "ws://test.invalid",
		conn: &websocket.Conn{},
		closeConn: func(*websocket.Conn) error {
			return nil
		},
		closed:   closed,
		pingDone: pingDone,
	}
	go func() {
		<-closed
		transport.mu.Lock()
		transport.mu.Unlock()
		close(pingDone)
	}()

	done := make(chan error, 1)
	go func() {
		done <- transport.Close()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("close transport: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close deadlocked while the shutdown goroutine waited for the transport mutex")
	}
}
