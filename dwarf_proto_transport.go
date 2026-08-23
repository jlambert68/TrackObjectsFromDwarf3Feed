package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

const (
	dwarfDefaultWSClientID      = ""
	dwarfDefaultWSPingInterval  = 5 * time.Second
	dwarfDefaultWSRequestTimout = 10 * time.Second
)

func defaultDwarfWSClientID() string {
	return fmt.Sprintf("sdk_%s", strconv.FormatInt(time.Now().UnixNano(), 36))
}

type DwarfWSProtocol string

const (
	dwarfWSProtocolLegacyJSON DwarfWSProtocol = "legacy_json"
	dwarfWSProtocolProtobuf   DwarfWSProtocol = "protobuf"
)

type DwarfProtoSessionConfig struct {
	ClientID       string
	PingInterval   time.Duration
	RequestTimeout time.Duration
}

type DwarfProtoSessionState struct {
	URL           string
	ClientID      string
	ConnectedAt   time.Time
	Protocol      DwarfWSProtocol
	NextRequestID uint32
}

type DwarfProtoCommand struct {
	RequestID   uint32
	Cmd         uint32
	DeviceID    uint32
	Name        string
	Description string
	Payload     []byte
}

type dwarfProtoPacket struct {
	MajorVersion uint64
	MinorVersion uint64
	DeviceID     uint64
	ModuleID     uint64
	Cmd          uint64
	Type         uint64
	Data         []byte
	ClientID     string
}

type DwarfProtoTransport struct {
	url     string
	timeout time.Duration
	debug   bool
	config  DwarfProtoSessionConfig

	mu       sync.Mutex
	conn     *websocket.Conn
	closed   chan struct{}
	pingDone chan struct{}
	state    DwarfProtoSessionState
	nextID   uint32
	pending  []dwarfProtoPacket
}

type DwarfProtoCommandResult struct {
	Command       DwarfProtoCommand
	Packet        dwarfProtoPacket
	Response      []byte
	ResponseHex   string
	SentAt        time.Time
	ReceivedAt    time.Time
	TimeoutMasked bool
}

type DwarfProtoPacketWaitResult struct {
	Packet      dwarfProtoPacket
	ReceivedAt  time.Time
	ResponseHex string
}

func newDwarfProtoTransport(controller DwarfController) *DwarfProtoTransport {
	config := DwarfProtoSessionConfig{
		ClientID:       controller.WSClientID,
		PingInterval:   controller.WSPingInterval,
		RequestTimeout: controller.WSRequestTimeout,
	}
	if config.ClientID == "" {
		config.ClientID = fmt.Sprintf("sdk_%s", strconv.FormatInt(time.Now().UnixNano(), 36))
	}
	if config.PingInterval <= 0 {
		config.PingInterval = dwarfDefaultWSPingInterval
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = dwarfDefaultWSRequestTimout
	}

	return &DwarfProtoTransport{
		url:     fmt.Sprintf("ws://%s:%d", controller.host(), controller.wsPort()),
		timeout: controller.timeout(),
		debug:   controller.DebugWS,
		config:  config,
	}
}

func (t *DwarfProtoTransport) Connect() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.conn != nil {
		return nil
	}

	config, err := websocket.NewConfig(t.url, "http://localhost/")
	if err != nil {
		return fmt.Errorf("configure protobuf websocket: %w", err)
	}
	config.Dialer = &net.Dialer{Timeout: t.timeout}

	conn, err := websocket.DialConfig(config)
	if err != nil {
		return fmt.Errorf("dial protobuf websocket: %w", err)
	}

	t.conn = conn
	t.closed = make(chan struct{})
	t.pingDone = make(chan struct{})
	t.state = DwarfProtoSessionState{
		URL:           t.url,
		ClientID:      t.config.ClientID,
		ConnectedAt:   time.Now(),
		Protocol:      dwarfWSProtocolProtobuf,
		NextRequestID: 1,
	}
	t.nextID = 0
	t.log("PROTO CONNECT %s client=%s", t.url, t.config.ClientID)
	go t.runPingLoop()
	return nil
}

func (t *DwarfProtoTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.conn == nil {
		return nil
	}

	close(t.closed)
	conn := t.conn
	t.conn = nil
	t.log("PROTO CLOSE %s", t.url)
	err := conn.Close()
	<-t.pingDone
	return err
}

