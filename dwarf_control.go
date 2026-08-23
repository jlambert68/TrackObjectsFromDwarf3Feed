package main

import (
	"bufio"
	"encoding/json"
	"errors"
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
	"time"

	"golang.org/x/net/websocket"
)

const (
	dwarfDefaultHost        = "192.168.50.136"
	dwarfDefaultWSPort      = 9900
	dwarfDefaultFTPPort     = 21
	dwarfTelephotoCameraID  = 0
	dwarfWideCameraID       = 1
	dwarfDefaultFTPRoot     = "/Videos"
	dwarfDefaultFTPUser     = "anonymous"
	dwarfDefaultFTPPassword = "anonymous"
)

const (
	dwarfProtoTeleOpenCameraCmd  = 10000
	dwarfProtoTeleStartRecordCmd = 10005
	dwarfProtoTeleStopRecordCmd  = 10006
	dwarfProtoWideOpenCameraCmd  = 12000
	dwarfProtoWideStartRecordCmd = 12030
	dwarfProtoWideStopRecordCmd  = 12031
	dwarfProtoTaskSwitchModeCmd  = 16402
	dwarfProtoTaskSwitchTechCmd  = 16403
	dwarfProtoTaskEnterCameraCmd = 16404
	dwarfProtoDefaultDeviceID    = 1
	dwarfProtoDefaultEncodeType  = 1
	dwarfProtoNotifyTeleFunction = 15215
	dwarfProtoNotifyWideFunction = 15216
	dwarfProtoNotifyAlbumUpdate  = 15230
	dwarfProtoNotifyRecordState  = 15275
)

const (
	dwarfCameraTele = "tele"
	dwarfCameraWide = "wide"
)

const (
	dwarfShootingModeVideo = 1
)

const (
	dwarfShootingTechVideo = 4
)

type DwarfController struct {
	Host             string
	WSPort           int
	FTPPort          int
	FTPUser          string
	FTPPassword      string
	FTPRoot          string
	Timeout          time.Duration
	DebugWS          bool
	WSClientID       string
	WSPingInterval   time.Duration
	WSRequestTimeout time.Duration
}

type DwarfMediaFile struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
}

type DwarfCommandResponse struct {
	Command int
	Raw     json.RawMessage
}

type DwarfConnectionReport struct {
	Host            string
	WSPort          int
	FTPPort         int
	WebSocketOK     bool
	WebSocketDetail string
	FTPOK           bool
	FTPDetail       string
}

type DwarfRecordStartReport struct {
	Host           string
	Camera         string
	RecordingName  string
	StartAckOK     bool
	StartAckDetail string
	StartAckRaw    string
	StopAckOK      bool
	StopAckDetail  string
	StopAckRaw     string
	BeforeCount    int
	AfterCount     int
	NewFiles       []string
	WaitDuration   time.Duration
	ListBeforeErr  string
	ListAfterErr   string
}

type DwarfRawWSReport struct {
	Host                string
	WSPort              int
	Payload             string
	AllowTimeoutSuccess bool
	ResponseRaw         string
	Err                 string
}

type dwarfFTPError struct {
	Command string
	Code    int
	Message string
	Err     error
}

