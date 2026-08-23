package main

import (
	"bufio"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/websocket"
)

const (
	defaultHost           = "192.168.88.1"
	defaultWSPort         = 9900
	defaultFTPPort        = 21
	defaultFTPRoot        = "/"
	defaultFTPUser        = "anonymous"
	defaultFTPPassword    = "anonymous"
	defaultDeviceID       = 1
	defaultEncodeType     = 1
	defaultPingInterval   = 5 * time.Second
	defaultRequestTimeout = 10 * time.Second
	defaultTimeout        = 10 * time.Second

	cameraTele = "tele"
	cameraWide = "wide"

	cmdTeleOpenCamera  = 10000
	cmdTeleStartRecord = 10005
	cmdTeleStopRecord  = 10006
	cmdWideOpenCamera  = 12000
	cmdWideStartRecord = 12030
	cmdWideStopRecord  = 12031
	cmdSwitchMode      = 16402
	cmdSwitchTech      = 16403
	cmdEnterCamera     = 16404

	notifyTeleFunction = 15215
	notifyWideFunction = 15216
	notifyAlbumUpdate  = 15230
	notifyRecordState  = 15275

	shootingModeVideo = 1
	shootingTechVideo = 4

	recordDownloadRoot = "/home/jlambert/egen_kod/go/go_workspace/src/jlambert/TrackObjectInDwarfLifvFeed_v1/cmd/dwarfprobe/downloads"
)

type config struct {
	Host           string
	WSPort         int
	FTPPort        int
	FTPUser        string
	FTPPassword    string
	FTPRoot        string
	Timeout        time.Duration
	RequestTimeout time.Duration
	PingInterval   time.Duration
	Debug          bool
	Camera         string
}

type mediaFile struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
}

type ftpClient struct {
	host    string
	port    int
	user    string
	pass    string
	timeout time.Duration
	ctrl    *textproto.Conn
	conn    net.Conn
}

type ftpListEntry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

type ftpError struct {
	Command string
	Code    int
	Message string
	Err     error
}

type protoPacket struct {
	MajorVersion uint64
	MinorVersion uint64
	DeviceID     uint64
	ModuleID     uint64
	Cmd          uint64
	Type         uint64
	Data         []byte
	ClientID     string
}

type protoCommand struct {
	RequestID uint32
	Cmd       uint32
	DeviceID  uint32
	Name      string
	Payload   []byte
}

type protoCommandResult struct {
	Packet      protoPacket
	ResponseHex string
}

type transport struct {
	url     string
	timeout time.Duration
	debug   bool

	clientID       string
	pingInterval   time.Duration
	requestTimeout time.Duration

	mu       sync.Mutex
	conn     *websocket.Conn
	closed   chan struct{}
	pingDone chan struct{}
	nextID   uint32
	pending  []protoPacket
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "connect":
		if err := runConnect(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "connect failed:", err)
			os.Exit(1)
		}
	case "list":
		if err := runList(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "list failed:", err)
			os.Exit(1)
		}
	case "download":
		if err := runDownload(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "download failed:", err)
			os.Exit(1)
		}
	case "record":
		if err := runRecord(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "record failed:", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `dwarfprobe exercises the DWARF websocket control path.

Usage:
  dwarfprobe connect [flags]
  dwarfprobe list [flags]
  dwarfprobe download [flags]
  dwarfprobe record [flags]

Examples:
  go run ./cmd/dwarfprobe connect -host 192.168.50.136 -debug
  go run ./cmd/dwarfprobe list -host 192.168.50.136 -ftp-root /Videos -recursive
  go run ./cmd/dwarfprobe download -host 192.168.50.136 -remote /DCIM/100MEDIA/clip.mp4 -out ./clip.mp4
  go run ./cmd/dwarfprobe record -host 192.168.50.136 -camera wide -wait 4s -debug
  go run ./cmd/dwarfprobe record -host 192.168.50.136 -camera tele -wait 4s -debug
`)
}

func runConnect(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	cfg := parseCommonFlags("connect", args)
	t := newTransport(cfg)
	defer t.Close()
	if err := t.Connect(); err != nil {
		return err
	}
	fmt.Printf("connected to ws://%s:%d client=%s\n", cfg.Host, cfg.WSPort, t.clientID)
	return nil
}

func runList(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}

	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	cfg := bindCommonFlags(fs)
	recursive := fs.Bool("recursive", true, "recursively walk directories under ftp-root")
	match := fs.String("match", "", "optional substring filter applied to remote file path")
	videoOnly := fs.Bool("video-only", false, "only return video files")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, err := openFTP(*cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	files, err := client.walkFiles(cfg.FTPRoot, *recursive)
	if err != nil {
		return err
	}

	matchText := strings.TrimSpace(*match)
	for _, file := range files {
		if *videoOnly && !isVideoFileName(file.Name) {
			continue
		}
		if matchText != "" && !strings.Contains(file.Path, matchText) {
			continue
		}
		fmt.Printf("%s\t%d\t%s\n", file.Path, file.Size, formatModTime(file.ModTime))
	}
	return nil
}