func (t *DwarfProtoTransport) State() DwarfProtoSessionState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

func (t *DwarfProtoTransport) NextRequestID() uint32 {
	id := atomic.AddUint32(&t.nextID, 1)
	t.mu.Lock()
	t.state.NextRequestID = id + 1
	t.mu.Unlock()
	return id
}

func (t *DwarfProtoTransport) SendBinaryCommand(command DwarfProtoCommand, allowTimeoutSuccess bool) (*DwarfProtoCommandResult, error) {
	if err := t.Connect(); err != nil {
		return nil, err
	}

	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn == nil {
		return nil, errors.New("protobuf websocket is not connected")
	}

	if err := conn.SetDeadline(time.Now().Add(t.config.RequestTimeout)); err != nil {
		return nil, fmt.Errorf("set protobuf websocket deadline: %w", err)
	}

	packetBytes, err := encodeDwarfWsPacket(dwarfProtoPacket{
		MajorVersion: 1,
		MinorVersion: 20,
		DeviceID:     uint64(command.DeviceID),
		ModuleID:     uint64(dwarfModuleIDForCommand(command.Cmd)),
		Cmd:          uint64(command.Cmd),
		Type:         0,
		Data:         command.Payload,
		ClientID:     t.config.ClientID,
	})
	if err != nil {
		return nil, fmt.Errorf("encode protobuf websocket packet: %w", err)
	}

	sentAt := time.Now()
	t.log("PROTO SEND %s id=%d cmd=%d name=%s packetBytes=%d payloadHex=%s", t.url, command.RequestID, command.Cmd, command.Name, len(packetBytes), hex.EncodeToString(command.Payload))
	if err := websocket.Message.Send(conn, packetBytes); err != nil {
		t.log("PROTO WRITE ERROR %s id=%d err=%v", t.url, command.RequestID, err)
		return nil, fmt.Errorf("send protobuf websocket payload: %w", err)
	}

	for {
		var response []byte
		if err := websocket.Message.Receive(conn, &response); err != nil {
			t.log("PROTO RECV ERROR %s id=%d err=%v", t.url, command.RequestID, err)
			if allowTimeoutSuccess && isNetTimeout(err) {
				return &DwarfProtoCommandResult{
					Command:       command,
					SentAt:        sentAt,
					TimeoutMasked: true,
				}, nil
			}
			return nil, fmt.Errorf("read protobuf websocket response: %w", err)
		}

		packet, err := decodeDwarfWsPacket(response)
		if err != nil {
			if isPrintableDeviceText(response) {
				text := strings.TrimSpace(string(response))
				t.log("PROTO RECV TEXT %s id=%d text=%q", t.url, command.RequestID, text)
				continue
			}
			t.log("PROTO RECV DECODE ERROR %s id=%d err=%v hex=%s", t.url, command.RequestID, err, hex.EncodeToString(response))
			return nil, fmt.Errorf("decode protobuf websocket response: %w", err)
		}

		t.log("PROTO RECV %s id=%d cmd=%d type=%d client=%q dataHex=%s", t.url, command.RequestID, packet.Cmd, packet.Type, packet.ClientID, hex.EncodeToString(packet.Data))

		if !isMatchingProtoReply(command, packet) {
			t.log("PROTO SKIP %s id=%d expectedCmd=%d gotCmd=%d gotType=%d", t.url, command.RequestID, command.Cmd, packet.Cmd, packet.Type)
			t.stashPacket(packet)
			continue
		}

		return &DwarfProtoCommandResult{
			Command:     command,
			Packet:      packet,
			Response:    packet.Data,
			ResponseHex: hex.EncodeToString(packet.Data),
			SentAt:      sentAt,
			ReceivedAt:  time.Now(),
		}, nil
	}
}

