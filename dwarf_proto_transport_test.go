package main

import "testing"

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
