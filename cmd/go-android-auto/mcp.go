package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bruxaodev/go-android-auto/pkg/adb"
	"github.com/bruxaodev/go-android-auto/pkg/auto"
)

const (
	mcpProtocolVersion = "2025-06-18"
	mcpServerName      = "go-android-auto"
	mcpServerVersion   = "dev"
	mcpURIScheme       = "go-android-auto"
)

type mcpServer struct {
	cfg commandConfig
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content           []mcpContent `json:"content"`
	StructuredContent any          `json:"structuredContent,omitempty"`
	IsError           bool         `json:"isError,omitempty"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type mcpResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type mcpResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
}

type mcpToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type mcpResourceReadParams struct {
	URI string `json:"uri"`
}

type mcpConfigArgs struct {
	AutomationDir              string  `json:"automation,omitempty"`
	DataDir                    string  `json:"values,omitempty"`
	ADBPath                    string  `json:"adb,omitempty"`
	DeviceSerial               string  `json:"serial,omitempty"`
	DeviceSerials              string  `json:"serials,omitempty"`
	DeviceIDs                  string  `json:"device_ids,omitempty"`
	DeviceRunMode              string  `json:"device_run_mode,omitempty"`
	DetectDeviceIDs            bool    `json:"detect_device_ids,omitempty"`
	DeviceMapPath              string  `json:"device_map,omitempty"`
	AllDevices                 bool    `json:"all_devices,omitempty"`
	TesseractPath              string  `json:"tesseract,omitempty"`
	AppiumCommand              string  `json:"appium,omitempty"`
	AppiumURL                  string  `json:"appium_url,omitempty"`
	AppiumURLs                 string  `json:"appium_urls,omitempty"`
	AppiumShards               int     `json:"appium_shards,omitempty"`
	AppiumSessionConcurrency   int     `json:"appium_session_concurrency,omitempty"`
	AppiumSystemPortBase       int     `json:"appium_system_port_base,omitempty"`
	AppiumMjpegPortBase        int     `json:"appium_mjpeg_port_base,omitempty"`
	AppiumChromedriverPortBase int     `json:"appium_chromedriver_port_base,omitempty"`
	DeviceLogDir               *string `json:"device_log_dir,omitempty"`
}

type mcpRunTimelineArgs struct {
	mcpConfigArgs
	Timeline         string `json:"timeline"`
	FallbackTimeline string `json:"fallback_timeline,omitempty"`
	Index            int    `json:"index,omitempty"`
	Timeout          string `json:"timeout,omitempty"`
}

type mcpInspectTimelineArgs struct {
	AutomationDir string `json:"automation,omitempty"`
	Timeline      string `json:"timeline"`
}

type mcpDoctorArgs struct {
	mcpConfigArgs
	Timeline string `json:"timeline,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
}

type mcpSetupDevicesArgs struct {
	mcpConfigArgs
	Timeout string `json:"timeout,omitempty"`
}

type mcpListDevicesArgs struct {
	ADBPath string `json:"adb,omitempty"`
	Timeout string `json:"timeout,omitempty"`
}