func (t *DwarfProtoTransport) WaitForPacket(match func(dwarfProtoPacket) bool, timeout time.Duration) (*DwarfProtoPacketWaitResult, error) {
	if err := t.Connect(); err != nil {
		return nil, err
	}

	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn == nil {
		return nil, errors.New("protobuf websocket is not connected")
	}

	if timeout <= 0 {
		timeout = t.config.RequestTimeout
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("set protobuf websocket deadline: %w", err)
	}

	if packet, ok := t.takePendingPacket(match); ok {
		t.log("PROTO WAIT PENDING %s cmd=%d type=%d client=%q dataHex=%s", t.url, packet.Cmd, packet.Type, packet.ClientID, hex.EncodeToString(packet.Data))
		return &DwarfProtoPacketWaitResult{
			Packet:      packet,
			ReceivedAt:  time.Now(),
			ResponseHex: hex.EncodeToString(packet.Data),
		}, nil
	}

	for {
		var response []byte
		if err := websocket.Message.Receive(conn, &response); err != nil {
			t.log("PROTO WAIT RECV ERROR %s err=%v", t.url, err)
			return nil, fmt.Errorf("read protobuf websocket response: %w", err)
		}

		packet, err := decodeDwarfWsPacket(response)
		if err != nil {
			if isPrintableDeviceText(response) {
				text := strings.TrimSpace(string(response))
				t.log("PROTO WAIT TEXT %s text=%q", t.url, text)
				continue
			}
			t.log("PROTO WAIT DECODE ERROR %s err=%v hex=%s", t.url, err, hex.EncodeToString(response))
			return nil, fmt.Errorf("decode protobuf websocket response: %w", err)
		}

		t.log("PROTO WAIT RECV %s cmd=%d type=%d client=%q dataHex=%s", t.url, packet.Cmd, packet.Type, packet.ClientID, hex.EncodeToString(packet.Data))
		if !match(packet) {
			continue
		}

		return &DwarfProtoPacketWaitResult{
			Packet:      packet,
			ReceivedAt:  time.Now(),
			ResponseHex: hex.EncodeToString(packet.Data),
		}, nil
	}
}

func (t *DwarfProtoTransport) stashPacket(packet dwarfProtoPacket) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = append(t.pending, packet)
}

func (t *DwarfProtoTransport) takePendingPacket(match func(dwarfProtoPacket) bool) (dwarfProtoPacket, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, packet := range t.pending {
		if !match(packet) {
			continue
		}
		t.pending = append(t.pending[:i], t.pending[i+1:]...)
		return packet, true
	}
	return dwarfProtoPacket{}, false
}

func (t *DwarfProtoTransport) runPingLoop() {
	defer close(t.pingDone)

	if t.config.PingInterval <= 0 {
		return
	}

	ticker := time.NewTicker(t.config.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.mu.Lock()
			conn := t.conn
			t.mu.Unlock()
			if conn != nil {
				if err := websocket.Message.Send(conn, "ping"); err != nil {
					t.log("PROTO KEEPALIVE ERROR %s client=%s err=%v", t.url, t.config.ClientID, err)
				} else {
					t.log("PROTO KEEPALIVE %s client=%s", t.url, t.config.ClientID)
				}
			}
		case <-t.closed:
			return
		}
	}
}

func (t *DwarfProtoTransport) log(format string, args ...any) {
	if !t.debug {
		return
	}
	fmt.Fprintf(os.Stdout, "DWARF DEBUG: "+format+"\n", args...)
}

func isMatchingProtoReply(command DwarfProtoCommand, packet dwarfProtoPacket) bool {
	if packet.Type != 1 && packet.Type != 3 {
		return false
	}
	return uint32(packet.Cmd) == command.Cmd
}

func dwarfModuleIDForCommand(cmd uint32) uint32 {
	switch {
	case cmd >= 10000 && cmd <= 10499:
		return 1
	case cmd >= 12000 && cmd <= 12499:
		return 2
	case cmd >= 11000 && cmd <= 11499:
		return 3
	case cmd >= 13000 && cmd <= 13299:
		return 4
	case cmd >= 13500 && cmd <= 13799:
		return 5
	case cmd >= 14000 && cmd <= 14499:
		return 6
	case cmd >= 14800 && cmd <= 14899:
		return 7
	case cmd >= 15000 && cmd <= 15199:
		return 8
	case cmd >= 15200 && cmd <= 15499:
		return 9
	case cmd >= 15500 && cmd <= 15599:
		return 10
	case cmd >= 15700 && cmd <= 15799:
		return 11
	case cmd >= 16100 && cmd <= 16399:
		return 13
	case cmd >= 16400 && cmd <= 16599:
		return 14
	case cmd >= 16700 && cmd <= 16799:
		return 15
	default:
		return 0
	}
}