func runDownload(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}

	fs := flag.NewFlagSet("download", flag.ContinueOnError)
	cfg := bindCommonFlags(fs)
	remotePath := fs.String("remote", "", "remote FTP file path to download")
	outPath := fs.String("out", "", "local output file path (defaults to remote basename)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	remote := normalizeRemotePath(*remotePath)
	if strings.TrimSpace(*remotePath) == "" {
		return errors.New("missing required -remote path")
	}

	output := strings.TrimSpace(*outPath)
	if output == "" {
		output = path.Base(remote)
	}

	client, err := openFTP(*cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.downloadFile(remote, output); err != nil {
		return err
	}

	fmt.Printf("downloaded %s -> %s\n", remote, output)
	return nil
}

func runRecord(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	cfg := bindCommonFlags(fs)
	wait := fs.Duration("wait", 4*time.Second, "time to stay recording before stop")
	if err := fs.Parse(args); err != nil {
		return err
	}

	t := newTransport(*cfg)
	defer t.Close()

	beforeFiles, beforeErr := listVideoFiles(*cfg)
	if beforeErr != nil {
		fmt.Printf("video file listing before recording failed: %v\n", beforeErr)
	}

	if err := t.Connect(); err != nil {
		return err
	}

	recordingName := fmt.Sprintf("DWARF_PROBE_%s", time.Now().Format("20060102150405"))
	fmt.Printf("starting recording host=%s camera=%s name=%s\n", cfg.Host, cfg.Camera, recordingName)

	startResp, err := startRecordingSession(t, *cfg)
	if err != nil {
		return err
	}
	fmt.Printf("start ack cmd=%d response=%s\n", startResp.Packet.Cmd, startResp.ResponseHex)

	time.Sleep(*wait)

	fmt.Printf("stopping recording host=%s camera=%s\n", cfg.Host, cfg.Camera)
	stopResp, err := stopRecordingSession(t, *cfg)
	if err != nil {
		return err
	}
	fmt.Printf("stop ack cmd=%d response=%s\n", stopResp.Packet.Cmd, stopResp.ResponseHex)

	afterFiles, afterErr := listVideoFiles(*cfg)
	if afterErr != nil {
		fmt.Printf("video file listing after recording failed: %v\n", afterErr)
		return nil
	}

	newFiles := diffMediaFiles(beforeFiles, afterFiles)
	var filesToDownload []string
	switch len(newFiles) {
	case 0:
		if latest := latestMediaFile(afterFiles); latest != nil {
			fmt.Printf("latest video file=%s\n", latest.Path)
			filesToDownload = append(filesToDownload, latest.Path)
		} else {
			fmt.Println("recorded video file could not be identified")
		}
	case 1:
		fmt.Printf("recorded video file=%s\n", newFiles[0])
		filesToDownload = append(filesToDownload, newFiles[0])
	default:
		fmt.Printf("recorded video files=%s\n", strings.Join(newFiles, ", "))
		filesToDownload = append(filesToDownload, newFiles...)
	}

	if len(filesToDownload) == 0 {
		return nil
	}

	downloadDir := filepath.Join(recordDownloadRoot, recordingName)
	downloadedFiles, err := downloadMediaFiles(*cfg, downloadDir, filesToDownload)
	if err != nil {
		return err
	}
	for _, downloadedFile := range downloadedFiles {
		fmt.Printf("downloaded recording=%s\n", downloadedFile)
	}
	return nil
}

func bindCommonFlags(fs *flag.FlagSet) *config {
	cfg := &config{}
	fs.StringVar(&cfg.Host, "host", defaultHost, "DWARF host/IP")
	fs.IntVar(&cfg.WSPort, "ws-port", defaultWSPort, "DWARF websocket port")
	fs.IntVar(&cfg.FTPPort, "ftp-port", defaultFTPPort, "DWARF FTP port")
	fs.StringVar(&cfg.FTPUser, "ftp-user", defaultFTPUser, "DWARF FTP username")
	fs.StringVar(&cfg.FTPPassword, "ftp-password", defaultFTPPassword, "DWARF FTP password")
	fs.StringVar(&cfg.FTPRoot, "ftp-root", defaultFTPRoot, "DWARF FTP root to scan for videos")
	fs.DurationVar(&cfg.Timeout, "timeout", defaultTimeout, "socket dial timeout")
	fs.DurationVar(&cfg.RequestTimeout, "request-timeout", defaultRequestTimeout, "request/notification wait timeout")
	fs.DurationVar(&cfg.PingInterval, "ping-interval", defaultPingInterval, "websocket ping interval")
	fs.BoolVar(&cfg.Debug, "debug", false, "print websocket debug traffic")
	fs.StringVar(&cfg.Camera, "camera", cameraWide, "camera to use: tele or wide")
	return cfg
}

func parseCommonFlags(name string, args []string) config {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	cfg := bindCommonFlags(fs)
	must(fs.Parse(args))
	cfg.Camera = normalizeCamera(cfg.Camera)
	return *cfg
}

func newTransport(cfg config) *transport {
	clientID := fmt.Sprintf("sdk_%s", strconv.FormatInt(time.Now().UnixNano(), 36))
	return &transport{
		url:            fmt.Sprintf("ws://%s:%d", cfg.Host, cfg.WSPort),
		timeout:        cfg.Timeout,
		debug:          cfg.Debug,
		clientID:       clientID,
		pingInterval:   cfg.PingInterval,
		requestTimeout: cfg.RequestTimeout,
	}
}

func (t *transport) Connect() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn != nil {
		return nil
	}

	cfg, err := websocket.NewConfig(t.url, "http://localhost/")
	if err != nil {
		return fmt.Errorf("configure websocket: %w", err)
	}
	cfg.Dialer = &net.Dialer{Timeout: t.timeout}

	conn, err := websocket.DialConfig(cfg)
	if err != nil {
		return fmt.Errorf("dial websocket: %w", err)
	}

	t.conn = conn
	t.closed = make(chan struct{})
	t.pingDone = make(chan struct{})
	t.log("CONNECT %s client=%s", t.url, t.clientID)
	go t.runPingLoop()
	return nil
}

