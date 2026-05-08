package auto

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bruxaodev/go-android-auto/pkg/adb"
	"github.com/bruxaodev/go-android-auto/pkg/ocr"
	"github.com/stretchr/testify/require"
)

func TestCommandResolveVariables(t *testing.T) {
	cmd := Command{
		Name:      "device {{device.id}}",
		Text:      "{{professores.nome.first}}",
		Args:      []string{"settings", "put", "global", "http_proxy", "{{proxy.address}}"},
		AppiumURL: "{{appium.url}}",
		Using:     "{{selector.strategy}}",
		Output:    "{{output.path}}",
		Capabilities: map[string]any{
			"platformName":          "Android",
			"appium:automationName": "UiAutomator2",
			"appium:udid":           "{{device.serial}}",
			"appium:options": map[string]any{
				"appPackage": "{{app.package}}",
				"noReset":    true,
			},
			"appium:otherArgs": []any{"{{app.arg}}", 2},
		},
	}

	resolved, err := cmd.resolve(map[string]string{
		"device.id":              "1",
		"device.serial":          "emulator-5554",
		"professores.nome.first": "João",
		"proxy.address":          "130.180.253.101:9102",
		"appium.url":             "http://127.0.0.1:4723",
		"selector.strategy":      "accessibility id",
		"output.path":            ".tmp/source.xml",
		"app.package":            "com.example",
		"app.arg":                "value",
	})
	require.NoError(t, err)
	require.Equal(t, "device 1", resolved.Name)
	require.Equal(t, "João", resolved.Text)
	require.Equal(t, "http://127.0.0.1:4723", resolved.AppiumURL)
	require.Equal(t, "accessibility id", resolved.Using)
	require.Equal(t, ".tmp/source.xml", resolved.Output)
	require.Equal(t, []string{"settings", "put", "global", "http_proxy", "130.180.253.101:9102"}, resolved.Args)
	require.Equal(t, "emulator-5554", resolved.Capabilities["appium:udid"])
	require.Equal(t, map[string]any{"appPackage": "com.example", "noReset": true}, resolved.Capabilities["appium:options"])
	require.Equal(t, []any{"value", 2}, resolved.Capabilities["appium:otherArgs"])
	require.Equal(t, []string{"settings", "put", "global", "http_proxy", "{{proxy.address}}"}, cmd.Args)
	require.Equal(t, "{{device.serial}}", cmd.Capabilities["appium:udid"])
}

func TestCommandResolveVariablesMissing(t *testing.T) {
	_, err := resolveVariables("{{proxy.user}}", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing variable(s): proxy.user")
}

func TestLoadAppiumTimelineFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeline.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
- name: start appium
  type: appium
  action: start-session
  appium_url: http://127.0.0.1:4723
  capabilities:
    appium:appPackage: com.example
- name: tap login
  type: appium
  action: tap
  using: accessibility id
  find: Login
`), 0o644))

	timeline, err := Load(path)

	require.NoError(t, err)
	require.Len(t, timeline, 2)
	require.Equal(t, CommandAppium, timeline[0].Type)
	require.Equal(t, ActionStartSession, timeline[0].Action)
	require.Equal(t, "http://127.0.0.1:4723", timeline[0].AppiumURL)
	require.Equal(t, "com.example", timeline[0].Capabilities["appium:appPackage"])
	require.Equal(t, "accessibility id", timeline[1].Using)
	require.Equal(t, "Login", timeline[1].Find)
}

func TestLoadExpandsNestedTimeline(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "utils"), 0o755))
	path := filepath.Join(dir, "gmail-create-account.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
- name: set resolution
  type: adb
  action: set-size
  size: 1080x2400
- name: open clear gmail
  type: timeline
  timeline: utils/open-clear-gmail.yaml
- name: reset resolution
  type: adb
  action: reset-size
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "utils", "open-clear-gmail.yaml"), []byte(`
- name: open gmail
  type: adb
  action: shell
  args:
    - monkey
    - -p
    - com.google.android.gm
- name: clear gmail
  type: adb
  action: clear-app
  package: com.google.android.gm
`), 0o644))

	timeline, err := Load(path)

	require.NoError(t, err)
	require.Len(t, timeline, 4)
	require.Equal(t, "set resolution", timeline[0].Name)
	require.Equal(t, "open gmail", timeline[1].Name)
	require.Equal(t, ActionShell, timeline[1].Action)
	require.Equal(t, []string{"monkey", "-p", "com.google.android.gm"}, timeline[1].Args)
	require.Equal(t, "clear gmail", timeline[2].Name)
	require.Equal(t, ActionClearApp, timeline[2].Action)
	require.Equal(t, "reset resolution", timeline[3].Name)
}

func TestLoadWithMetadataExtractsFallbackDirective(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gmail-create-account.yaml")
	relativeFallback := filepath.Join("utils", "reset-display.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
- fallback: utils/reset-display.yaml
- name: home
  type: adb
  action: home
`), 0o644))

	loaded, err := LoadWithMetadata(path)

	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, relativeFallback), loaded.FallbackPath)
	require.Len(t, loaded.Timeline, 1)
	require.Equal(t, "home", loaded.Timeline[0].Name)
	require.Empty(t, loaded.Timeline[0].FallbackPath)
}