type mcpDirectActionArgs struct {
	mcpConfigArgs
	Action       string         `json:"action"`
	Then         string         `json:"then,omitempty"`
	Name         string         `json:"name,omitempty"`
	Key          string         `json:"key,omitempty"`
	Text         string         `json:"text,omitempty"`
	Find         string         `json:"find,omitempty"`
	ValueSuffix  string         `json:"value_suffix,omitempty"`
	APK          string         `json:"apk,omitempty"`
	Package      string         `json:"package,omitempty"`
	Args         []string       `json:"args,omitempty"`
	Screenshot   string         `json:"screenshot,omitempty"`
	Output       string         `json:"output,omitempty"`
	OutputKey    string         `json:"output_key,omitempty"`
	OCRLang      string         `json:"ocr_lang,omitempty"`
	OCRPSM       int            `json:"ocr_psm,omitempty"`
	AppiumURL    string         `json:"appium_url,omitempty"`
	Using        string         `json:"using,omitempty"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
	Size         string         `json:"size,omitempty"`
	X            *int           `json:"x,omitempty"`
	Y            *int           `json:"y,omitempty"`
	X1           *int           `json:"x1,omitempty"`
	Y1           *int           `json:"y1,omitempty"`
	X2           *int           `json:"x2,omitempty"`
	Y2           *int           `json:"y2,omitempty"`
	WaitBefore   string         `json:"wait_before,omitempty"`
	WaitAfter    string         `json:"wait_after,omitempty"`
	Timeout      string         `json:"timeout,omitempty"`
	Interval     string         `json:"interval,omitempty"`
	MS           *int           `json:"ms,omitempty"`
	DataIndex    int            `json:"data_index,omitempty"`
	PortIndex    int            `json:"port_index,omitempty"`
	RunTimeout   string         `json:"run_timeout,omitempty"`
}

type mcpTimelineInspection struct {
	Path                string              `json:"path"`
	FallbackPath        string              `json:"fallback_path,omitempty"`
	CommandCount        int                 `json:"command_count"`
	UsesADB             bool                `json:"uses_adb"`
	UsesOCR             bool                `json:"uses_ocr"`
	UsesAppium          bool                `json:"uses_appium"`
	ReferencedDataFiles []string            `json:"referenced_data_files"`
	Commands            []mcpCommandSummary `json:"commands"`
}

type mcpCommandSummary struct {
	Index  int      `json:"index"`
	Name   string   `json:"name,omitempty"`
	Type   string   `json:"type"`
	Action string   `json:"action"`
	Then   string   `json:"then,omitempty"`
	Fields []string `json:"fields,omitempty"`
}

type mcpActionDoc struct {
	Type        string   `json:"type"`
	Action      string   `json:"action"`
	Description string   `json:"description"`
	Required    []string `json:"required,omitempty"`
	Optional    []string `json:"optional,omitempty"`
	SideEffects []string `json:"side_effects,omitempty"`
}

func runMCPServer(ctx context.Context, cfg commandConfig, input io.Reader, output io.Writer) error {
	server := &mcpServer{cfg: cfg}
	decoder := json.NewDecoder(input)
	encoder := json.NewEncoder(output)
	for {
		var request mcpRequest
		if err := decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return encoder.Encode(mcpErrorResponse(nil, -32700, "parse error", err.Error()))
		}

		response := server.handleRequest(ctx, request)
		if response == nil {
			continue
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
}

func (s *mcpServer) handleRequest(ctx context.Context, request mcpRequest) *mcpResponse {
	if request.JSONRPC != "" && request.JSONRPC != "2.0" {
		return mcpErrorResponse(request.ID, -32600, "invalid request", "jsonrpc must be 2.0")
	}
	if strings.TrimSpace(request.Method) == "" {
		return mcpErrorResponse(request.ID, -32600, "invalid request", "method is required")
	}

	if !request.hasID() && strings.HasPrefix(request.Method, "notifications/") {
		return nil
	}

	switch request.Method {
	case "initialize":
		return mcpResultResponse(request.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"tools":     map[string]any{},
				"resources": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    mcpServerName,
				"version": mcpServerVersion,
			},
			"instructions": "Use resources/read on go-android-auto://capabilities and go-android-auto://actions before controlling devices. Tools can run ADB, OCR, Appium, setup, doctor, and full timeline automation on connected Android devices.",
		})
	case "ping":
		return mcpResultResponse(request.ID, map[string]any{})
	case "tools/list":
		return mcpResultResponse(request.ID, map[string]any{"tools": mcpTools()})
	case "tools/call":
		result, rpcErr := s.handleToolCall(ctx, request.Params)
		if rpcErr != nil {
			return mcpErrorResponse(request.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data)
		}
		return mcpResultResponse(request.ID, result)
	case "resources/list":
		resources, err := s.resources()
		if err != nil {
			return mcpErrorResponse(request.ID, -32603, "internal error", err.Error())
		}
		return mcpResultResponse(request.ID, map[string]any{"resources": resources})
	case "resources/read":
		result, rpcErr := s.handleResourceRead(request.Params)
		if rpcErr != nil {
			return mcpErrorResponse(request.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data)
		}
		return mcpResultResponse(request.ID, result)
	default:
		if !request.hasID() {
			return nil
		}
		return mcpErrorResponse(request.ID, -32601, "method not found", request.Method)
	}
}

func (r mcpRequest) hasID() bool {
	trimmed := strings.TrimSpace(string(r.ID))
	return trimmed != "" && trimmed != "null"
}

func mcpResultResponse(id json.RawMessage, result any) *mcpResponse {
	return &mcpResponse{JSONRPC: "2.0", ID: mcpResponseID(id), Result: result}
}

func mcpErrorResponse(id json.RawMessage, code int, message string, data any) *mcpResponse {
	return &mcpResponse{JSONRPC: "2.0", ID: mcpResponseID(id), Error: &mcpRPCError{Code: code, Message: message, Data: data}}
}

func mcpResponseID(id json.RawMessage) json.RawMessage {
	if strings.TrimSpace(string(id)) == "" {
		return json.RawMessage("null")
	}
	return id
}

func (s *mcpServer) handleToolCall(ctx context.Context, raw json.RawMessage) (mcpToolResult, *mcpRPCError) {
	params, err := decodeMCPParams[mcpToolCallParams](raw)
	if err != nil {
		return mcpToolResult{}, &mcpRPCError{Code: -32602, Message: "invalid params", Data: err.Error()}
	}
	switch params.Name {
	case "list_devices":
		return s.toolListDevices(ctx, params.Arguments)
	case "inspect_timeline":
		return s.toolInspectTimeline(params.Arguments)
	case "doctor":
		return s.toolDoctor(ctx, params.Arguments)
	case "run_timeline":
		return s.toolRunTimeline(ctx, params.Arguments)
	case "setup_devices":
		return s.toolSetupDevices(ctx, params.Arguments)
	case "adb_action":
		return s.toolDirectAction(ctx, params.Arguments, auto.CommandADB)
	case "ocr_action":
		return s.toolDirectAction(ctx, params.Arguments, auto.CommandOCR)
	case "appium_action":
		return s.toolDirectAction(ctx, params.Arguments, auto.CommandAppium)
	default:
		return mcpErrorTool(fmt.Errorf("unknown tool %q", params.Name)), nil
	}
}

func (s *mcpServer) toolListDevices(ctx context.Context, raw json.RawMessage) (mcpToolResult, *mcpRPCError) {
	args, err := decodeMCPParams[mcpListDevicesArgs](raw)
	if err != nil {
		return mcpToolResult{}, &mcpRPCError{Code: -32602, Message: "invalid tool arguments", Data: err.Error()}
	}
	callCtx, cancel, err := mcpContextWithTimeout(ctx, args.Timeout)
	if err != nil {
		return mcpToolResult{}, &mcpRPCError{Code: -32602, Message: "invalid timeout", Data: err.Error()}
	}
	defer cancel()
	adbPath := strings.TrimSpace(args.ADBPath)
	if adbPath == "" {
		adbPath = s.cfg.adbPath
	}
	devices, err := adb.New("", adbPath).Devices(callCtx)
	if err != nil {
		return mcpErrorTool(err), nil
	}
	return mcpJSONTool(map[string]any{"devices": devices, "count": len(devices)}), nil
}

func (s *mcpServer) toolInspectTimeline(raw json.RawMessage) (mcpToolResult, *mcpRPCError) {
	args, err := decodeMCPParams[mcpInspectTimelineArgs](raw)
	if err != nil {
		return mcpToolResult{}, &mcpRPCError{Code: -32602, Message: "invalid tool arguments", Data: err.Error()}
	}
	cfg := s.cfg
	if strings.TrimSpace(args.AutomationDir) != "" {
		cfg.automationDir = strings.TrimSpace(args.AutomationDir)
	}
	cfg.timeLinePath = strings.TrimSpace(args.Timeline)
	inspection, err := inspectTimeline(cfg)
	if err != nil {
		return mcpErrorTool(err), nil
	}
	return mcpJSONTool(inspection), nil
}

func (s *mcpServer) toolDoctor(ctx context.Context, raw json.RawMessage) (mcpToolResult, *mcpRPCError) {
	args, err := decodeMCPParams[mcpDoctorArgs](raw)
	if err != nil {
		return mcpToolResult{}, &mcpRPCError{Code: -32602, Message: "invalid tool arguments", Data: err.Error()}
	}
	cfg := args.mcpConfigArgs.apply(s.cfg)
	cfg.timeLinePath = strings.TrimSpace(args.Timeline)
	callCtx, cancel, err := mcpContextWithTimeout(ctx, args.Timeout)
	if err != nil {
		return mcpToolResult{}, &mcpRPCError{Code: -32602, Message: "invalid timeout", Data: err.Error()}
	}
	defer cancel()
	var output strings.Builder
	err = runDoctor(callCtx, cfg, &output)
	return mcpOutputTool(output.String(), map[string]any{}, err), nil
}

func (s *mcpServer) toolRunTimeline(ctx context.Context, raw json.RawMessage) (mcpToolResult, *mcpRPCError) {
	args, err := decodeMCPParams[mcpRunTimelineArgs](raw)
	if err != nil {
		return mcpToolResult{}, &mcpRPCError{Code: -32602, Message: "invalid tool arguments", Data: err.Error()}
	}
	cfg := args.mcpConfigArgs.apply(s.cfg)
	cfg.timeLinePath = strings.TrimSpace(args.Timeline)
	cfg.fallbackPath = strings.TrimSpace(args.FallbackTimeline)
	cfg.timeLineIndex = args.Index
	callCtx, cancel, err := mcpContextWithTimeout(ctx, args.Timeout)
	if err != nil {
		return mcpToolResult{}, &mcpRPCError{Code: -32602, Message: "invalid timeout", Data: err.Error()}
	}
	defer cancel()
	var output strings.Builder
	err = runCommandWithOutput(callCtx, cfg, nil, &output)
	return mcpOutputTool(output.String(), map[string]any{}, err), nil
}

func (s *mcpServer) toolSetupDevices(ctx context.Context, raw json.RawMessage) (mcpToolResult, *mcpRPCError) {
	args, err := decodeMCPParams[mcpSetupDevicesArgs](raw)
	if err != nil {
		return mcpToolResult{}, &mcpRPCError{Code: -32602, Message: "invalid tool arguments", Data: err.Error()}
	}
	cfg := args.mcpConfigArgs.apply(s.cfg)
	callCtx, cancel, err := mcpContextWithTimeout(ctx, args.Timeout)
	if err != nil {
		return mcpToolResult{}, &mcpRPCError{Code: -32602, Message: "invalid timeout", Data: err.Error()}
	}
	defer cancel()
	var output strings.Builder
	err = runDeviceSetup(callCtx, cfg, &output)
	return mcpOutputTool(output.String(), map[string]any{"device_map": cfg.deviceMapPath}, err), nil
}

func (s *mcpServer) toolDirectAction(ctx context.Context, raw json.RawMessage, commandType auto.CommandType) (mcpToolResult, *mcpRPCError) {
	args, err := decodeMCPParams[mcpDirectActionArgs](raw)
	if err != nil {
		return mcpToolResult{}, &mcpRPCError{Code: -32602, Message: "invalid tool arguments", Data: err.Error()}
	}
	if strings.TrimSpace(args.Action) == "" {
		return mcpToolResult{}, &mcpRPCError{Code: -32602, Message: "invalid tool arguments", Data: "action is required"}
	}
	cfg := args.mcpConfigArgs.apply(s.cfg)
	callCtx, cancel, err := mcpContextWithTimeout(ctx, args.RunTimeout)
	if err != nil {
		return mcpToolResult{}, &mcpRPCError{Code: -32602, Message: "invalid run_timeout", Data: err.Error()}
	}
	defer cancel()

	command := args.toCommand(commandType)
	if commandType == auto.CommandAppium && command.AppiumURL != "" && cfg.appiumURL == "" && cfg.appiumURLs == "" {
		cfg.appiumURL = command.AppiumURL
	}
	timeline := auto.Timeline{command}
	dataSet, err := auto.LoadDataDirFiles(cfg.dataDir, auto.DataFileReferences(timeline))
	if err != nil {
		return mcpErrorTool(err), nil
	}
	var output strings.Builder
	targets, err := resolveDeviceTargets(callCtx, cfg, &output)
	if err != nil {
		return mcpOutputTool(output.String(), map[string]any{"type": commandType, "action": command.Action}, err), nil
	}
	if len(targets) == 1 {
		if args.DataIndex > 0 {
			targets[0].DataIndex = args.DataIndex
		}
		if args.PortIndex > 0 {
			targets[0].PortIndex = args.PortIndex
		}
	}
	if commandType == auto.CommandAppium {
		cfg.appiumSessionLimiter = make(chan struct{}, appiumSessionConcurrency(cfg))
		pool, err := ensureAppiumServerPool(callCtx, cfg, timeline, len(targets), &output)
		if err != nil {
			return mcpOutputTool(output.String(), map[string]any{"type": commandType, "action": command.Action}, err), nil
		}
		if pool != nil {
			defer func() { _ = pool.Close() }()
			targets = assignAppiumTargets(targets, pool.URLs)
		}
	}
	err = runTimelineOnDevices(callCtx, cfg, timeline, nil, dataSet, targets, &output)
	return mcpOutputTool(output.String(), map[string]any{"type": commandType, "action": command.Action, "target_count": len(targets)}, err), nil
}

func (a mcpConfigArgs) apply(cfg commandConfig) commandConfig {
	if strings.TrimSpace(a.AutomationDir) != "" {
		cfg.automationDir = strings.TrimSpace(a.AutomationDir)
	}
	if strings.TrimSpace(a.DataDir) != "" {
		cfg.dataDir = strings.TrimSpace(a.DataDir)
	}
	if strings.TrimSpace(a.ADBPath) != "" {
		cfg.adbPath = strings.TrimSpace(a.ADBPath)
	}
	if strings.TrimSpace(a.DeviceSerial) != "" {
		cfg.deviceSerial = strings.TrimSpace(a.DeviceSerial)
	}
	if strings.TrimSpace(a.DeviceSerials) != "" {
		cfg.deviceSerials = strings.TrimSpace(a.DeviceSerials)
	}
	if strings.TrimSpace(a.DeviceIDs) != "" {
		cfg.deviceIDs = strings.TrimSpace(a.DeviceIDs)
	}
	if strings.TrimSpace(a.DeviceRunMode) != "" {
		cfg.deviceRunMode = strings.TrimSpace(a.DeviceRunMode)
	}
	if a.DetectDeviceIDs {
		cfg.detectDeviceIDs = true
	}
	if strings.TrimSpace(a.DeviceMapPath) != "" {
		cfg.deviceMapPath = strings.TrimSpace(a.DeviceMapPath)
	}
	if a.AllDevices {
		cfg.allDevices = true
	}
	if strings.TrimSpace(a.TesseractPath) != "" {
		cfg.tesseractPath = strings.TrimSpace(a.TesseractPath)
	}
	if strings.TrimSpace(a.AppiumCommand) != "" {
		cfg.appiumCommand = strings.TrimSpace(a.AppiumCommand)
	}
	if strings.TrimSpace(a.AppiumURL) != "" {
		cfg.appiumURL = strings.TrimSpace(a.AppiumURL)
	}
	if strings.TrimSpace(a.AppiumURLs) != "" {
		cfg.appiumURLs = strings.TrimSpace(a.AppiumURLs)
	}
	if a.AppiumShards > 0 {
		cfg.appiumShards = a.AppiumShards
	}
	if a.AppiumSessionConcurrency > 0 {
		cfg.appiumSessionConcurrency = a.AppiumSessionConcurrency
	}
	if a.AppiumSystemPortBase > 0 {
		cfg.appiumSystemPortBase = a.AppiumSystemPortBase
	}
	if a.AppiumMjpegPortBase > 0 {
		cfg.appiumMjpegPortBase = a.AppiumMjpegPortBase
	}
	if a.AppiumChromedriverPortBase > 0 {
		cfg.appiumChromedriverPortBase = a.AppiumChromedriverPortBase
	}
	if a.DeviceLogDir != nil {
		cfg.deviceLogDir = *a.DeviceLogDir
	}
	return cfg
}

func (a mcpDirectActionArgs) toCommand(commandType auto.CommandType) auto.Command {
	return auto.Command{
		Name:         a.Name,
		Type:         commandType,
		Action:       auto.Action(a.Action),
		Then:         auto.Action(a.Then),
		Key:          a.Key,
		Text:         a.Text,
		Find:         a.Find,
		ValueSuffix:  a.ValueSuffix,
		Apk:          a.APK,
		Package:      a.Package,
		Args:         a.Args,
		Screenshot:   a.Screenshot,
		Output:       a.Output,
		OutputKey:    a.OutputKey,
		OCRLang:      a.OCRLang,
		OCRPSM:       a.OCRPSM,
		AppiumURL:    a.AppiumURL,
		Using:        a.Using,
		Capabilities: a.Capabilities,
		Size:         a.Size,
		X:            a.X,
		Y:            a.Y,
		X1:           a.X1,
		Y1:           a.Y1,
		X2:           a.X2,
		Y2:           a.Y2,
		WaitBefore:   a.WaitBefore,
		WaitAfter:    a.WaitAfter,
		Timeout:      a.Timeout,
		Interval:     a.Interval,
		MS:           a.MS,
	}
}

func inspectTimeline(cfg commandConfig) (mcpTimelineInspection, error) {
	timelinePath, err := resolveTimelinePath(cfg)
	if err != nil {
		return mcpTimelineInspection{}, err
	}
	loaded, err := auto.LoadWithMetadata(timelinePath)
	if err != nil {
		return mcpTimelineInspection{}, err
	}
	timeline := loaded.Timeline
	dataFiles := sortedKeys(auto.DataFileReferences(timeline))
	inspection := mcpTimelineInspection{
		Path:                timelinePath,
		FallbackPath:        loaded.FallbackPath,
		CommandCount:        len(timeline),
		UsesAppium:          auto.TimelineUsesAppium(timeline),
		ReferencedDataFiles: dataFiles,
		Commands:            make([]mcpCommandSummary, 0, len(timeline)),
	}
	for i, command := range timeline {
		if command.Type == auto.CommandADB {
			inspection.UsesADB = true
		}
		if command.Type == auto.CommandOCR {
			inspection.UsesOCR = true
		}
		inspection.Commands = append(inspection.Commands, mcpCommandSummary{
			Index:  i,
			Name:   command.Name,
			Type:   string(command.Type),
			Action: string(command.Action),
			Then:   string(command.Then),
			Fields: commandFieldNames(command),
		})
	}
	return inspection, nil
}

func commandFieldNames(command auto.Command) []string {
	fields := make([]string, 0)
	add := func(name string, ok bool) {
		if ok {
			fields = append(fields, name)
		}
	}
	add("key", command.Key != "")
	add("text", command.Text != "")
	add("find", command.Find != "")
	add("value_suffix", command.ValueSuffix != "")
	add("apk", command.Apk != "")
	add("package", command.Package != "")
	add("args", len(command.Args) > 0)
	add("screenshot", command.Screenshot != "")
	add("output", command.Output != "")
	add("output_key", command.OutputKey != "")
	add("ocr_lang", command.OCRLang != "")
	add("ocr_psm", command.OCRPSM > 0)
	add("appium_url", command.AppiumURL != "")
	add("using", command.Using != "")
	add("capabilities", len(command.Capabilities) > 0)
	add("size", command.Size != "")
	add("x", command.X != nil)
	add("y", command.Y != nil)
	add("x1", command.X1 != nil)
	add("y1", command.Y1 != nil)
	add("x2", command.X2 != nil)
	add("y2", command.Y2 != nil)
	add("wait_before", command.WaitBefore != "")
	add("wait_after", command.WaitAfter != "")
	add("timeout", command.Timeout != "")
	add("interval", command.Interval != "")
	add("ms", command.MS != nil)
	add("timeline", command.TimelinePath != "")
	add("timelines", len(command.TimelinePaths) > 0)
	add("optional", command.Optional)
	sort.Strings(fields)
	return fields
}

func (s *mcpServer) resources() ([]mcpResource, error) {
	resources := []mcpResource{
		{URI: "go-android-auto://actions", Name: "Action reference", Description: "JSON reference for every ADB, OCR, Appium, and timeline action", MimeType: "application/json"},
		{URI: "go-android-auto://capabilities", Name: "Capabilities", Description: "Complete service documentation for AI control", MimeType: "text/markdown"},
		{URI: "go-android-auto://config", Name: "Current config", Description: "Resolved server config and automation paths", MimeType: "application/json"},
		{URI: "go-android-auto://device-map", Name: "Device map", Description: "Known wallpaper id to ADB serial mapping", MimeType: "application/json"},
		{URI: "go-android-auto://timeline-schema", Name: "Timeline schema", Description: "YAML command fields and variable rules", MimeType: "text/markdown"},
		{URI: "go-android-auto://values/index", Name: "Values index", Description: "CSV value files with headers and row counts only", MimeType: "application/json"},
	}
	scripts, err := loadTimelineScripts(s.cfg.automationDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	for _, script := range scripts {
		uri := "go-android-auto://automation/" + pathpkg.Clean(filepath.ToSlash(script.Name))
		resources = append(resources, mcpResource{URI: uri, Name: "automation/" + script.Name, Description: "Timeline automation YAML", MimeType: "application/x-yaml"})
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].URI < resources[j].URI })
	return resources, nil
}

func (s *mcpServer) handleResourceRead(raw json.RawMessage) (map[string]any, *mcpRPCError) {
	params, err := decodeMCPParams[mcpResourceReadParams](raw)
	if err != nil {
		return nil, &mcpRPCError{Code: -32602, Message: "invalid params", Data: err.Error()}
	}
	content, err := s.readResource(strings.TrimSpace(params.URI))
	if err != nil {
		return nil, &mcpRPCError{Code: -32602, Message: "invalid resource", Data: err.Error()}
	}
	return map[string]any{"contents": []mcpResourceContent{content}}, nil
}

func (s *mcpServer) readResource(uri string) (mcpResourceContent, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return mcpResourceContent{}, err
	}
	if parsed.Scheme != mcpURIScheme {
		return mcpResourceContent{}, fmt.Errorf("unsupported resource scheme %q", parsed.Scheme)
	}
	switch parsed.Host {
	case "actions":
		return mcpTextResource(uri, "application/json", mustJSONText(mcpActionDocs())), nil
	case "capabilities":
		return mcpTextResource(uri, "text/markdown", mcpCapabilitiesMarkdown()), nil
	case "timeline-schema":
		return mcpTextResource(uri, "text/markdown", mcpTimelineSchemaMarkdown()), nil
	case "config":
		return mcpTextResource(uri, "application/json", mustJSONText(s.configSummary())), nil
	case "device-map":
		text, err := readDeviceMapText(s.cfg.deviceMapPath)
		if err != nil {
			return mcpResourceContent{}, err
		}
		return mcpTextResource(uri, "application/json", text), nil
	case "values":
		if strings.Trim(parsed.Path, "/") != "index" {
			return mcpResourceContent{}, fmt.Errorf("only go-android-auto://values/index is exposed")
		}
		index, err := valuesIndex(s.cfg.dataDir)
		if err != nil {
			return mcpResourceContent{}, err
		}
		return mcpTextResource(uri, "application/json", mustJSONText(index)), nil
	case "automation":
		relativePath := strings.TrimPrefix(parsed.Path, "/")
		path, err := safeResourcePath(s.cfg.automationDir, relativePath)
		if err != nil {
			return mcpResourceContent{}, err
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return mcpResourceContent{}, fmt.Errorf("automation resource must be yaml")
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return mcpResourceContent{}, err
		}
		return mcpTextResource(uri, "application/x-yaml", string(content)), nil
	default:
		return mcpResourceContent{}, fmt.Errorf("unknown resource host %q", parsed.Host)
	}
}

func mcpTextResource(uri, mimeType, text string) mcpResourceContent {
	return mcpResourceContent{URI: uri, MimeType: mimeType, Text: text}
}

func (s *mcpServer) configSummary() map[string]any {
	return map[string]any{
		"automation":                       s.cfg.automationDir,
		"values":                           s.cfg.dataDir,
		"device_map":                       s.cfg.deviceMapPath,
		"adb":                              defaultText(s.cfg.adbPath, "adb"),
		"tesseract":                        defaultText(s.cfg.tesseractPath, "tesseract"),
		"appium":                           defaultText(s.cfg.appiumCommand, "appium"),
		"appium_url":                       defaultText(s.cfg.appiumURL, defaultAppiumURL),
		"appium_urls":                      s.cfg.appiumURLs,
		"appium_shards":                    s.cfg.appiumShards,
		"appium_session_concurrency":       appiumSessionConcurrency(s.cfg),
		"appium_system_port_base":          defaultInt(s.cfg.appiumSystemPortBase, defaultAppiumSystemPortBase),
		"appium_mjpeg_port_base":           defaultInt(s.cfg.appiumMjpegPortBase, defaultAppiumMjpegPortBase),
		"appium_chromedriver_port_base":    defaultInt(s.cfg.appiumChromedriverPortBase, defaultAppiumChromedriverPortBase),
		"device_log_dir":                   s.cfg.deviceLogDir,
		"default_device_run_mode":          s.cfg.deviceRunMode,
		"mcp_stdout_contains_only_jsonrpc": true,
	}
}

func valuesIndex(dataDir string) ([]map[string]any, error) {
	dataSet, err := auto.LoadDataDir(dataDir)
	if err != nil {
		return nil, err
	}
	index := make([]map[string]any, 0, len(dataSet.Files))
	for _, file := range dataSet.Files {
		index = append(index, map[string]any{
			"name":    file.Name,
			"path":    file.Path,
			"headers": file.Headers,
			"rows":    len(file.Rows),
		})
	}
	return index, nil
}

func readDeviceMapText(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "{\n  \"devices\": []\n}\n", nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "{\n  \"devices\": []\n}\n", nil
		}
		return "", err
	}
	return string(content), nil
}

func safeResourcePath(baseDir string, relativePath string) (string, error) {
	if strings.TrimSpace(baseDir) == "" {
		return "", fmt.Errorf("base directory is empty")
	}
	if strings.TrimSpace(relativePath) == "" || filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("invalid relative resource path")
	}
	for _, part := range strings.Split(relativePath, "/") {
		if part == ".." {
			return "", fmt.Errorf("resource path cannot contain ..")
		}
	}
	cleanRelative := pathpkg.Clean(relativePath)
	if cleanRelative == "." || strings.HasPrefix(cleanRelative, "../") {
		return "", fmt.Errorf("invalid relative resource path")
	}
	joined := filepath.Join(baseDir, filepath.FromSlash(cleanRelative))
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", err
	}
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	if absJoined != absBase && !strings.HasPrefix(absJoined, absBase+string(os.PathSeparator)) {
		return "", fmt.Errorf("resource path escapes base directory")
	}
	return joined, nil
}

func mcpTools() []mcpTool {
	tools := []mcpTool{
		{
			Name:        "list_devices",
			Description: "List connected ADB devices. Read-only.",
			InputSchema: objectSchema(map[string]any{"adb": stringSchema("ADB binary path"), "timeout": stringSchema("Optional Go duration, for example 10s")}),
		},
		{
			Name:        "inspect_timeline",
			Description: "Load a timeline YAML, expand includes, and report commands, data references, fallback, OCR/Appium usage.",
			InputSchema: objectSchema(map[string]any{"timeline": stringSchema("Timeline path or name under automation dir"), "automation": stringSchema("Automation directory override")}, "timeline"),
		},
		{
			Name:        "doctor",
			Description: "Run readiness checks for config, binaries, devices, and an optional selected timeline.",
			InputSchema: commonToolSchema(map[string]any{"timeline": stringSchema("Optional timeline path or name"), "timeout": stringSchema("Optional Go duration")}),
		},
		{
			Name:        "run_timeline",
			Description: "Run a full timeline automation on one or more devices. Mutates connected devices and may start Appium.",
			InputSchema: commonToolSchema(map[string]any{"timeline": stringSchema("Timeline path or name under automation dir"), "fallback_timeline": stringSchema("Fallback timeline override"), "index": integerSchema("Starting command index"), "timeout": stringSchema("Optional Go duration")}, "timeline"),
		},
		{
			Name:        "setup_devices",
			Description: "Detect wallpaper device ids with OCR and write the device map. Mutates/reads connected devices and writes config.",
			InputSchema: commonToolSchema(map[string]any{"timeout": stringSchema("Optional Go duration")}),
		},
		{
			Name:        "adb_action",
			Description: "Run one ADB action directly: home, keyevent, tap, input, swipe, text, install, clear-app, screenshot, set/reset density/size, long-press, pull, wait, shell.",
			InputSchema: directActionSchema(),
		},
		{
			Name:        "ocr_action",
			Description: "Run one OCR action directly using screenshot plus Tesseract: wait, tap, input, capture, generate-identifier.",
			InputSchema: directActionSchema(),
		},
		{
			Name:        "appium_action",
			Description: "Run one Appium action directly: start-session, quit-session, tap, input, page-source, capture, wait. May start Appium.",
			InputSchema: directActionSchema(),
		},
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

func commonToolSchema(extra map[string]any, required ...string) map[string]any {
	properties := commonProperties()
	for key, value := range extra {
		properties[key] = value
	}
	return objectSchema(properties, required...)
}

func commonProperties() map[string]any {
	return map[string]any{
		"automation":                    stringSchema("Automation directory override"),
		"values":                        stringSchema("CSV values directory override"),
		"adb":                           stringSchema("ADB binary path"),
		"serial":                        stringSchema("Single ADB serial"),
		"serials":                       stringSchema("Comma-separated ADB serials"),
		"device_ids":                    stringSchema("Comma-separated 1-based device ids matching selected serials"),
		"device_run_mode":               enumSchema("Multi-device mode", []string{deviceRunModeParallel, deviceRunModeQueue}),
		"detect_device_ids":             booleanSchema("Detect device ids from wallpaper OCR"),
		"device_map":                    stringSchema("Device map JSON path"),
		"all_devices":                   booleanSchema("Target every connected ADB device"),
		"tesseract":                     stringSchema("Tesseract binary path"),
		"appium":                        stringSchema("Appium executable path"),
		"appium_url":                    stringSchema("Appium server URL"),
		"appium_urls":                   stringSchema("Comma-separated Appium shard URLs"),
		"appium_shards":                 integerSchema("Appium shard count"),
		"appium_session_concurrency":    integerSchema("Maximum concurrent Appium session creation"),
		"appium_system_port_base":       integerSchema("Base appium:systemPort"),
		"appium_mjpeg_port_base":        integerSchema("Base appium:mjpegServerPort"),
		"appium_chromedriver_port_base": integerSchema("Base appium:chromedriverPort"),
		"device_log_dir":                stringSchema("Per-device log directory; empty disables file logs"),
	}
}

func directActionSchema() map[string]any {
	properties := commonProperties()
	for key, value := range map[string]any{
		"action":       stringSchema("Action name for the selected tool type"),
		"then":         stringSchema("Follow-up action for supported wait actions"),
		"name":         stringSchema("Log label"),
		"key":          stringSchema("ADB keyevent key"),
		"text":         stringSchema("Input text or wait duration depending on action"),
		"find":         stringSchema("OCR text, Appium selector, or capture regex"),
		"value_suffix": stringSchema("Suffix for generated identifiers"),
		"apk":          stringSchema("APK path"),
		"package":      stringSchema("Android package name"),
		"args":         arraySchema("ADB shell arguments", stringSchema("Argument")),
		"screenshot":   stringSchema("Screenshot/local path or adb pull remote path depending on action"),
		"output":       stringSchema("Output file or CSV for capture/page-source"),
		"output_key":   stringSchema("Variable key for captured output"),
		"ocr_lang":     stringSchema("Tesseract language"),
		"ocr_psm":      integerSchema("Tesseract page segmentation mode"),
		"using":        stringSchema("Appium locator strategy, default xpath"),
		"capabilities": map[string]any{"type": "object", "description": "Extra Appium capabilities"},
		"size":         stringSchema("Display size or density value"),
		"x":            integerSchema("X coordinate"),
		"y":            integerSchema("Y coordinate"),
		"x1":           integerSchema("Swipe start X"),
		"y1":           integerSchema("Swipe start Y"),
		"x2":           integerSchema("Swipe end X"),
		"y2":           integerSchema("Swipe end Y"),
		"wait_before":  stringSchema("Delay before action"),
		"wait_after":   stringSchema("Delay after action"),
		"timeout":      stringSchema("Action wait timeout"),
		"interval":     stringSchema("Action wait interval"),
		"ms":           integerSchema("Long press duration in milliseconds"),
		"data_index":   integerSchema("0-based data row/device index"),
		"port_index":   integerSchema("0-based Appium port index"),
		"run_timeout":  stringSchema("Optional whole tool call timeout"),
	} {
		properties[key] = value
	}
	return objectSchema(properties, "action")
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integerSchema(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func booleanSchema(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func enumSchema(description string, values []string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}

func arraySchema(description string, itemSchema map[string]any) map[string]any {
	return map[string]any{"type": "array", "description": description, "items": itemSchema}
}

func decodeMCPParams[T any](raw json.RawMessage) (T, error) {
	var value T
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if decoder.More() {
		return value, fmt.Errorf("unexpected extra JSON data")
	}
	return value, nil
}

func mcpContextWithTimeout(ctx context.Context, timeout string) (context.Context, context.CancelFunc, error) {
	timeout = strings.TrimSpace(timeout)
	if timeout == "" {
		return ctx, func() {}, nil
	}
	duration, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, nil, err
	}
	if duration <= 0 {
		return nil, nil, fmt.Errorf("timeout must be greater than 0")
	}
	callCtx, cancel := context.WithTimeout(ctx, duration)
	return callCtx, cancel, nil
}

func mcpJSONTool(value any) mcpToolResult {
	text := mustJSONText(value)
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: text}}, StructuredContent: value}
}

func mcpTextTool(text string, structured any, isError bool) mcpToolResult {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		if isError {
			text = "operation failed"
		} else {
			text = "ok"
		}
	}
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: text}}, StructuredContent: structured, IsError: isError}
}

func mcpOutputTool(output string, structured map[string]any, err error) mcpToolResult {
	if structured == nil {
		structured = map[string]any{}
	}
	structured["ok"] = err == nil
	if err != nil {
		structured["error"] = err.Error()
		output = strings.TrimRight(output, "\n")
		if output != "" {
			output += "\n"
		}
		output += err.Error()
	}
	return mcpTextTool(output, structured, err != nil)
}

func mcpErrorTool(err error) mcpToolResult {
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: err.Error()}}, StructuredContent: map[string]any{"ok": false, "error": err.Error()}, IsError: true}
}

func mustJSONText(value any) string {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("{\n  \"error\": %q\n}", err.Error())
	}
	return string(content) + "\n"
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func defaultText(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func defaultInt(value int, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func mcpActionDocs() []mcpActionDoc {
	return []mcpActionDoc{
		{Type: "adb", Action: "home", Description: "Send KEYCODE_HOME.", SideEffects: []string{"changes foreground app"}},
		{Type: "adb", Action: "keyevent", Description: "Send an Android key event.", Required: []string{"key"}, SideEffects: []string{"changes device input state"}},
		{Type: "adb", Action: "tap", Description: "Tap absolute screen coordinates.", Required: []string{"x", "y"}, SideEffects: []string{"touch input"}},
		{Type: "adb", Action: "input", Description: "Tap coordinates, then type text through adb input.", Required: []string{"x", "y", "text"}, SideEffects: []string{"touch input", "text input"}},
		{Type: "adb", Action: "swipe", Description: "Swipe between absolute coordinates.", Required: []string{"x1", "y1", "x2", "y2"}, SideEffects: []string{"touch input"}},
		{Type: "adb", Action: "text", Description: "Type text through adb input.", Required: []string{"text"}, SideEffects: []string{"text input"}},
		{Type: "adb", Action: "install", Description: "Install or replace an APK through package manager.", Required: []string{"apk"}, SideEffects: []string{"installs app"}},
		{Type: "adb", Action: "clear-app", Description: "Clear app data for a package.", Required: []string{"package"}, SideEffects: []string{"destructive app data reset"}},
		{Type: "adb", Action: "screenshot", Description: "Capture screenshot to a local path.", Required: []string{"screenshot"}, SideEffects: []string{"writes local file"}},
		{Type: "adb", Action: "set-density", Description: "Set display density via wm density.", Required: []string{"size"}, SideEffects: []string{"changes display settings"}},
		{Type: "adb", Action: "reset-density", Description: "Reset display density.", SideEffects: []string{"changes display settings"}},
		{Type: "adb", Action: "set-size", Description: "Set display size via wm size.", Required: []string{"size"}, SideEffects: []string{"changes display settings"}},
		{Type: "adb", Action: "reset-size", Description: "Reset display size.", SideEffects: []string{"changes display settings"}},
		{Type: "adb", Action: "long-press", Description: "Long press at absolute coordinates.", Required: []string{"x", "y", "ms"}, SideEffects: []string{"touch input"}},
		{Type: "adb", Action: "pull", Description: "Pull a remote device file to a local path. Uses screenshot as remote path and text as local path.", Required: []string{"screenshot", "text"}, SideEffects: []string{"writes local file"}},
		{Type: "adb", Action: "wait", Description: "Sleep for the duration in text.", Required: []string{"text"}},
		{Type: "adb", Action: "shell", Description: "Run adb shell with args.", Required: []string{"args"}, SideEffects: []string{"arbitrary device command"}},
		{Type: "ocr", Action: "wait", Description: "Wait for OCR text or sleep if find is empty. Supports then tap, input, capture, generate-identifier.", Optional: []string{"find", "text", "then", "timeout", "interval", "ocr_lang", "ocr_psm"}, SideEffects: []string{"screenshots", "optional touch/text/output"}},
		{Type: "ocr", Action: "tap", Description: "Find text in screenshot using Tesseract and tap its center.", Required: []string{"find"}, Optional: []string{"ocr_lang", "ocr_psm"}, SideEffects: []string{"screenshots", "touch input"}},
		{Type: "ocr", Action: "input", Description: "Find text in screenshot, tap its center, then type text.", Required: []string{"find", "text"}, Optional: []string{"ocr_lang", "ocr_psm"}, SideEffects: []string{"screenshots", "touch input", "text input"}},
		{Type: "ocr", Action: "capture", Description: "Capture a regex value from OCR text and save it to output file/CSV and variables.", Required: []string{"find", "output"}, Optional: []string{"output_key", "ocr_lang", "ocr_psm"}, SideEffects: []string{"screenshots", "writes output"}},
		{Type: "ocr", Action: "generate-identifier", Description: "Find target text, tap it, type a deterministic identifier, and save it.", Required: []string{"find", "text", "output"}, Optional: []string{"value_suffix", "output_key"}, SideEffects: []string{"screenshots", "touch input", "text input", "writes output"}},
		{Type: "appium", Action: "start-session", Description: "Create an Appium session with default and custom capabilities.", Optional: []string{"appium_url", "capabilities"}, SideEffects: []string{"starts Appium session"}},
		{Type: "appium", Action: "quit-session", Description: "Delete the active Appium session and remove forwarded ports.", SideEffects: []string{"ends Appium session"}},
		{Type: "appium", Action: "tap", Description: "Find an element and click it.", Required: []string{"find"}, Optional: []string{"using", "appium_url", "capabilities"}, SideEffects: []string{"UI input"}},
		{Type: "appium", Action: "input", Description: "Find an element, click it, and send keys.", Required: []string{"find", "text"}, Optional: []string{"using", "appium_url", "capabilities"}, SideEffects: []string{"UI input", "text input"}},
		{Type: "appium", Action: "page-source", Description: "Fetch page source and write it to output.", Required: []string{"output"}, Optional: []string{"appium_url", "capabilities"}, SideEffects: []string{"writes output"}},
		{Type: "appium", Action: "capture", Description: "Capture a regex value from Appium page source and save it.", Required: []string{"find", "output"}, Optional: []string{"output_key", "appium_url", "capabilities"}, SideEffects: []string{"writes output"}},
		{Type: "appium", Action: "wait", Description: "Wait for an Appium element or sleep if find is empty. Supports then tap, input, capture.", Optional: []string{"find", "text", "then", "timeout", "interval", "using", "appium_url", "capabilities"}, SideEffects: []string{"optional UI input/output"}},
		{Type: "timeline", Action: "include", Description: "Include another YAML timeline by using type timeline with timeline, or just timeline without type.", Required: []string{"timeline"}},
		{Type: "timeline", Action: "race", Description: "Run multiple child timelines in parallel and continue with the first successful branch.", Required: []string{"timelines"}, Optional: []string{"optional"}},
		{Type: "timeline", Action: "repeat", Description: "Repeat a child timeline until it fails, is canceled, or timeout expires.", Required: []string{"timeline"}, Optional: []string{"timeout", "interval", "optional"}},
	}
}

func mcpCapabilitiesMarkdown() string {
	var builder strings.Builder
	builder.WriteString("# go-android-auto MCP capabilities\n\n")
	builder.WriteString("This MCP server gives an AI client full control over go-android-auto through stdio JSON-RPC. Use tools/list to discover callable tools and resources/read to inspect documentation and automation files. Side-effectful tools control connected Android devices, write files, and may start/stop Appium.\n\n")
	builder.WriteString("## Core service functionality\n\n")
	builder.WriteString("- Timeline YAML automation with strict fields and {{file.column}} variable interpolation.\n")
	builder.WriteString("- ADB control for touches, text, key events, shell commands, screenshots, app install/data clear, display size/density, pull, and waits.\n")
	builder.WriteString("- OCR control using screenshots plus Tesseract for text finding, tapping, input, capture, waits, and generated identifiers.\n")
	builder.WriteString("- Appium control using UiAutomator2 sessions, element find/click/input, page-source, capture, waits, and sharded servers.\n")
	builder.WriteString("- Multi-device automation with parallel or queue mode, device id detection, device-map persistence, per-device data rows, and fallback timelines.\n")
	builder.WriteString("- CSV values under the values directory provide variables; captures can write files or CSV values.\n\n")
	builder.WriteString("## MCP tools\n\n")
	for _, tool := range mcpTools() {
		builder.WriteString("- `" + tool.Name + "`: " + tool.Description + "\n")
	}
	builder.WriteString("\n## Action reference\n\n")
	for _, doc := range mcpActionDocs() {
		builder.WriteString("- `" + doc.Type + "." + doc.Action + "`: " + doc.Description)
		if len(doc.Required) > 0 {
			builder.WriteString(" Required: `" + strings.Join(doc.Required, "`, `") + "`.")
		}
		if len(doc.Optional) > 0 {
			builder.WriteString(" Optional: `" + strings.Join(doc.Optional, "`, `") + "`.")
		}
		if len(doc.SideEffects) > 0 {
			builder.WriteString(" Side effects: " + strings.Join(doc.SideEffects, ", ") + ".")
		}
		builder.WriteString("\n")
	}
	builder.WriteString("\n## Recommended AI control flow\n\n")
	builder.WriteString("1. Call `resources/read` for `go-android-auto://capabilities` and `go-android-auto://actions`.\n")
	builder.WriteString("2. Call `list_devices`, then `doctor` for the selected timeline/device set.\n")
	builder.WriteString("3. Call `inspect_timeline` before `run_timeline` to understand Appium/OCR/data requirements.\n")
	builder.WriteString("4. Use `adb_action`, `ocr_action`, or `appium_action` for interactive step-by-step control.\n")
	builder.WriteString("5. Use `run_timeline` for complete scripted automation and `setup_devices` to build the device map.\n")
	return builder.String()
}

