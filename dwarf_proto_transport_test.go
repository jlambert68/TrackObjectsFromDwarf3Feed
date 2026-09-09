package main

import (
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestDialDwarfWebSocketRetriesConnectionRefused(t *testing.T) {
	wantConn := &websocket.Conn{}
	config, err := websocket.NewConfig("ws://camera.test:9900", "http://localhost/")
	if err != nil {
		t.Fatalf("configure websocket: %v", err)
	}
	dialCalls := 0
	waitCalls := 0
	retryCalls := 0
	conn, err := dialDwarfWebSocketWithRetry(
		config,
		4,
		time.Millisecond,
		func(*websocket.Config) (*websocket.Conn, error) {
			dialCalls++
			if dialCalls < 3 {
				return nil, &websocket.DialError{
					Config: config,
					Err:    &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
				}
			}
			return wantConn, nil
		},
		func(time.Duration) { waitCalls++ },
		func(_, _ int, _ time.Duration, _ error) { retryCalls++ },
	)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	if conn != wantConn {
		t.Fatal("retry helper returned the wrong connection")
	}
	if dialCalls != 3 || waitCalls != 2 || retryCalls != 2 {
		t.Fatalf("unexpected retry counts: dials=%d waits=%d callbacks=%d", dialCalls, waitCalls, retryCalls)
	}
}

func TestDialDwarfWebSocketDoesNotRetryOtherErrors(t *testing.T) {
	wantErr := errors.New("invalid handshake")
	dialCalls := 0
	_, err := dialDwarfWebSocketWithRetry(
		&websocket.Config{},
		4,
		time.Millisecond,
		func(*websocket.Config) (*websocket.Conn, error) {
			dialCalls++
			return nil, wantErr
		},
		func(time.Duration) { t.Fatal("unexpected retry wait") },
		nil,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("unexpected dial error: %v", err)
	}
	if dialCalls != 1 {
		t.Fatalf("unexpected dial count: got %d want 1", dialCalls)
	}
}

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