func TestLoadRejectsFallbackDirectiveAfterFirstItem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeline.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
- name: home
  type: adb
  action: home
- fallback: reset-display.yaml
`), 0o644))

	_, err := LoadWithMetadata(path)

	require.Error(t, err)
	require.Contains(t, err.Error(), "fallback is only supported on the first timeline item")
}

func TestAutomationSearchGoogleKeepsResetDisplayAsFallbackOnly(t *testing.T) {
	path := filepath.Join("..", "..", "automation", "search-google.yaml")

	loaded, err := LoadWithMetadata(path)

	require.NoError(t, err)
	expectedFallback, err := filepath.Abs(filepath.Join("..", "..", "automation", "utils", "reset-display.yaml"))
	require.NoError(t, err)
	require.Equal(t, expectedFallback, loaded.FallbackPath)
	for _, cmd := range loaded.Timeline {
		require.NotEqual(t, ActionResetSize, cmd.Action)
		require.NotEqual(t, ActionResetDensity, cmd.Action)
		require.NotEqual(t, "reset resolution", cmd.Name)
		require.NotEqual(t, "reset density", cmd.Name)
		if cmd.Type == CommandADB {
			require.Empty(t, cmd.Then)
		}
	}
}

func TestAutomationSearchGoogleExpandsNestedTimelines(t *testing.T) {
	path := filepath.Join("..", "..", "automation", "search-google.yaml")

	timeline, err := Load(path)

	require.NoError(t, err)
	require.Len(t, timeline, 8)
	require.Equal(t, []string{
		"set resolution",
		"set density",
		"set 3-button navigation",
		"set proxy",
		"home",
		"clear chrome data",
		"open google search",
		"search google",
	}, commandNames(timeline))
	for _, cmd := range timeline {
		require.NotEqual(t, CommandTimeline, cmd.Type)
	}
	require.Equal(t, ActionText, timeline[len(timeline)-1].Action)
}

func TestAutomationChromeSearchUsesTextAction(t *testing.T) {
	path := filepath.Join("..", "..", "automation", "utils", "chrome", "search.yaml")
	timeline, err := Load(path)
	require.NoError(t, err)
	require.Len(t, timeline, 1)
	require.Equal(t, CommandADB, timeline[0].Type)
	require.Equal(t, ActionText, timeline[0].Action)
	require.Equal(t, "{{search.query}}", timeline[0].Text)
	require.Empty(t, timeline[0].Args)

	events := timelineEvents{}
	runner := Runner{
		Device: &recordingDevice{events: &events},
		Ocr:    fakeOCR{},
		Variables: map[string]string{
			"search.query": "What is the capital of France?",
		},
	}

	err = runner.Run(context.Background(), timeline)

	require.NoError(t, err)
	require.Equal(t, []string{"adb text What is the capital of France?"}, events.values())
}

func TestLoadRejectsTimelineIncludeCycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "root.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
- type: timeline
  timeline: child.yaml
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "child.yaml"), []byte(`
- type: timeline
  timeline: root.yaml