func mcpTimelineSchemaMarkdown() string {
	return `# Timeline YAML schema

Each command is a YAML object. Common fields:

- ` + "`name`" + `: optional log label.
- ` + "`type`" + `: ` + "`adb`" + `, ` + "`ocr`" + `, ` + "`appium`" + `, or ` + "`timeline`" + `.
- ` + "`action`" + `: action name from ` + "`go-android-auto://actions`" + `.
- ` + "`wait_before`" + ` / ` + "`wait_after`" + `: Go duration strings.
- ` + "`timeout`" + ` / ` + "`interval`" + `: wait/repeat tuning as Go duration strings.
- ` + "`optional`" + `: ignore supported command failures.
- ` + "`fallback`" + `: only on the first item; timeline to run after a device failure.

ADB fields: ` + "`key`" + `, ` + "`text`" + `, ` + "`args`" + `, ` + "`apk`" + `, ` + "`package`" + `, ` + "`screenshot`" + `, ` + "`size`" + `, ` + "`x`" + `, ` + "`y`" + `, ` + "`x1`" + `, ` + "`y1`" + `, ` + "`x2`" + `, ` + "`y2`" + `, ` + "`ms`" + `.

OCR fields: ` + "`find`" + `, ` + "`text`" + `, ` + "`then`" + `, ` + "`output`" + `, ` + "`output_key`" + `, ` + "`value_suffix`" + `, ` + "`ocr_lang`" + `, ` + "`ocr_psm`" + `.

Appium fields: ` + "`find`" + `, ` + "`text`" + `, ` + "`then`" + `, ` + "`output`" + `, ` + "`output_key`" + `, ` + "`using`" + `, ` + "`appium_url`" + `, ` + "`capabilities`" + `.

Timeline fields: ` + "`timeline`" + ` for include/repeat and ` + "`timelines`" + ` for race.

Variables use ` + "`{{file.column}}`" + ` from CSV files in the values directory. Built-in variables include ` + "`device.index`" + `, ` + "`device.id`" + `, ` + "`device.serial`" + `, and ` + "`device.port_index`" + `.
`
}