func encodeDwarfWsPacket(packet dwarfProtoPacket) ([]byte, error) {
	var buf []byte
	buf = appendProtoVarintField(buf, 1, packet.MajorVersion)
	buf = appendProtoVarintField(buf, 2, packet.MinorVersion)
	buf = appendProtoVarintField(buf, 3, packet.DeviceID)
	buf = appendProtoVarintField(buf, 4, packet.ModuleID)
	buf = appendProtoVarintField(buf, 5, packet.Cmd)
	buf = appendProtoVarintField(buf, 6, packet.Type)
	buf = appendProtoBytesField(buf, 7, packet.Data)
	buf = appendProtoStringField(buf, 8, packet.ClientID)
	return buf, nil
}

func decodeDwarfWsPacket(data []byte) (dwarfProtoPacket, error) {
	var packet dwarfProtoPacket
	for len(data) > 0 {
		fieldNum, wireType, n, err := readProtoTag(data)
		if err != nil {
			return packet, err
		}
		data = data[n:]

		switch wireType {
		case 0:
			value, consumed, err := readProtoVarint(data)
			if err != nil {
				return packet, err
			}
			data = data[consumed:]
			switch fieldNum {
			case 1:
				packet.MajorVersion = value
			case 2:
				packet.MinorVersion = value
			case 3:
				packet.DeviceID = value
			case 4:
				packet.ModuleID = value
			case 5:
				packet.Cmd = value
			case 6:
				packet.Type = value
			}
		case 2:
			payload, consumed, err := readProtoBytes(data)
			if err != nil {
				return packet, err
			}
			data = data[consumed:]
			switch fieldNum {
			case 7:
				packet.Data = payload
			case 8:
				packet.ClientID = string(payload)
			}
		default:
			return packet, fmt.Errorf("unsupported protobuf wire type %d", wireType)
		}
	}
	return packet, nil
}

func appendProtoVarintField(dst []byte, fieldNum int, value uint64) []byte {
	dst = appendProtoTag(dst, fieldNum, 0)
	return appendProtoVarint(dst, value)
}

func appendProtoBytesField(dst []byte, fieldNum int, value []byte) []byte {
	dst = appendProtoTag(dst, fieldNum, 2)
	dst = appendProtoVarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func appendProtoStringField(dst []byte, fieldNum int, value string) []byte {
	return appendProtoBytesField(dst, fieldNum, []byte(value))
}

func appendProtoTag(dst []byte, fieldNum int, wireType int) []byte {
	return appendProtoVarint(dst, uint64(fieldNum<<3|wireType))
}

func appendProtoVarint(dst []byte, value uint64) []byte {
	for value >= 0x80 {
		dst = append(dst, byte(value)|0x80)
		value >>= 7
	}
	return append(dst, byte(value))
}

func isPrintableDeviceText(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	for _, b := range data {
		if b == '\n' || b == '\r' || b == '\t' {
			continue
		}
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	return true
}

func readProtoTag(data []byte) (fieldNum int, wireType int, consumed int, err error) {
	value, consumed, err := readProtoVarint(data)
	if err != nil {
		return 0, 0, 0, err
	}
	return int(value >> 3), int(value & 0x7), consumed, nil
}

func readProtoVarint(data []byte) (uint64, int, error) {
	var value uint64
	for i := 0; i < len(data) && i < 10; i++ {
		b := data[i]
		value |= uint64(b&0x7f) << (7 * i)
		if b < 0x80 {
			return value, i + 1, nil
		}
	}
	if len(data) == 0 {
		return 0, 0, io.ErrUnexpectedEOF
	}
	return 0, 0, errors.New("invalid protobuf varint")
}

func readProtoBytes(data []byte) ([]byte, int, error) {
	length, consumed, err := readProtoVarint(data)
	if err != nil {
		return nil, 0, err
	}
	if uint64(len(data[consumed:])) < length {
		return nil, 0, io.ErrUnexpectedEOF
	}
	start := consumed
	end := consumed + int(length)
	return append([]byte(nil), data[start:end]...), end, nil
}