`), 0o644))

	_, err := Load(path)

	require.Error(t, err)
	require.Contains(t, err.Error(), "timeline include cycle")
}

func TestRunnerInterleavesADBAndAppiumActions(t *testing.T) {
	events := timelineEvents{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /session":
			var body struct {
				Capabilities struct {
					AlwaysMatch map[string]any `json:"alwaysMatch"`
				} `json:"capabilities"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "device-a", body.Capabilities.AlwaysMatch["appium:udid"])
			require.Equal(t, float64(8201), body.Capabilities.AlwaysMatch["appium:systemPort"])
			require.Equal(t, float64(9201), body.Capabilities.AlwaysMatch["appium:mjpegServerPort"])
			require.Equal(t, float64(10201), body.Capabilities.AlwaysMatch["appium:chromedriverPort"])
			require.Equal(t, float64(120000), body.Capabilities.AlwaysMatch["appium:adbExecTimeout"])
			require.Equal(t, float64(120000), body.Capabilities.AlwaysMatch["appium:androidInstallTimeout"])
			require.Equal(t, float64(120000), body.Capabilities.AlwaysMatch["appium:uiautomator2ServerInstallTimeout"])
			require.Equal(t, float64(120000), body.Capabilities.AlwaysMatch["appium:uiautomator2ServerLaunchTimeout"])
			require.Equal(t, "com.example", body.Capabilities.AlwaysMatch["appium:appPackage"])
			events.append("appium session")
			_, _ = w.Write([]byte(`{"value":{"sessionId":"session-1","capabilities":{}}}`))
		case "POST /session/session-1/element":
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "accessibility id", body["using"])
			require.Equal(t, "Login", body["value"])
			events.append("appium find Login")
			_, _ = w.Write([]byte(`{"value":{"element-6066-11e4-a52e-4f735466cecf":"element-1"}}`))
		case "POST /session/session-1/element/element-1/click":
			events.append("appium click")
			_, _ = w.Write([]byte(`{"value":null}`))
		case "DELETE /session/session-1":
			events.append("appium delete")
			_, _ = w.Write([]byte(`{"value":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	x := 10
	y := 20
	runner := Runner{
		Device:          &recordingDevice{events: &events},
		Ocr:             fakeOCR{},
		AppiumServerURL: server.URL,
		Variables: map[string]string{
			"device.index":  "1",
			"device.serial": "device-a",
		},
	}

	err := runner.Run(context.Background(), Timeline{
		{Type: CommandADB, Action: ActionTap, X: &x, Y: &y},
		{Type: CommandAppium, Action: ActionTap, Using: "accessibility id", Find: "Login", Capabilities: map[string]any{"appium:appPackage": "com.example"}},
		{Type: CommandADB, Action: ActionText, Text: "done"},
	})

	require.NoError(t, err)
	require.Equal(t, []string{
		"adb tap 10 20",
		"appium session",
		"appium find Login",
		"appium click",
		"adb text done",
		"appium delete",
	}, events.values())
}

func TestRunnerWaitsForOCRTextThenTaps(t *testing.T) {
	events := timelineEvents{}
	engine := &eventuallyOCR{failures: 2}
	runner := Runner{
		Device: &recordingDevice{events: &events},
		Ocr:    engine,
	}

	err := runner.Run(context.Background(), Timeline{
		{Type: CommandOCR, Action: ActionWait, Then: ActionTap, Find: "Create account", Timeout: "200ms", Interval: "1ms"},
	})

	require.NoError(t, err)
	require.Equal(t, 3, engine.attempts)
	require.Equal(t, []string{"adb screenshot", "adb screenshot", "adb screenshot", "adb tap 0 0"}, events.values())
}

func TestRunnerTapsIndexedRepeatedOCRTarget(t *testing.T) {
	events := timelineEvents{}
	runner := Runner{
		Device: &recordingDevice{events: &events},
		Ocr: targetOCR{
			targets: map[string]bool{`[3]."palavra"`: true},
			bounds: map[string]ocr.Bounds{
				`[3]."palavra"`: {Left: 80, Top: 160, Right: 120, Bottom: 184},
			},
		},
	}

	err := runner.Run(context.Background(), Timeline{
		{Type: CommandOCR, Action: ActionTap, Find: `[3]."palavra"`},
	})

	require.NoError(t, err)
	require.Equal(t, []string{"adb screenshot", "adb tap 100 172"}, events.values())
}

func TestRunnerRunFromReportsAbsoluteCommandIndex(t *testing.T) {
	runner := Runner{
		Device: &recordingDevice{},
		Ocr:    fakeOCR{},
	}

	err := runner.RunFrom(context.Background(), Timeline{
		{Type: CommandADB, Action: ActionHome, Name: "first"},
		{Type: CommandADB, Action: "missing", Name: "second"},
	}, 1)

	require.Error(t, err)
	require.Contains(t, err.Error(), "command 1 (second) failed")
}

func TestRunnerCapturesOCRRegexToOutput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture.txt")
	runner := Runner{
		Device: &recordingDevice{},
		Ocr: textOCR{
			text: "Google\nCreate an email address\nO yoshiharukohayakawa09@gmail.com\n",
		},
	}

	err := runner.Run(context.Background(), Timeline{
		{Type: CommandOCR, Action: ActionCapture, Find: `([A-Za-z0-9._%+-]+@gmail\.com)`, Output: output},
	})

	require.NoError(t, err)
	content, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "yoshiharukohayakawa09@gmail.com\n", string(content))
}

func TestRunnerCapturesOCRRegexToDeviceCSV(t *testing.T) {
	output := filepath.Join(t.TempDir(), "gmail.csv")
	runner := Runner{
		Device: &recordingDevice{},
		Ocr: textOCR{
			text: "O yoshiharukohayakawa09@gmail.com\n",
		},
		Variables: map[string]string{
			"device.id":     "2",
			"device.serial": "emulator-5556",
		},
	}

	err := runner.Run(context.Background(), Timeline{
		{Type: CommandOCR, Action: ActionCapture, Find: `([A-Za-z0-9._%+-]+@gmail\.com)`, Output: output},
	})

	require.NoError(t, err)
	content, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "id,device,value\n2,emulator-5556,yoshiharukohayakawa09@gmail.com\n", string(content))
}

func TestRunnerWaitCapturesOCRRegexToDeviceCSV(t *testing.T) {
	output := filepath.Join(t.TempDir(), "gmail.csv")
	runner := Runner{
		Device: &recordingDevice{},
		Ocr: textOCR{
			text: "Google\nO yoshiharukohayakawa09@gmail.com\n",
		},
		Variables: map[string]string{
			"device.id":     "2",
			"device.serial": "emulator-5556",
		},
	}

	err := runner.Run(context.Background(), Timeline{
		{Type: CommandOCR, Action: ActionWait, Then: ActionCapture, Find: `([A-Za-z0-9._%+-]+@gmail\.com)`, Output: output, Timeout: "200ms", Interval: "1ms"},
	})

	require.NoError(t, err)
	require.Equal(t, "yoshiharukohayakawa09@gmail.com", runner.Variables["gmail.value"])
	content, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "id,device,value\n2,emulator-5556,yoshiharukohayakawa09@gmail.com\n", string(content))
}

func TestRunnerCapturesRelativeValuesCSVToDataDir(t *testing.T) {
	dataDir := t.TempDir()
	output := filepath.Join(dataDir, "gmail.csv")
	runner := Runner{
		Device:  &recordingDevice{},
		Ocr:     textOCR{text: "O suggested.gmail@gmail.com\n"},
		DataDir: dataDir,
		Variables: map[string]string{
			"device.id":     "2",
			"device.serial": "emulator-5556",
		},
	}

	err := runner.Run(context.Background(), Timeline{
		{Type: CommandOCR, Action: ActionWait, Then: ActionCapture, Find: `([A-Za-z0-9._%+-]+@gmail\.com)`, Output: "values/gmail.csv", Timeout: "200ms", Interval: "1ms"},
	})

	require.NoError(t, err)
	require.FileExists(t, output)
	require.NoFileExists(t, filepath.Join("values", "gmail.csv"))
}

func TestRunnerGeneratesIdentifierToDeviceCSV(t *testing.T) {
	events := timelineEvents{}
	output := filepath.Join(t.TempDir(), "account.csv")
	runner := Runner{
		Device: &recordingDevice{events: &events},
		Ocr: textOCR{
			text: "Create record\nIdentifier\n",
		},
		Variables: map[string]string{
			"device.id":     "2",
			"device.serial": "emulator-5556",
			"record.label":  "Example Item 42",
		},
	}

	err := runner.Run(context.Background(), Timeline{
		{Type: CommandOCR, Action: ActionGenerateIdentifier, Find: "Identifier", Text: "{{record.label}}", ValueSuffix: "-done", Output: output},
	})

	require.NoError(t, err)
	identifier := generateIdentifier("Example Item 42", runner.Variables)
	require.Equal(t, []string{"adb screenshot", "adb tap 0 0", "adb text " + identifier}, events.values())
	content, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "id,device,value\n2,emulator-5556,"+identifier+"-done\n", string(content))
}

func TestRunnerRaceTimelineContinuesAfterFirstSuccess(t *testing.T) {
	output := filepath.Join(t.TempDir(), "account.csv")
	runner := Runner{
		Device: &recordingDevice{},
		Ocr: textOCR{
			text: "Create record\nIdentifier\n",
		},
		Variables: map[string]string{
			"device.id":     "2",
			"device.serial": "emulator-5556",
			"record.label":  "Example Item 42",
		},
	}

	err := runner.Run(context.Background(), Timeline{
		{
			Type:   CommandTimeline,
			Action: ActionRace,
			raceTimelines: []Timeline{
				{{Type: CommandOCR, Action: ActionCapture, Find: `([A-Za-z0-9._%+-]+@gmail\.com)`, Output: output}},
				{{Type: CommandOCR, Action: ActionGenerateIdentifier, Find: "Identifier", Text: "{{record.label}}", ValueSuffix: "-done", Output: output}},
			},
		},
	})

	require.NoError(t, err)
	identifier := generateIdentifier("Example Item 42", runner.Variables)
	content, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "id,device,value\n2,emulator-5556,"+identifier+"-done\n", string(content))
}

func TestRunnerRaceTimelineUsesCapturedCSVValueInWinningBranch(t *testing.T) {
	events := timelineEvents{}
	output := filepath.Join(t.TempDir(), "account.csv")
	runner := Runner{
		Device: &recordingDevice{events: &events},
		Ocr: targetOCR{
			text: "Record\nsuggested-value\n",
			targets: map[string]bool{
				"suggested-value": true,
			},
		},
		Variables: map[string]string{
			"device.id":     "2",
			"device.serial": "emulator-5556",
			"record.label":  "Example Item 42",
		},
	}

	err := runner.Run(context.Background(), Timeline{
		{
			Type:   CommandTimeline,
			Action: ActionRace,
			raceTimelines: []Timeline{
				{
					{Type: CommandOCR, Action: ActionCapture, Find: `(suggested-value)`, Output: output},
					{Type: CommandOCR, Action: ActionTap, Find: "{{account.value}}"},
				},
				{{Type: CommandOCR, Action: ActionGenerateIdentifier, Find: "Identifier", Text: "{{record.label}}", ValueSuffix: "-done", Output: output}},
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, "suggested-value", runner.Variables["account.value"])
	require.ElementsMatch(t, []string{"adb screenshot", "adb screenshot", "adb screenshot", "adb tap 0 0"}, events.values())
	content, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "id,device,value\n2,emulator-5556,suggested-value\n", string(content))
}

func TestRunnerRaceTimelineSerializesConcurrentOCROperations(t *testing.T) {
	engine := &blockingOCR{
		text: "Record\nserial-race-value\nIdentifier\n",
	}
	runner := Runner{
		Device: &recordingDevice{},
		Ocr:    engine,
		Variables: map[string]string{
			"device.id":     "2",
			"device.serial": "emulator-5556",
			"record.label":  "Example Item 42",
		},
	}
	output := filepath.Join(t.TempDir(), "account.csv")

	err := runner.Run(context.Background(), Timeline{
		{
			Type:   CommandTimeline,
			Action: ActionRace,
			raceTimelines: []Timeline{
				{{Type: CommandOCR, Action: ActionWait, Then: ActionCapture, Find: `(serial-race-value)`, Output: output, Timeout: "200ms", Interval: "1ms"}},
				{{Type: CommandOCR, Action: ActionWait, Then: ActionGenerateIdentifier, Find: "Identifier", Text: "{{record.label}}", ValueSuffix: "-done", Output: output, Timeout: "200ms", Interval: "1ms"}},
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, int32(1), engine.maxConcurrent.Load())
}

func TestRunnerRaceTimelineFailsOnlyWhenAllBranchesFail(t *testing.T) {
	runner := Runner{Device: &recordingDevice{}, Ocr: textOCR{text: "no match"}}

	err := runner.Run(context.Background(), Timeline{
		{
			Type:   CommandTimeline,
			Action: ActionRace,
			raceTimelines: []Timeline{
				{{Type: CommandOCR, Action: ActionCapture, Find: `first`, Output: filepath.Join(t.TempDir(), "first.txt")}},
				{{Type: CommandOCR, Action: ActionCapture, Find: `second`, Output: filepath.Join(t.TempDir(), "second.txt")}},
			},
		},
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "all race timelines failed")
}

func TestRunnerRaceTimelineOptionalIgnoresAllFailures(t *testing.T) {
	runner := Runner{Device: &recordingDevice{}, Ocr: textOCR{text: "no match"}}

	err := runner.Run(context.Background(), Timeline{
		{
			Type:     CommandTimeline,
			Action:   ActionRace,
			Optional: true,
			raceTimelines: []Timeline{
				{{Type: CommandOCR, Action: ActionCapture, Find: `missing`, Output: filepath.Join(t.TempDir(), "missing.txt")}},
			},
		},
	})

	require.NoError(t, err)
}

func TestRunnerWaitsForAppiumElementThenInputs(t *testing.T) {
	attempts := 0
	requests := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /session":
			_, _ = w.Write([]byte(`{"value":{"sessionId":"session-1","capabilities":{}}}`))
		case "POST /session/session-1/element":
			attempts++
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "accessibility id", body["using"])
			require.Equal(t, "Create account", body["value"])
			if attempts < 3 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"value":{"error":"no such element","message":"not yet"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"value":{"element-6066-11e4-a52e-4f735466cecf":"element-1"}}`))
		case "POST /session/session-1/element/element-1/click":
			_, _ = w.Write([]byte(`{"value":null}`))
		case "POST /session/session-1/element/element-1/value":
			var body struct {
				Text string `json:"text"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "abc", body.Text)
			_, _ = w.Write([]byte(`{"value":null}`))
		case "DELETE /session/session-1":
			_, _ = w.Write([]byte(`{"value":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runner := Runner{
		Device:          &recordingDevice{},
		Ocr:             fakeOCR{},
		AppiumServerURL: server.URL,
	}

	err := runner.Run(context.Background(), Timeline{
		{Type: CommandAppium, Action: ActionWait, Then: ActionInput, Using: "accessibility id", Find: "Create account", Text: "abc", Timeout: "200ms", Interval: "1ms"},
	})

	require.NoError(t, err)
	require.Equal(t, 3, attempts)
	require.Equal(t, []string{
		"POST /session",
		"POST /session/session-1/element",
		"POST /session/session-1/element",
		"POST /session/session-1/element",
		"POST /session/session-1/element/element-1/click",
		"POST /session/session-1/element/element-1/value",
		"DELETE /session/session-1",
	}, requests)
}

func TestRunnerCapturesAppiumRegexToCSV(t *testing.T) {
	dataDir := t.TempDir()
	requests := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /session":
			_, _ = w.Write([]byte(`{"value":{"sessionId":"session-1","capabilities":{}}}`))
		case "GET /session/session-1/source":
			_, _ = w.Write([]byte(`{"value":"<hierarchy><node text='Seu código de verificação é 583921'/></hierarchy>"}`))
		case "DELETE /session/session-1":
			_, _ = w.Write([]byte(`{"value":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runner := Runner{
		Device:          &recordingDevice{},
		Ocr:             fakeOCR{},
		AppiumServerURL: server.URL,
		DataDir:         dataDir,
		Output:          io.Discard,
		Variables: map[string]string{
			"device.id":     "2",
			"device.serial": "device-b",
		},
	}

	err := runner.Run(context.Background(), Timeline{{Type: CommandAppium, Action: ActionCapture, Find: `verifica..o[^0-9]+([0-9]{6})`, Output: "verification_code.csv"}})

	require.NoError(t, err)
	require.Equal(t, "583921", runner.Variables["verification_code"])
	content, err := os.ReadFile(filepath.Join(dataDir, "verification_code.csv"))
	require.NoError(t, err)
	require.Equal(t, "id,device,value\n2,device-b,583921\n", string(content))
	require.Equal(t, []string{
		"POST /session",
		"GET /session/session-1/source",
		"DELETE /session/session-1",
	}, requests)
}

func TestRunnerCapturesAppiumRegexToDynamicCSVWithOutputKey(t *testing.T) {
	dataDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /session":
			_, _ = w.Write([]byte(`{"value":{"sessionId":"session-1","capabilities":{}}}`))
		case "GET /session/session-1/source":
			_, _ = w.Write([]byte(`{"value":"<hierarchy><node text='834965'/></hierarchy>"}`))
		case "DELETE /session/session-1":
			_, _ = w.Write([]byte(`{"value":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runner := Runner{
		Device:          &recordingDevice{},
		Ocr:             fakeOCR{},
		AppiumServerURL: server.URL,
		DataDir:         dataDir,
		Output:          io.Discard,
		Variables: map[string]string{
			"device.id":     "1",
			"device.serial": "device-a",
			"user.username": "alice",
		},
	}

	err := runner.Run(context.Background(), Timeline{{Type: CommandAppium, Action: ActionCapture, Find: `\b([0-9]{6})\b`, Output: "{{user.username}}_code.csv", OutputKey: "verification_code"}})

	require.NoError(t, err)
	require.Equal(t, "834965", runner.Variables["alice_code"])
	require.Equal(t, "834965", runner.Variables["verification_code"])
	content, err := os.ReadFile(filepath.Join(dataDir, "alice_code.csv"))
	require.NoError(t, err)
	require.Equal(t, "id,device,value\n1,device-a,834965\n", string(content))
}

func TestRunnerWaitsForAppiumCapture(t *testing.T) {
	output := filepath.Join(t.TempDir(), "code.txt")
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /session":
			_, _ = w.Write([]byte(`{"value":{"sessionId":"session-1","capabilities":{}}}`))
		case "GET /session/session-1/source":
			attempts++
			if attempts < 3 {
				_, _ = w.Write([]byte(`{"value":"<hierarchy><node text='Inbox loading'/></hierarchy>"}`))
				return
			}
			_, _ = w.Write([]byte(`{"value":"<hierarchy><node text='Verification code: 771204'/></hierarchy>"}`))
		case "DELETE /session/session-1":
			_, _ = w.Write([]byte(`{"value":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runner := Runner{Device: &recordingDevice{}, Ocr: fakeOCR{}, AppiumServerURL: server.URL, Output: io.Discard}

	err := runner.Run(context.Background(), Timeline{{Type: CommandAppium, Action: ActionWait, Then: ActionCapture, Find: `code: ([0-9]{6})`, Output: output, Timeout: "200ms", Interval: "1ms"}})

	require.NoError(t, err)
	require.Equal(t, 3, attempts)
	content, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "771204\n", string(content))
}

func TestRunnerSerializesAppiumSessionCreation(t *testing.T) {
	var active atomic.Int32
	var maxActive atomic.Int32
	var sessions atomic.Int32
	var firstSession sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			current := active.Add(1)
			for {
				max := maxActive.Load()
				if current <= max || maxActive.CompareAndSwap(max, current) {
					break
				}
			}
			firstSession.Do(func() {
				time.Sleep(50 * time.Millisecond)
			})
			id := sessions.Add(1)
			active.Add(-1)
			_, _ = w.Write([]byte(`{"value":{"sessionId":"session-` + strconv.Itoa(int(id)) + `","capabilities":{}}}`))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"value":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, variables := range []map[string]string{
		{"device.index": "0", "device.serial": "device-a"},
		{"device.index": "1", "device.serial": "device-b"},
	} {
		variables := variables
		wg.Add(1)
		go func() {
			defer wg.Done()
			runner := Runner{Device: &recordingDevice{}, Ocr: fakeOCR{}, AppiumServerURL: server.URL, Output: io.Discard, Variables: variables}
			errs <- runner.Run(context.Background(), Timeline{{Type: CommandAppium, Action: ActionStartSession}})
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), maxActive.Load())
	require.Equal(t, int32(2), sessions.Load())
}

func TestRunnerUsesConfiguredAppiumSessionLimiter(t *testing.T) {
	var active atomic.Int32
	var maxActive atomic.Int32
	var sessions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			current := active.Add(1)
			for {
				max := maxActive.Load()
				if current <= max || maxActive.CompareAndSwap(max, current) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			id := sessions.Add(1)
			active.Add(-1)
			_, _ = w.Write([]byte(`{"value":{"sessionId":"session-` + strconv.Itoa(int(id)) + `","capabilities":{}}}`))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"value":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	limiter := make(chan struct{}, 2)
	errs := make(chan error, 3)
	var wg sync.WaitGroup
	for _, variables := range []map[string]string{
		{"device.port_index": "0", "device.serial": "device-a"},
		{"device.port_index": "1", "device.serial": "device-b"},
		{"device.port_index": "2", "device.serial": "device-c"},
	} {
		variables := variables
		wg.Add(1)
		go func() {
			defer wg.Done()
			runner := Runner{Device: &recordingDevice{}, Ocr: fakeOCR{}, AppiumServerURL: server.URL, AppiumSessionLimiter: limiter, Output: io.Discard, Variables: variables}
			errs <- runner.Run(context.Background(), Timeline{{Type: CommandAppium, Action: ActionStartSession}})
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int32(2), maxActive.Load())
	require.Equal(t, int32(3), sessions.Load())
}

func TestRunnerUsesPortIndexAndConfiguredPortBases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /session":
			var body struct {
				Capabilities struct {
					AlwaysMatch map[string]any `json:"alwaysMatch"`
				} `json:"capabilities"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, float64(13005), body.Capabilities.AlwaysMatch["appium:systemPort"])
			require.Equal(t, float64(14005), body.Capabilities.AlwaysMatch["appium:mjpegServerPort"])
			require.Equal(t, float64(15005), body.Capabilities.AlwaysMatch["appium:chromedriverPort"])
			_, _ = w.Write([]byte(`{"value":{"sessionId":"session-1","capabilities":{}}}`))
		case "DELETE /session/session-1":
			_, _ = w.Write([]byte(`{"value":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runner := Runner{
		Device:                     &recordingDevice{},
		Ocr:                        fakeOCR{},
		AppiumServerURL:            server.URL,
		AppiumSystemPortBase:       13000,
		AppiumMjpegPortBase:        14000,
		AppiumChromedriverPortBase: 15000,
		Output:                     io.Discard,
		Variables:                  map[string]string{"device.index": "1", "device.port_index": "5", "device.serial": "device-a"},
	}

	err := runner.Run(context.Background(), Timeline{{Type: CommandAppium, Action: ActionStartSession}})

	require.NoError(t, err)
}

func TestAddGoogleAccountAppiumTimelineAttachesToCurrentScreen(t *testing.T) {
	timeline, err := Load(filepath.Join("..", "..", "automation", "add-google-account-appium.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, timeline)

	startSession := timeline[1]
	require.Equal(t, ActionStartSession, startSession.Action)
	require.Equal(t, false, startSession.Capabilities["appium:autoLaunch"])
	require.NotContains(t, startSession.Capabilities, "appium:appPackage")
}

type timelineEvents struct {
	mu         sync.Mutex
	valuesList []string
}

func commandNames(timeline Timeline) []string {
	names := make([]string, len(timeline))
	for i, cmd := range timeline {
		names[i] = cmd.Name
	}
	return names
}

func (e *timelineEvents) append(value string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.valuesList = append(e.valuesList, value)
}

func (e *timelineEvents) values() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.valuesList...)
}

type recordingDevice struct {
	events *timelineEvents
}

func (d *recordingDevice) Devices(context.Context) ([]string, error) { return nil, nil }
func (d *recordingDevice) Connect(context.Context) (adb.CommandResult, error) {
	return adb.CommandResult{}, nil
}
func (d *recordingDevice) Shell(context.Context, ...string) (adb.CommandResult, error) {
	return adb.CommandResult{}, nil
}
func (d *recordingDevice) Tap(_ context.Context, x, y int) (adb.CommandResult, error) {
	if d.events != nil {
		d.events.append("adb tap " + strconv.Itoa(x) + " " + strconv.Itoa(y))
	}
	return adb.CommandResult{}, nil
}
func (d *recordingDevice) Swipe(context.Context, int, int, int, int) (adb.CommandResult, error) {
	return adb.CommandResult{}, nil
}
func (d *recordingDevice) KeyEvent(context.Context, string) (adb.CommandResult, error) {
	return adb.CommandResult{}, nil
}
func (d *recordingDevice) Text(_ context.Context, text string) (adb.CommandResult, error) {
	if d.events != nil {
		d.events.append("adb text " + text)
	}
	return adb.CommandResult{}, nil
}
func (d *recordingDevice) LongPress(context.Context, int, int, int) (adb.CommandResult, error) {
	return adb.CommandResult{}, nil
}
func (d *recordingDevice) Pull(context.Context, string, string) (adb.CommandResult, error) {
	return adb.CommandResult{}, nil
}
func (d *recordingDevice) Screenshot(context.Context, string) (adb.CommandResult, error) {
	if d.events != nil {
		d.events.append("adb screenshot")
	}
	return adb.CommandResult{}, nil
}

type fakeOCR struct{}

func (fakeOCR) FindText(context.Context, string, string, ocr.Options) (*ocr.Bounds, error) {
	return &ocr.Bounds{}, nil
}
func (fakeOCR) Text(context.Context, string, ocr.Options) (string, error) { return "", nil }

type textOCR struct {
	text string
}

func (t textOCR) FindText(context.Context, string, string, ocr.Options) (*ocr.Bounds, error) {
	return &ocr.Bounds{}, nil
}
func (t textOCR) Text(context.Context, string, ocr.Options) (string, error) { return t.text, nil }

type targetOCR struct {
	text    string
	targets map[string]bool
	bounds  map[string]ocr.Bounds
}

func (t targetOCR) FindText(_ context.Context, _ string, target string, _ ocr.Options) (*ocr.Bounds, error) {
	if t.targets[target] {
		if bounds, ok := t.bounds[target]; ok {
			return &bounds, nil
		}
		return &ocr.Bounds{}, nil
	}
	return nil, errors.New("not found")
}
func (t targetOCR) Text(context.Context, string, ocr.Options) (string, error) { return t.text, nil }

type blockingOCR struct {
	text          string
	active        atomic.Int32
	maxConcurrent atomic.Int32
}

func (b *blockingOCR) start() func() {
	active := b.active.Add(1)
	for {
		max := b.maxConcurrent.Load()
		if active <= max || b.maxConcurrent.CompareAndSwap(max, active) {
			break
		}
	}
	time.Sleep(10 * time.Millisecond)
	return func() { b.active.Add(-1) }
}

func (b *blockingOCR) FindText(context.Context, string, string, ocr.Options) (*ocr.Bounds, error) {
	done := b.start()
	defer done()
	return &ocr.Bounds{}, nil
}

func (b *blockingOCR) Text(context.Context, string, ocr.Options) (string, error) {
	done := b.start()
	defer done()
	return b.text, nil
}

type eventuallyOCR struct {
	failures int
	attempts int
}

func (e *eventuallyOCR) FindText(context.Context, string, string, ocr.Options) (*ocr.Bounds, error) {
	e.attempts++
	if e.attempts <= e.failures {
		return nil, errors.New("not found")
	}
	return &ocr.Bounds{}, nil
}
func (e *eventuallyOCR) Text(context.Context, string, ocr.Options) (string, error) { return "", nil }