func (t *transport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn == nil {
		return nil
	}
	close(t.closed)
	conn := t.conn
	t.conn = nil
	err := conn.Close()
	<-t.pingDone
	return err
}

func (t *transport) dropConnection() error {
	t.mu.Lock()
	if t.conn == nil {
		t.mu.Unlock()
		return nil
	}
	close(t.closed)
	conn := t.conn
	pingDone := t.pingDone
	t.conn = nil
	t.mu.Unlock()
	err := conn.Close()
	<-pingDone
	return err
}

func (t *transport) stashPacket(packet protoPacket) {
	t.mu.Lock()
	t.pending = append(t.pending, packet)
	t.mu.Unlock()
}

func (t *transport) takePendingPacket(match func(protoPacket) bool) (protoPacket, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, packet := range t.pending {
		if !match(packet) {
			continue
		}
		t.pending = append(t.pending[:i], t.pending[i+1:]...)
		return packet, true
	}
	return protoPacket{}, false
}

func (t *transport) NextRequestID() uint32 {
	return atomic.AddUint32(&t.nextID, 1)
}

func (t *transport) Send(command protoCommand, allowTimeoutSuccess bool) (*protoCommandResult, error) {
	if err := t.Connect(); err != nil {
		return nil, err
	}

	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn == nil {
		return nil, errors.New("websocket is not connected")
	}

	if err := conn.SetDeadline(time.Now().Add(t.requestTimeout)); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}

	packetBytes, err := encodePacket(protoPacket{
		MajorVersion: 1,
		MinorVersion: 20,
		DeviceID:     uint64(command.DeviceID),
		ModuleID:     uint64(moduleIDForCommand(command.Cmd)),
		Cmd:          uint64(command.Cmd),
		Type:         0,
		Data:         command.Payload,
		ClientID:     t.clientID,
	})
	if err != nil {
		return nil, fmt.Errorf("encode packet: %w", err)
	}

	t.log("SEND id=%d cmd=%d name=%s payload=%s", command.RequestID, command.Cmd, command.Name, hex.EncodeToString(command.Payload))
	if err := websocket.Message.Send(conn, packetBytes); err != nil {
		_ = t.dropConnection()
		return nil, fmt.Errorf("send payload: %w", err)
	}

	for {
		var raw []byte
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			if allowTimeoutSuccess && (isNetTimeout(err) || isConnectionCloseError(err)) {
				_ = t.dropConnection()
				return &protoCommandResult{}, nil
			}
			_ = t.dropConnection()
			return nil, fmt.Errorf("receive reply: %w", err)
		}

		packet, err := decodePacket(raw)
		if err != nil {
			if isPrintableText(raw) {
				t.log("RECV text=%q", strings.TrimSpace(string(raw)))
				continue
			}
			return nil, fmt.Errorf("decode reply: %w", err)
		}
		t.log("RECV cmd=%d type=%d data=%s", packet.Cmd, packet.Type, hex.EncodeToString(packet.Data))
		if !isMatchingReply(command, packet) {
			t.stashPacket(packet)
			continue
		}
		return &protoCommandResult{Packet: packet, ResponseHex: hex.EncodeToString(packet.Data)}, nil
	}
}

