package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPInitialize(t *testing.T) {
	output := runMCPRequests(t, commandConfig{}, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)

	response := output[0]
	require.Nil(t, response["error"])
	result := response["result"].(map[string]any)
	require.Equal(t, mcpProtocolVersion, result["protocolVersion"])
	require.Contains(t, result["capabilities"].(map[string]any), "tools")
	require.Equal(t, mcpServerName, result["serverInfo"].(map[string]any)["name"])
}

func TestMCPInitializedNotificationHasNoResponse(t *testing.T) {
	output := runMCPRequests(t, commandConfig{}, `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)

	require.Empty(t, output)
}

func TestMCPToolsListIncludesAutomationTools(t *testing.T) {
	output := runMCPRequests(t, commandConfig{}, `{"jsonrpc":"2.0","id":"tools","method":"tools/list","params":{}}`)

	tools := output[0]["result"].(map[string]any)["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	require.Contains(t, names, "run_timeline")
	require.Contains(t, names, "adb_action")
	require.Contains(t, names, "ocr_action")
	require.Contains(t, names, "appium_action")
}

func TestMCPResourcesListAndReadAutomation(t *testing.T) {
	automationDir := t.TempDir()
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(automationDir, "flow.yaml"), []byte("- type: adb\n  action: home\n"), 0o644))
	cfg := commandConfig{automationDir: automationDir, dataDir: dataDir, deviceMapPath: filepath.Join(t.TempDir(), "device-map.json")}

	listed := runMCPRequests(t, cfg, `{"jsonrpc":"2.0","id":1,"method":"resources/list","params":{}}`)
	resources := listed[0]["result"].(map[string]any)["resources"].([]any)
	var found bool
	for _, resource := range resources {
		if resource.(map[string]any)["uri"] == "go-android-auto://automation/flow.yaml" {
			found = true
		}
	}
	require.True(t, found)

	read := runMCPRequests(t, cfg, `{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"go-android-auto://automation/flow.yaml"}}`)
	contents := read[0]["result"].(map[string]any)["contents"].([]any)
	require.Contains(t, contents[0].(map[string]any)["text"], "action: home")
}

func TestMCPResourceReadRejectsTraversal(t *testing.T) {
	cfg := commandConfig{automationDir: t.TempDir(), dataDir: t.TempDir()}
	output := runMCPRequests(t, cfg, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"go-android-auto://automation/../secret.yaml"}}`)

	rpcErr := output[0]["error"].(map[string]any)
	require.EqualValues(t, -32602, rpcErr["code"])
}

func TestMCPValuesIndexDoesNotExposeRows(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "secret.csv"), []byte("token\nsuper-secret\n"), 0o644))
	cfg := commandConfig{automationDir: t.TempDir(), dataDir: dataDir}

	output := runMCPRequests(t, cfg, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"go-android-auto://values/index"}}`)
	text := output[0]["result"].(map[string]any)["contents"].([]any)[0].(map[string]any)["text"].(string)

	require.Contains(t, text, "secret")
	require.Contains(t, text, "token")
	require.NotContains(t, text, "super-secret")
}

func TestMCPListDevicesTool(t *testing.T) {
	adbPath := createDevicesADB(t, []string{"device-a", "device-b"})
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_devices","arguments":{"adb":` + strconvQuote(adbPath) + `}}}`

	output := runMCPRequests(t, commandConfig{}, request)

	result := output[0]["result"].(map[string]any)
	require.NotEqual(t, true, result["isError"])
	structured := result["structuredContent"].(map[string]any)
	require.EqualValues(t, 2, structured["count"])
}

func TestMCPInspectTimelineTool(t *testing.T) {
	automationDir := t.TempDir()
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(automationDir, "flow.yaml"), []byte("- type: adb\n  action: text\n  text: '{{search.query}}'\n"), 0o644))
	cfg := commandConfig{automationDir: automationDir, dataDir: dataDir}

	output := runMCPRequests(t, cfg, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"inspect_timeline","arguments":{"timeline":"flow.yaml"}}}`)

	structured := output[0]["result"].(map[string]any)["structuredContent"].(map[string]any)
	require.EqualValues(t, 1, structured["command_count"])
	require.Equal(t, true, structured["uses_adb"])
	require.Contains(t, structured["referenced_data_files"], "search")
}

func TestMCPRunTimelineTool(t *testing.T) {
	automationDir := t.TempDir()
	dataDir := t.TempDir()
	adbPath := createDevicesADB(t, []string{"device-a"})
	require.NoError(t, os.WriteFile(filepath.Join(automationDir, "flow.yaml"), []byte("- type: adb\n  action: shell\n  args: [ok]\n"), 0o644))
	disabledLogs := ""
	cfg := commandConfig{automationDir: automationDir, dataDir: dataDir, adbPath: adbPath, deviceSerial: "device-a", deviceLogDir: disabledLogs, deviceRunMode: deviceRunModeParallel}

	output := runMCPRequests(t, cfg, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_timeline","arguments":{"timeline":"flow.yaml"}}}`)

	result := output[0]["result"].(map[string]any)
	require.NotEqual(t, true, result["isError"])
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	require.Contains(t, text, "Starting timeline")
}

func TestMCPADBActionTargetsMultipleDevices(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "adb.log")
	adbPath := createFailingTimelineADB(t, logPath)
	cfg := commandConfig{adbPath: adbPath, deviceSerials: "device-a,device-b", deviceLogDir: "", deviceRunMode: deviceRunModeParallel}

	output := runMCPRequests(t, cfg, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"adb_action","arguments":{"action":"shell","args":["ok-main"]}}}`)

	result := output[0]["result"].(map[string]any)
	require.NotEqual(t, true, result["isError"])
	content, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(content), "-s device-a shell ok-main")
	require.Contains(t, string(content), "-s device-b shell ok-main")
}

func TestMCPUnknownMethod(t *testing.T) {
	output := runMCPRequests(t, commandConfig{}, `{"jsonrpc":"2.0","id":1,"method":"missing","params":{}}`)

	rpcErr := output[0]["error"].(map[string]any)
	require.EqualValues(t, -32601, rpcErr["code"])
}

func runMCPRequests(t *testing.T, cfg commandConfig, requests ...string) []map[string]any {
	t.Helper()
	input := strings.NewReader(strings.Join(requests, "\n") + "\n")
	var output strings.Builder
	require.NoError(t, runMCPServer(context.Background(), cfg, input, &output))

	responses := make([]map[string]any, 0)
	reader := bufio.NewReader(strings.NewReader(output.String()))
	for {
		line, err := reader.ReadString('\n')
		if errors.Is(err, io.EOF) && strings.TrimSpace(line) == "" {
			break
		}
		require.NoError(t, err)
		var response map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &response))
		responses = append(responses, response)
	}
	return responses
}

func strconvQuote(value string) string {
	content, _ := json.Marshal(value)
	return string(content)
}