func (e *dwarfFTPError) Error() string {
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

func (e *dwarfFTPError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func DefaultDwarfController() DwarfController {
	return DwarfController{
		Host:             dwarfDefaultHost,
		WSPort:           dwarfDefaultWSPort,
		FTPPort:          dwarfDefaultFTPPort,
		FTPUser:          dwarfDefaultFTPUser,
		FTPPassword:      dwarfDefaultFTPPassword,
		FTPRoot:          dwarfDefaultFTPRoot,
		Timeout:          10 * time.Second,
		DebugWS:          false,
		WSClientID:       defaultDwarfWSClientID(),
		WSPingInterval:   dwarfDefaultWSPingInterval,
		WSRequestTimeout: dwarfDefaultWSRequestTimout,
	}
}

func (c DwarfController) StartVideoRecording(camera, name string) (*DwarfCommandResponse, error) {
	if name == "" {
		name = fmt.Sprintf("DWARF_%s", time.Now().Format("20060102150405"))
	}

	transport := newDwarfProtoTransport(c)
	defer transport.Close()

	return c.startVideoRecordingSession(transport, camera, name)
}

func (c DwarfController) startVideoRecordingSession(transport *DwarfProtoTransport, camera, name string) (*DwarfCommandResponse, error) {
	if name == "" {
		name = fmt.Sprintf("DWARF_%s", time.Now().Format("20060102150405"))
	}

	if _, err := c.sendProtoCommandBestEffort(transport, DwarfProtoCommand{
		RequestID: transport.NextRequestID(),
		Cmd:       dwarfProtoTaskEnterCameraCmd,
		DeviceID:  dwarfProtoDefaultDeviceID,
		Name:      "enter_camera",
		Payload:   encodeProtoEnterCamera(dwarfProtoDefaultEncodeType),
	}); err != nil {
		return nil, err
	}

	if _, err := c.sendProtoCommandBestEffort(transport, DwarfProtoCommand{
		RequestID: transport.NextRequestID(),
		Cmd:       dwarfProtoTaskSwitchModeCmd,
		DeviceID:  dwarfProtoDefaultDeviceID,
		Name:      "switch_shooting_mode",
		Payload:   encodeProtoSwitchShootingMode(dwarfShootingModeVideo),
	}); err != nil {
		return nil, err
	}

	if _, err := c.sendProtoCommandBestEffort(transport, DwarfProtoCommand{
		RequestID: transport.NextRequestID(),
		Cmd:       dwarfProtoTaskSwitchTechCmd,
		DeviceID:  dwarfProtoDefaultDeviceID,
		Name:      "switch_shooting_tech",
		Payload:   encodeProtoSwitchShootingTech(dwarfShootingTechVideo),
	}); err != nil {
		return nil, err
	}
	time.Sleep(750 * time.Millisecond)

	if _, err := c.sendProtoCommandBestEffort(transport, DwarfProtoCommand{
		RequestID: transport.NextRequestID(),
		Cmd:       dwarfProtoOpenCameraCmd(camera),
		DeviceID:  dwarfProtoDefaultDeviceID,
		Name:      "open_camera",
		Payload:   encodeProtoOpenCamera(camera, false, dwarfProtoDefaultEncodeType),
	}); err != nil {
		return nil, err
	}
	time.Sleep(750 * time.Millisecond)

	resp, err := c.sendProtoCommand(transport, DwarfProtoCommand{
		RequestID: transport.NextRequestID(),
		Cmd:       dwarfProtoStartRecordCmd(camera),
		DeviceID:  dwarfProtoDefaultDeviceID,
		Name:      "start_record",
		Payload:   encodeProtoStartRecord(dwarfProtoDefaultEncodeType),
	})
	if err != nil {
		return nil, err
	}
	if err := c.waitForRecordingRunning(transport, camera); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c DwarfController) StopVideoRecording(camera string) (*DwarfCommandResponse, error) {
	transport := newDwarfProtoTransport(c)
	defer transport.Close()

	resp, err := c.sendProtoCommand(transport, DwarfProtoCommand{
		RequestID: transport.NextRequestID(),
		Cmd:       dwarfProtoStopRecordCmd(camera),
		DeviceID:  dwarfProtoDefaultDeviceID,
		Name:      "stop_record",
		Payload:   nil,
	})
	if err != nil {
		return nil, err
	}
	if err := c.waitForRecordingStopped(transport, camera); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c DwarfController) ListVideoFiles() ([]DwarfMediaFile, error) {
	client, err := c.openFTP()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	root := c.FTPRoot
	if root == "" {
		root = dwarfDefaultFTPRoot
	}

	files, err := client.walkVideoFiles(root)
	if err != nil {
		return nil, err
	}

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

func (c DwarfController) DownloadFile(remotePath, localPath string) error {
	client, err := c.openFTP()
	if err != nil {
		return err
	}
	defer client.Close()
	return client.downloadFile(remotePath, localPath)
}

func (c DwarfController) DeleteFile(remotePath string) error {
	client, err := c.openFTP()
	if err != nil {
		return err
	}
	defer client.Close()
	return client.deleteFile(remotePath)
}

func (c DwarfController) TestConnections() DwarfConnectionReport {
	report := DwarfConnectionReport{
		Host:    c.host(),
		WSPort:  c.wsPort(),
		FTPPort: c.ftpPort(),
	}

	if err := c.testWebSocketReachability(); err != nil {
		report.WebSocketDetail = err.Error()
	} else {
		report.WebSocketOK = true
		report.WebSocketDetail = "websocket handshake succeeded"
	}

	if err := c.testFTPReachability(); err != nil {
		report.FTPDetail = err.Error()
	} else {
		report.FTPOK = true
		report.FTPDetail = "ftp login succeeded"
	}

	return report
}

func (c DwarfController) TestRecordStart(camera string, wait time.Duration) DwarfRecordStartReport {
	report := DwarfRecordStartReport{
		Host:          c.host(),
		Camera:        normalizeDwarfCamera(camera),
		RecordingName: fmt.Sprintf("DWARF_TEST_%s", time.Now().Format("20060102150405")),
		WaitDuration:  wait,
	}

	beforeFiles, err := c.ListVideoFiles()
	if err != nil {
		report.ListBeforeErr = err.Error()
	} else {
		report.BeforeCount = len(beforeFiles)
	}

	transport := newDwarfProtoTransport(c)
	defer transport.Close()

	startResp, err := c.startVideoRecordingSession(transport, camera, report.RecordingName)
	if err != nil {
		report.StartAckDetail = err.Error()
	} else {
		report.StartAckOK = true
		report.StartAckDetail = "start recording acknowledged"
		report.StartAckRaw = formatDwarfRawResponse(startResp)

		time.Sleep(wait)

		stopResp, stopErr := c.stopVideoRecordingSession(transport, camera)
		if stopErr != nil {
			report.StopAckDetail = stopErr.Error()
		} else {
			report.StopAckOK = true
			report.StopAckDetail = "stop recording acknowledged"
			report.StopAckRaw = formatDwarfRawResponse(stopResp)
		}
	}

	afterFiles, err := c.ListVideoFiles()
	if err != nil {
		report.ListAfterErr = err.Error()
	} else {
		report.AfterCount = len(afterFiles)
		report.NewFiles = diffDwarfMediaFiles(beforeFiles, afterFiles)
	}

	return report
}

func (c DwarfController) stopVideoRecordingSession(transport *DwarfProtoTransport, camera string) (*DwarfCommandResponse, error) {
	resp, err := c.sendProtoCommand(transport, DwarfProtoCommand{
		RequestID: transport.NextRequestID(),
		Cmd:       dwarfProtoStopRecordCmd(camera),
		DeviceID:  dwarfProtoDefaultDeviceID,
		Name:      "stop_record",
		Payload:   nil,
	})
	if err != nil {
		return nil, err
	}
	if err := c.waitForRecordingStopped(transport, camera); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c DwarfController) waitForRecordingRunning(transport *DwarfProtoTransport, camera string) error {
	timeout := c.WSRequestTimeout + 5*time.Second
	_, err := transport.WaitForPacket(func(packet dwarfProtoPacket) bool {
		if packet.Cmd == uint64(dwarfProtoNotifyRecordState) {
			return recordingStateIsRunning(packet.Data)
		}
		return packet.Cmd == uint64(dwarfProtoFunctionStateCmd(camera)) && functionStateMatches(packet.Data, 4, 1)
	}, timeout)
	if err != nil {
		return fmt.Errorf("wait for recording start state: %w", err)
	}
	return nil
}

func (c DwarfController) waitForRecordingStopped(transport *DwarfProtoTransport, camera string) error {
	timeout := c.WSRequestTimeout + 8*time.Second
	_, err := transport.WaitForPacket(func(packet dwarfProtoPacket) bool {
		if packet.Cmd == uint64(dwarfProtoNotifyAlbumUpdate) {
			return true
		}
		if packet.Cmd == uint64(dwarfProtoNotifyRecordState) {
			return recordingStateIsStopped(packet.Data)
		}
		return packet.Cmd == uint64(dwarfProtoFunctionStateCmd(camera)) && functionStateMatches(packet.Data, 4, 0)
	}, timeout)
	if err != nil {
		return fmt.Errorf("wait for recording stop state: %w", err)
	}
	return nil
}

func dwarfProtoFunctionStateCmd(camera string) uint32 {
	switch normalizeDwarfCamera(camera) {
	case dwarfCameraWide:
		return dwarfProtoNotifyWideFunction
	default:
		return dwarfProtoNotifyTeleFunction
	}
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
	state, ok := decodeNestedProtoState(data)
	return ok && state == 1
}

func recordingStateIsStopped(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	if isImplicitStoppedRecordState(data) {
		return true
	}
	state, ok := decodeNestedProtoState(data)
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

func decodeNestedProtoState(data []byte) (int32, bool) {
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

func (c DwarfController) SendRawWSCommand(payload string, allowTimeoutSuccess bool) DwarfRawWSReport {
	report := DwarfRawWSReport{
		Host:                c.host(),
		WSPort:              c.wsPort(),
		Payload:             payload,
		AllowTimeoutSuccess: allowTimeoutSuccess,
	}

	response, err := c.sendWSRawCommand(payload, allowTimeoutSuccess)
	if err != nil {
		report.Err = err.Error()
		return report
	}
	report.ResponseRaw = formatDwarfRawResponse(response)
	return report
}

func (c DwarfController) sendProtoCommand(transport *DwarfProtoTransport, command DwarfProtoCommand) (*DwarfCommandResponse, error) {
	result, err := transport.SendBinaryCommand(command, false)
	if err != nil {
		return nil, err
	}

	response, err := decodeProtoComResponse(result.Response)
	if err != nil {
		return nil, fmt.Errorf("decode %s response: %w", command.Name, err)
	}
	if response.Code != 0 {
		return nil, fmt.Errorf("%s failed with code %d", command.Name, response.Code)
	}

	return &DwarfCommandResponse{
		Command: int(command.Cmd),
		Raw:     append(json.RawMessage(nil), result.Response...),
	}, nil
}

func (c DwarfController) sendProtoCommandBestEffort(transport *DwarfProtoTransport, command DwarfProtoCommand) (*DwarfCommandResponse, error) {
	result, err := transport.SendBinaryCommand(command, true)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return &DwarfCommandResponse{Command: int(command.Cmd)}, nil
		}
		return nil, err
	}
	if len(result.Response) == 0 {
		return &DwarfCommandResponse{Command: int(command.Cmd)}, nil
	}
	return c.sendProtoCommandResponse(command, result.Response)
}

func (c DwarfController) sendProtoCommandResponse(command DwarfProtoCommand, responseBytes []byte) (*DwarfCommandResponse, error) {
	response, err := decodeProtoComResponse(responseBytes)
	if err != nil {
		return nil, fmt.Errorf("decode %s response: %w", command.Name, err)
	}
	if response.Code != 0 {
		return nil, fmt.Errorf("%s failed with code %d", command.Name, response.Code)
	}

	return &DwarfCommandResponse{
		Command: int(command.Cmd),
		Raw:     append(json.RawMessage(nil), responseBytes...),
	}, nil
}

func dwarfProtoOpenCameraCmd(camera string) uint32 {
	switch normalizeDwarfCamera(camera) {
	case dwarfCameraWide:
		return dwarfProtoWideOpenCameraCmd
	default:
		return dwarfProtoTeleOpenCameraCmd
	}
}

func dwarfProtoStartRecordCmd(camera string) uint32 {
	switch normalizeDwarfCamera(camera) {
	case dwarfCameraWide:
		return dwarfProtoWideStartRecordCmd
	default:
		return dwarfProtoTeleStartRecordCmd
	}
}

func dwarfProtoStopRecordCmd(camera string) uint32 {
	switch normalizeDwarfCamera(camera) {
	case dwarfCameraWide:
		return dwarfProtoWideStopRecordCmd
	default:
		return dwarfProtoTeleStopRecordCmd
	}
}

type dwarfProtoComResponse struct {
	Code int32
}

func decodeProtoComResponse(data []byte) (dwarfProtoComResponse, error) {
	var response dwarfProtoComResponse
	for len(data) > 0 {
		fieldNum, wireType, consumed, err := readProtoTag(data)
		if err != nil {
			return response, err
		}
		data = data[consumed:]

		switch wireType {
		case 0:
			value, read, err := readProtoVarint(data)
			if err != nil {
				return response, err
			}
			data = data[read:]
			if fieldNum == 1 {
				response.Code = int32(value)
			}
		case 2:
			_, read, err := readProtoBytes(data)
			if err != nil {
				return response, err
			}
			data = data[read:]
		default:
			return response, fmt.Errorf("unsupported response wire type %d", wireType)
		}
	}
	return response, nil
}

func encodeProtoEnterCamera(encodeType int32) []byte {
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

func encodeProtoOpenCamera(camera string, binning bool, encodeType int32) []byte {
	var payload []byte
	if normalizeDwarfCamera(camera) == dwarfCameraTele && binning {
		payload = appendProtoVarintField(payload, 1, 1)
	}
	if encodeType != 0 {
		payload = appendProtoVarintField(payload, 2, uint64(encodeType))
	}
	return payload
}

func encodeProtoStartRecord(encodeType int32) []byte {
	if encodeType == 0 {
		return nil
	}
	return appendProtoVarintField(nil, 1, uint64(encodeType))
}

func encodeProtoSwitchShootingMode(mode int32) []byte {
	return appendProtoVarintField(nil, 1, uint64(mode))
}

func encodeProtoSwitchShootingTech(tech int32) []byte {
	return appendProtoVarintField(nil, 1, uint64(tech))
}

func (c DwarfController) sendWSCommand(command map[string]any, allowTimeoutSuccess bool) (*DwarfCommandResponse, error) {
	payload, err := json.Marshal(command)
	if err != nil {
		return nil, fmt.Errorf("marshal dwarf command: %w", err)
	}
	return c.sendWSPayload(payload, allowTimeoutSuccess)
}

func (c DwarfController) sendWSRawCommand(payload string, allowTimeoutSuccess bool) (*DwarfCommandResponse, error) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return nil, errors.New("raw websocket payload must not be empty")
	}

	var normalized json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &normalized); err != nil {
		return nil, fmt.Errorf("decode raw websocket payload: %w", err)
	}
	return c.sendWSPayload([]byte(normalized), allowTimeoutSuccess)
}

func (c DwarfController) sendWSPayload(payload []byte, allowTimeoutSuccess bool) (*DwarfCommandResponse, error) {
	url := fmt.Sprintf("ws://%s:%d", c.host(), c.wsPort())
	config, err := websocket.NewConfig(url, "http://localhost/")
	if err != nil {
		return nil, fmt.Errorf("configure dwarf websocket: %w", err)
	}
	config.Dialer = &net.Dialer{Timeout: c.timeout()}

	conn, err := websocket.DialConfig(config)
	if err != nil {
		return nil, fmt.Errorf("dial dwarf websocket: %w", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(c.timeout())); err != nil {
		return nil, fmt.Errorf("set websocket deadline: %w", err)
	}
	c.logWSDebug("WS SEND %s %s", url, string(payload))
	if _, err := conn.Write(payload); err != nil {
		c.logWSDebug("WS WRITE ERROR %s %v", url, err)
		return nil, fmt.Errorf("send dwarf command: %w", err)
	}

	var response json.RawMessage
	if err := websocket.Message.Receive(conn, &response); err != nil {
		c.logWSDebug("WS RECV ERROR %s %v", url, err)
		if allowTimeoutSuccess && isNetTimeout(err) {
			return &DwarfCommandResponse{
				Command: commandInterfaceFromPayload(payload),
			}, nil
		}
		if isNetTimeout(err) {
			return nil, fmt.Errorf("read dwarf websocket response: %w (no acknowledgement from DWARF; verify the host IP, confirm the DWARFLAB app has started a live view session, and check that the device is not busy with another capture mode)", err)
		}
		return nil, fmt.Errorf("read dwarf websocket response: %w", err)
	}
	c.logWSDebug("WS RECV %s %s", url, string(response))

	return &DwarfCommandResponse{
		Command: commandInterfaceFromPayload(payload),
		Raw:     response,
	}, nil
}

func (c DwarfController) testWebSocketReachability() error {
	url := fmt.Sprintf("ws://%s:%d", c.host(), c.wsPort())
	config, err := websocket.NewConfig(url, "http://localhost/")
	if err != nil {
		return fmt.Errorf("configure websocket: %w", err)
	}
	config.Dialer = &net.Dialer{Timeout: c.timeout()}

	conn, err := websocket.DialConfig(config)
	if err != nil {
		return fmt.Errorf("dial websocket: %w", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(c.timeout())); err != nil {
		return fmt.Errorf("set websocket deadline: %w", err)
	}
	return nil
}

func formatDwarfRawResponse(response *DwarfCommandResponse) string {
	if response == nil || len(response.Raw) == 0 {
		return ""
	}
	return string(response.Raw)
}

func diffDwarfMediaFiles(before, after []DwarfMediaFile) []string {
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

func (c DwarfController) testFTPReachability() error {
	client, err := c.openFTP()
	if err != nil {
		return err
	}
	defer client.Close()
	return nil
}

func commandInterfaceFromPayload(payload []byte) int {
	var command map[string]any
	if err := json.Unmarshal(payload, &command); err != nil {
		return 0
	}
	return commandInterface(command)
}

func (c DwarfController) logWSDebug(format string, args ...any) {
	if !c.DebugWS {
		return
	}
	fmt.Fprintf(os.Stdout, "DWARF DEBUG: "+format+"\n", args...)
}

func commandInterface(command map[string]any) int {
	value, ok := command["interface"]
	if !ok {
		return 0
	}
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func (c DwarfController) host() string {
	if c.Host != "" {
		return c.Host
	}
	return dwarfDefaultHost
}

func (c DwarfController) wsPort() int {
	if c.WSPort > 0 {
		return c.WSPort
	}
	return dwarfDefaultWSPort
}

func (c DwarfController) ftpPort() int {
	if c.FTPPort > 0 {
		return c.FTPPort
	}
	return dwarfDefaultFTPPort
}

func (c DwarfController) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 10 * time.Second
}

func isNetTimeout(err error) bool {
	type timeout interface {
		Timeout() bool
	}
	netErr, ok := err.(timeout)
	return ok && netErr.Timeout()
}

type dwarfFTPClient struct {
	host    string
	port    int
	user    string
	pass    string
	timeout time.Duration
	ctrl    *textproto.Conn
	conn    net.Conn
}

func (c DwarfController) openFTP() (*dwarfFTPClient, error) {
	addr := net.JoinHostPort(c.host(), strconv.Itoa(c.ftpPort()))
	conn, err := net.DialTimeout("tcp", addr, c.timeout())
	if err != nil {
		return nil, fmt.Errorf("dial dwarf ftp: %w", err)
	}

	client := &dwarfFTPClient{
		host:    c.host(),
		port:    c.ftpPort(),
		user:    valueOrDefault(c.FTPUser, dwarfDefaultFTPUser),
		pass:    valueOrDefault(c.FTPPassword, dwarfDefaultFTPPassword),
		timeout: c.timeout(),
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

func (c *dwarfFTPClient) Close() {
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

func (c *dwarfFTPClient) login() error {
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

func (c *dwarfFTPClient) walkVideoFiles(root string) ([]DwarfMediaFile, error) {
	root = normalizeRemotePath(root)
	if err := c.ensureRemoteDir(root); err != nil {
		return nil, err
	}
	var files []DwarfMediaFile
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
				if _, ok := seen[fullPath]; ok {
					continue
				}
				seen[fullPath] = struct{}{}
				queue = append(queue, fullPath)
				continue
			}
			if !isVideoFileName(entry.Name) {
				continue
			}
			files = append(files, DwarfMediaFile{
				Path:    fullPath,
				Name:    entry.Name,
				Size:    entry.Size,
				ModTime: entry.ModTime,
			})
		}
	}
	return files, nil
}

func (c *dwarfFTPClient) listDir(remoteDir string) ([]ftpListEntry, error) {
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
			var ftpErr *dwarfFTPError
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

func (c *dwarfFTPClient) ensureRemoteDir(remoteDir string) error {
	remoteDir = normalizeRemotePath(remoteDir)
	if remoteDir == dwarfDefaultFTPRoot {
		return nil
	}

	parentDir := path.Dir(remoteDir)
	if parentDir == "." {
		parentDir = dwarfDefaultFTPRoot
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

func (c *dwarfFTPClient) downloadFile(remotePath, localPath string) error {
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

	if err := os.MkdirAll(pathDir(localPath), 0755); err != nil {
		_ = dataConn.Close()
		return fmt.Errorf("create local download directory: %w", err)
	}

	file, err := os.Create(localPath)
	if err != nil {
		_ = dataConn.Close()
		return fmt.Errorf("create local file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, dataConn); err != nil {
		_ = dataConn.Close()
		return fmt.Errorf("copy ftp download to local file: %w", err)
	}
	_ = dataConn.Close()

	if _, _, err := c.readResponse(commandText, 226, 250); err != nil {
		return err
	}
	return nil
}

func (c *dwarfFTPClient) deleteFile(remotePath string) error {
	commandText := "DELE " + normalizeRemotePath(remotePath)
	if err := c.writeRawCommand(commandText); err != nil {
		return err
	}
	if _, _, err := c.readResponse(commandText, 250); err != nil {
		return err
	}
	return nil
}

func (c *dwarfFTPClient) openPassiveDataConn() (net.Conn, error) {
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

func (c *dwarfFTPClient) writeCommand(format string, args ...any) error {
	return c.writeRawCommand(fmt.Sprintf(format, args...))
}

func (c *dwarfFTPClient) writeRawCommand(command string) error {
	if c.conn != nil {
		_ = c.conn.SetDeadline(time.Now().Add(c.timeout))
	}
	if err := c.ctrl.PrintfLine("%s", command); err != nil {
		return fmt.Errorf("send ftp command: %w", err)
	}
	return nil
}

func (c *dwarfFTPClient) readResponse(command string, expected ...int) (int, string, error) {
	if c.conn != nil {
		_ = c.conn.SetDeadline(time.Now().Add(c.timeout))
	}
	code, msg, err := c.ctrl.ReadResponse(expected[0])
	if err == nil {
		return code, msg, nil
	}
	if len(expected) < 2 {
		return code, msg, &dwarfFTPError{Command: command, Code: code, Message: msg, Err: err}
	}

	for _, candidate := range expected[1:] {
		if strings.HasPrefix(msg, strconv.Itoa(candidate)) || code == candidate {
			return code, msg, nil
		}
	}
	return code, msg, &dwarfFTPError{Command: command, Code: code, Message: msg, Err: err}
}

type ftpListEntry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
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

func normalizeRemotePath(value string) string {
	if value == "" {
		return dwarfDefaultFTPRoot
	}
	cleaned := path.Clean("/" + strings.TrimSpace(value))
	if cleaned == "." {
		return dwarfDefaultFTPRoot
	}
	return cleaned
}

func pathDir(localPath string) string {
	dir := filepath.Dir(localPath)
	if dir == "." {
		return ""
	}
	return dir
}

func valueOrDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func dwarfCameraID(camera string) int {
	switch normalizeDwarfCamera(camera) {
	case dwarfCameraWide:
		return dwarfWideCameraID
	default:
		return dwarfTelephotoCameraID
	}
}

func normalizeDwarfCamera(camera string) string {
	switch strings.ToLower(strings.TrimSpace(camera)) {
	case dwarfCameraWide:
		return dwarfCameraWide
	default:
		return dwarfCameraTele
	}
}

func dwarfCameraFilePrefixes(camera string) []string {
	switch normalizeDwarfCamera(camera) {
	case dwarfCameraWide:
		return []string{"DWARF3_WIDE_", "DWARF_WIDE_"}
	default:
		return []string{"DWARF3_TELE_", "DWARF_TELE_"}
	}
}