func (t *transport) WaitForPacket(match func(protoPacket) bool, timeout time.Duration) (protoPacket, error) {
	if err := t.Connect(); err != nil {
		return protoPacket{}, err
	}

	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn == nil {
		return protoPacket{}, errors.New("websocket is not connected")
	}

	if timeout <= 0 {
		timeout = t.requestTimeout
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return protoPacket{}, fmt.Errorf("set deadline: %w", err)
	}

	if packet, ok := t.takePendingPacket(match); ok {
		t.log("WAIT pending cmd=%d type=%d data=%s", packet.Cmd, packet.Type, hex.EncodeToString(packet.Data))
		return packet, nil
	}

	for {
		var raw []byte
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			return protoPacket{}, fmt.Errorf("receive packet: %w", err)
		}
		packet, err := decodePacket(raw)
		if err != nil {
			if isPrintableText(raw) {
				t.log("WAIT text=%q", strings.TrimSpace(string(raw)))
				continue
			}
			return protoPacket{}, fmt.Errorf("decode packet: %w", err)
		}
		t.log("WAIT cmd=%d type=%d data=%s", packet.Cmd, packet.Type, hex.EncodeToString(packet.Data))
		if match(packet) {
			return packet, nil
		}
	}
}

func (t *transport) runPingLoop() {
	defer close(t.pingDone)
	if t.pingInterval <= 0 {
		return
	}
	ticker := time.NewTicker(t.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.mu.Lock()
			conn := t.conn
			t.mu.Unlock()
			if conn != nil {
				if err := websocket.Message.Send(conn, "ping"); err != nil {
					t.log("KEEPALIVE ERROR err=%v", err)
				}
			}
		case <-t.closed:
			return
		}
	}
}

func (t *transport) log(format string, args ...any) {
	if !t.debug {
		return
	}
	fmt.Printf("DWARF DEBUG: "+format+"\n", args...)
}

func startRecordingSession(t *transport, cfg config) (*protoCommandResult, error) {
	if _, err := t.Send(protoCommand{
		RequestID: t.NextRequestID(),
		Cmd:       cmdEnterCamera,
		DeviceID:  defaultDeviceID,
		Name:      "enter_camera",
		Payload:   encodeEnterCamera(defaultEncodeType),
	}, true); err != nil {
		return nil, err
	}
	if _, err := t.Send(protoCommand{
		RequestID: t.NextRequestID(),
		Cmd:       cmdSwitchMode,
		DeviceID:  defaultDeviceID,
		Name:      "switch_shooting_mode",
		Payload:   encodeSwitchMode(shootingModeVideo),
	}, true); err != nil {
		return nil, err
	}
	if _, err := t.Send(protoCommand{
		RequestID: t.NextRequestID(),
		Cmd:       cmdSwitchTech,
		DeviceID:  defaultDeviceID,
		Name:      "switch_shooting_tech",
		Payload:   encodeSwitchTech(shootingTechVideo),
	}, true); err != nil {
		return nil, err
	}
	time.Sleep(750 * time.Millisecond)
	if _, err := t.Send(protoCommand{
		RequestID: t.NextRequestID(),
		Cmd:       openCameraCmd(cfg.Camera),
		DeviceID:  defaultDeviceID,
		Name:      "open_camera",
		Payload:   encodeOpenCamera(cfg.Camera, false, defaultEncodeType),
	}, true); err != nil {
		return nil, err
	}
	time.Sleep(750 * time.Millisecond)
	resp, err := t.Send(protoCommand{
		RequestID: t.NextRequestID(),
		Cmd:       startRecordCmd(cfg.Camera),
		DeviceID:  defaultDeviceID,
		Name:      "start_record",
		Payload:   encodeStartRecord(defaultEncodeType),
	}, false)
	if err != nil {
		return nil, err
	}
	if err := waitForRecordingRunning(t, cfg.Camera, cfg.RequestTimeout+5*time.Second); err != nil {
		return nil, err
	}
	return resp, nil
}

func stopRecordingSession(t *transport, cfg config) (*protoCommandResult, error) {
	resp, err := t.Send(protoCommand{
		RequestID: t.NextRequestID(),
		Cmd:       stopRecordCmd(cfg.Camera),
		DeviceID:  defaultDeviceID,
		Name:      "stop_record",
	}, false)
	if err != nil {
		return nil, err
	}
	if err := waitForRecordingStopped(t, cfg.Camera, cfg.RequestTimeout+8*time.Second); err != nil {
		return nil, err
	}
	return resp, nil
}

func waitForRecordingRunning(t *transport, camera string, timeout time.Duration) error {
	_, err := t.WaitForPacket(func(packet protoPacket) bool {
		if packet.Cmd == notifyRecordState {
			return recordingStateIsRunning(packet.Data)
		}
		return packet.Cmd == uint64(functionStateCmd(camera)) && functionStateMatches(packet.Data, 4, 1)
	}, timeout)
	if err != nil {
		return fmt.Errorf("wait for recording start state: %w", err)
	}
	return nil
}

func waitForRecordingStopped(t *transport, camera string, timeout time.Duration) error {
	_, err := t.WaitForPacket(func(packet protoPacket) bool {
		if packet.Cmd == notifyAlbumUpdate {
			return true
		}
		if packet.Cmd == notifyRecordState {
			return recordingStateIsStopped(packet.Data)
		}
		return packet.Cmd == uint64(functionStateCmd(camera)) && functionStateMatches(packet.Data, 4, 0)
	}, timeout)
	if err != nil {
		return fmt.Errorf("wait for recording stop state: %w", err)
	}
	return nil
}

func normalizeCamera(camera string) string {
	switch strings.ToLower(strings.TrimSpace(camera)) {
	case cameraWide:
		return cameraWide
	default:
		return cameraTele
	}
}

func openCameraCmd(camera string) uint32 {
	if normalizeCamera(camera) == cameraWide {
		return cmdWideOpenCamera
	}
	return cmdTeleOpenCamera
}

func startRecordCmd(camera string) uint32 {
	if normalizeCamera(camera) == cameraWide {
		return cmdWideStartRecord
	}
	return cmdTeleStartRecord
}

func stopRecordCmd(camera string) uint32 {
	if normalizeCamera(camera) == cameraWide {
		return cmdWideStopRecord
	}
	return cmdTeleStopRecord
}

func functionStateCmd(camera string) uint32 {
	if normalizeCamera(camera) == cameraWide {
		return notifyWideFunction
	}
	return notifyTeleFunction
}

func encodeEnterCamera(encodeType int32) []byte {
	var clientParam []byte
	if encodeType != 0 {
		clientParam = appendProtoVarintField(clientParam, 1, uint64(encodeType))
	}
	var payload []byte
	if len(clientParam) > 0 {
		payload = appendProtoBytesField(payload, 3, clientParam)
	}
	return payload
}

func encodeOpenCamera(camera string, binning bool, encodeType int32) []byte {
	var payload []byte
	if normalizeCamera(camera) == cameraTele && binning {
		payload = appendProtoVarintField(payload, 1, 1)
	}
	if encodeType != 0 {
		payload = appendProtoVarintField(payload, 2, uint64(encodeType))
	}
	return payload
}

func encodeStartRecord(encodeType int32) []byte {
	if encodeType == 0 {
		return nil
	}
	return appendProtoVarintField(nil, 1, uint64(encodeType))
}

func encodeSwitchMode(mode int32) []byte {
	return appendProtoVarintField(nil, 1, uint64(mode))
}

func encodeSwitchTech(tech int32) []byte {
	return appendProtoVarintField(nil, 1, uint64(tech))
}

func isMatchingReply(command protoCommand, packet protoPacket) bool {
	if packet.Type != 1 && packet.Type != 3 {
		return false
	}
	return uint32(packet.Cmd) == command.Cmd
}

func moduleIDForCommand(cmd uint32) uint32 {
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
	case cmd >= 16400 && cmd <= 16599:
		return 14
	default:
		return 0
	}
}

func encodePacket(packet protoPacket) ([]byte, error) {
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

func decodePacket(data []byte) (protoPacket, error) {
	var packet protoPacket
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

func functionStateMatches(data []byte, functionID, state int32) bool {
	var gotFunctionID int32 = -1
	var gotState int32 = -1
	for len(data) > 0 {
		fieldNum, wireType, consumed, err := readProtoTag(data)
		if err != nil {
			return false
		}
		data = data[consumed:]
		switch wireType {
		case 0:
			value, read, err := readProtoVarint(data)
			if err != nil {
				return false
			}
			data = data[read:]
			switch fieldNum {
			case 2:
				gotFunctionID = int32(value)
			case 3:
				gotState = int32(value)
			}
		case 2:
			_, read, err := readProtoBytes(data)
			if err != nil {
				return false
			}
			data = data[read:]
		default:
			return false
		}
	}
	return gotFunctionID == functionID && gotState == state
}

func recordingStateIsRunning(data []byte) bool {
	state, ok := decodeNestedState(data)
	return ok && state == 1
}

func recordingStateIsStopped(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	if isImplicitStoppedRecordState(data) {
		return true
	}
	state, ok := decodeNestedState(data)
	return ok && (state == 0 || state == 2 || state == 3)
}

func isImplicitStoppedRecordState(data []byte) bool {
	fieldNum, wireType, consumed, err := readProtoTag(data)
	if err != nil || fieldNum != 2 || wireType != 0 {
		return false
	}
	value, read, err := readProtoVarint(data[consumed:])
	return err == nil && consumed+read == len(data) && value == 1
}

func decodeNestedState(data []byte) (int32, bool) {
	if state, ok := decodeStateField(data); ok {
		return state, true
	}
	for len(data) > 0 {
		fieldNum, wireType, consumed, err := readProtoTag(data)
		if err != nil {
			return 0, false
		}
		data = data[consumed:]
		switch wireType {
		case 0:
			_, read, err := readProtoVarint(data)
			if err != nil {
				return 0, false
			}
			data = data[read:]
		case 2:
			payload, read, err := readProtoBytes(data)
			if err != nil {
				return 0, false
			}
			data = data[read:]
			if fieldNum == 1 || fieldNum == 4 {
				if state, ok := decodeStateField(payload); ok {
					return state, true
				}
			}
		default:
			return 0, false
		}
	}
	return 0, false
}

func decodeStateField(data []byte) (int32, bool) {
	for len(data) > 0 {
		fieldNum, wireType, consumed, err := readProtoTag(data)
		if err != nil {
			return 0, false
		}
		data = data[consumed:]
		switch wireType {
		case 0:
			value, read, err := readProtoVarint(data)
			if err != nil {
				return 0, false
			}
			data = data[read:]
			if fieldNum == 1 {
				return int32(value), true
			}
		case 2:
			_, read, err := readProtoBytes(data)
			if err != nil {
				return 0, false
			}
			data = data[read:]
		default:
			return 0, false
		}
	}
	return 0, false
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

func isPrintableText(data []byte) bool {
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

func isNetTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isConnectionCloseError(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE)
}

func listVideoFiles(cfg config) ([]mediaFile, error) {
	client, err := openFTP(cfg)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	files, err := client.walkFiles(cfg.FTPRoot, true)
	if err != nil {
		return nil, err
	}
	filtered := files[:0]
	for _, file := range files {
		if isVideoFileName(file.Name) {
			filtered = append(filtered, file)
		}
	}
	files = filtered
	sort.Slice(files, func(i, j int) bool {
		if files[i].ModTime.Equal(files[j].ModTime) {
			return files[i].Path < files[j].Path
		}
		if files[i].ModTime.IsZero() {
			return false
		}
		if files[j].ModTime.IsZero() {
			return true
		}
		return files[i].ModTime.After(files[j].ModTime)
	})
	return files, nil
}

func diffMediaFiles(before, after []mediaFile) []string {
	seen := make(map[string]struct{}, len(before))
	for _, file := range before {
		seen[file.Path] = struct{}{}
	}
	var newFiles []string
	for _, file := range after {
		if _, ok := seen[file.Path]; ok {
			continue
		}
		newFiles = append(newFiles, file.Path)
	}
	sort.Strings(newFiles)
	return newFiles
}

func latestMediaFile(files []mediaFile) *mediaFile {
	if len(files) == 0 {
		return nil
	}
	return &files[0]
}

func downloadMediaFiles(cfg config, localDir string, remotePaths []string) ([]string, error) {
	client, err := openFTP(cfg)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return nil, fmt.Errorf("create download directory: %w", err)
	}

	downloadedFiles := make([]string, 0, len(remotePaths))
	for _, remotePath := range remotePaths {
		localPath := filepath.Join(localDir, path.Base(remotePath))
		if err := client.downloadFile(remotePath, localPath); err != nil {
			return nil, err
		}
		downloadedFiles = append(downloadedFiles, localPath)
	}
	return downloadedFiles, nil
}

func openFTP(cfg config) (*ftpClient, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.FTPPort))
	conn, err := net.DialTimeout("tcp", addr, cfg.Timeout)
	if err != nil {
		return nil, fmt.Errorf("dial dwarf ftp: %w", err)
	}

	client := &ftpClient{
		host:    cfg.Host,
		port:    cfg.FTPPort,
		user:    cfg.FTPUser,
		pass:    cfg.FTPPassword,
		timeout: cfg.Timeout,
		conn:    conn,
		ctrl:    textproto.NewConn(conn),
	}
	if _, _, err := client.readResponse("CONNECT", 220); err != nil {
		client.Close()
		return nil, err
	}
	if err := client.login(); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

func (c *ftpClient) Close() {
	if c.ctrl != nil {
		_ = c.ctrl.PrintfLine("QUIT")
		_ = c.ctrl.Close()
		c.ctrl = nil
	}
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func (c *ftpClient) login() error {
	if err := c.writeCommand("USER %s", c.user); err != nil {
		return err
	}
	code, _, err := c.readResponse("USER "+c.user, 230, 331)
	if err != nil {
		return err
	}
	if code == 331 {
		if err := c.writeCommand("PASS %s", c.pass); err != nil {
			return err
		}
		if _, _, err := c.readResponse("PASS ********", 230); err != nil {
			return err
		}
	}
	if err := c.writeCommand("TYPE I"); err != nil {
		return err
	}
	if _, _, err := c.readResponse("TYPE I", 200); err != nil {
		return err
	}
	return nil
}

func (c *ftpClient) walkFiles(root string, recursive bool) ([]mediaFile, error) {
	root = normalizeRemotePath(root)
	if err := c.ensureRemoteDir(root); err != nil {
		return nil, err
	}
	var files []mediaFile
	queue := []string{root}
	seen := map[string]struct{}{root: {}}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]

		entries, err := c.listDir(dir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Name == "." || entry.Name == ".." {
				continue
			}
			fullPath := normalizeRemotePath(path.Join(dir, entry.Name))
			if entry.IsDir {
				if !recursive {
					continue
				}
				if _, ok := seen[fullPath]; ok {
					continue
				}
				seen[fullPath] = struct{}{}
				queue = append(queue, fullPath)
				continue
			}
			files = append(files, mediaFile{
				Path:    fullPath,
				Name:    entry.Name,
				Size:    entry.Size,
				ModTime: entry.ModTime,
			})
		}
	}
	return files, nil
}

func (c *ftpClient) listDir(remoteDir string) ([]ftpListEntry, error) {
	dataConn, err := c.openPassiveDataConn()
	if err != nil {
		return nil, err
	}

	commandText := "LIST " + remoteDir
	if strings.TrimSpace(remoteDir) == "" || remoteDir == "/" {
		commandText = "LIST"
	}
	if err := c.writeRawCommand(commandText); err != nil {
		_ = dataConn.Close()
		return nil, err
	}
	if _, _, err := c.readResponse(commandText, 125, 150); err != nil {
		_ = dataConn.Close()
		if remoteDir == "/" {
			var ftpErr *ftpError
			if errors.As(err, &ftpErr) && ftpErr.Code == 550 && strings.Contains(strings.ToLower(ftpErr.Message), "permission denied") {
				return c.listDir("")
			}
		}
		return nil, err
	}

	body, err := io.ReadAll(dataConn)
	_ = dataConn.Close()
	if err != nil {
		return nil, fmt.Errorf("read ftp directory listing: %w", err)
	}
	if _, _, err := c.readResponse(commandText, 226, 250); err != nil {
		return nil, err
	}

	lines := splitFTPListing(string(body))
	entries := make([]ftpListEntry, 0, len(lines))
	for _, line := range lines {
		entry, ok := parseUnixFTPListLine(line)
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func (c *ftpClient) ensureRemoteDir(remoteDir string) error {
	remoteDir = normalizeRemotePath(remoteDir)
	if remoteDir == defaultFTPRoot {
		return nil
	}

	parentDir := path.Dir(remoteDir)
	if parentDir == "." {
		parentDir = defaultFTPRoot
	}
	base := path.Base(remoteDir)

	entries, err := c.listDir(parentDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name != base {
			continue
		}
		if !entry.IsDir {
			return fmt.Errorf("ftp path %s is not a directory", remoteDir)
		}
		return nil
	}
	return fmt.Errorf("ftp directory %s does not exist", remoteDir)
}

func (c *ftpClient) downloadFile(remotePath, localPath string) error {
	dataConn, err := c.openPassiveDataConn()
	if err != nil {
		return err
	}

	commandText := "RETR " + normalizeRemotePath(remotePath)
	if err := c.writeRawCommand(commandText); err != nil {
		_ = dataConn.Close()
		return err
	}
	if _, _, err := c.readResponse(commandText, 125, 150); err != nil {
		_ = dataConn.Close()
		return err
	}

	out, err := os.Create(localPath)
	if err != nil {
		_ = dataConn.Close()
		return fmt.Errorf("create local file: %w", err)
	}

	copyErr := error(nil)
	if _, err := io.Copy(out, dataConn); err != nil {
		copyErr = fmt.Errorf("copy ftp payload: %w", err)
	}
	closeErr := out.Close()
	_ = dataConn.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return fmt.Errorf("close local file: %w", closeErr)
	}
	if _, _, err := c.readResponse(commandText, 226, 250); err != nil {
		return err
	}
	return nil
}

func (c *ftpClient) openPassiveDataConn() (net.Conn, error) {
	if err := c.writeCommand("PASV"); err != nil {
		return nil, err
	}
	_, msg, err := c.readResponse("PASV", 227)
	if err != nil {
		return nil, err
	}
	host, port, err := parsePASVResponse(msg)
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, c.timeout)
	if err != nil {
		return nil, fmt.Errorf("dial ftp passive data connection: %w", err)
	}
	return conn, nil
}

func (c *ftpClient) writeCommand(format string, args ...any) error {
	return c.writeRawCommand(fmt.Sprintf(format, args...))
}

func (c *ftpClient) writeRawCommand(command string) error {
	if c.conn != nil {
		_ = c.conn.SetDeadline(time.Now().Add(c.timeout))
	}
	if err := c.ctrl.PrintfLine("%s", command); err != nil {
		return fmt.Errorf("send ftp command: %w", err)
	}
	return nil
}

func (c *ftpClient) readResponse(command string, expected ...int) (int, string, error) {
	if c.conn != nil {
		_ = c.conn.SetDeadline(time.Now().Add(c.timeout))
	}
	code, msg, err := c.ctrl.ReadResponse(expected[0])
	if err == nil {
		return code, msg, nil
	}
	if len(expected) < 2 {
		return code, msg, &ftpError{Command: command, Code: code, Message: msg, Err: err}
	}
	for _, candidate := range expected[1:] {
		if strings.HasPrefix(msg, strconv.Itoa(candidate)) || code == candidate {
			return code, msg, nil
		}
	}
	return code, msg, &ftpError{Command: command, Code: code, Message: msg, Err: err}
}

func (e *ftpError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code > 0 {
		return fmt.Sprintf("ftp %s failed with %d %q", e.Command, e.Code, e.Message)
	}
	if e.Err != nil {
		return fmt.Sprintf("ftp %s failed: %v", e.Command, e.Err)
	}
	return fmt.Sprintf("ftp %s failed", e.Command)
}

func parsePASVResponse(msg string) (string, int, error) {
	start := strings.IndexByte(msg, '(')
	end := strings.IndexByte(msg, ')')
	if start < 0 || end <= start {
		return "", 0, fmt.Errorf("unexpected PASV response: %q", msg)
	}
	parts := strings.Split(msg[start+1:end], ",")
	if len(parts) != 6 {
		return "", 0, fmt.Errorf("unexpected PASV address: %q", msg)
	}
	values := make([]int, 0, 6)
	for _, part := range parts {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return "", 0, fmt.Errorf("parse PASV value %q: %w", part, err)
		}
		values = append(values, value)
	}
	host := fmt.Sprintf("%d.%d.%d.%d", values[0], values[1], values[2], values[3])
	port := values[4]*256 + values[5]
	return host, port, nil
}

func parseUnixFTPListLine(line string) (ftpListEntry, bool) {
	fields := strings.Fields(line)
	if len(fields) < 9 {
		return ftpListEntry{}, false
	}
	size, err := strconv.ParseInt(fields[4], 10, 64)
	if err != nil {
		return ftpListEntry{}, false
	}
	name := strings.Join(fields[8:], " ")
	if name == "" {
		return ftpListEntry{}, false
	}
	entry := ftpListEntry{
		Name:  name,
		IsDir: strings.HasPrefix(fields[0], "d"),
		Size:  size,
	}
	if modTime, ok := parseUnixFTPModTime(fields[5], fields[6], fields[7]); ok {
		entry.ModTime = modTime
	}
	return entry, true
}

func parseUnixFTPModTime(monthField, dayField, yearOrClockField string) (time.Time, bool) {
	day, err := strconv.Atoi(dayField)
	if err != nil {
		return time.Time{}, false
	}
	month, ok := parseMonth(monthField)
	if !ok {
		return time.Time{}, false
	}
	now := time.Now()
	if strings.Contains(yearOrClockField, ":") {
		parsed, err := time.ParseInLocation("2006 Jan 2 15:04", fmt.Sprintf("%d %s %d %s", now.Year(), monthField, day, yearOrClockField), now.Location())
		if err != nil {
			return time.Time{}, false
		}
		if parsed.After(now.Add(24 * time.Hour)) {
			parsed = parsed.AddDate(-1, 0, 0)
		}
		return time.Date(parsed.Year(), month, day, parsed.Hour(), parsed.Minute(), 0, 0, parsed.Location()), true
	}
	year, err := strconv.Atoi(yearOrClockField)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(year, month, day, 0, 0, 0, 0, now.Location()), true
}

func parseMonth(value string) (time.Month, bool) {
	parsed, err := time.Parse("Jan", value)
	if err != nil {
		return 0, false
	}
	return parsed.Month(), true
}

func splitFTPListing(listing string) []string {
	scanner := bufio.NewScanner(strings.NewReader(listing))
	lines := make([]string, 0)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func isVideoFileName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".avi") || strings.HasSuffix(lower, ".mov")
}

func formatModTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Format(time.RFC3339)
}

func normalizeRemotePath(value string) string {
	if value == "" {
		return defaultFTPRoot
	}
	cleaned := path.Clean("/" + strings.TrimSpace(value))
	if cleaned == "." {
		return defaultFTPRoot
	}
	return cleaned
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
