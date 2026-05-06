package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func TestLoadTimelineScriptsSorted(t *testing.T) {
	automationDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(automationDir, "utils"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(automationDir, "z.yaml"), []byte("[]"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(automationDir, "a.yml"), []byte("[]"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(automationDir, "utils", "b.yaml"), []byte("[]"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(automationDir, "ignore.txt"), []byte("x"), 0o644))

	scripts, err := loadTimelineScripts(automationDir)

	require.NoError(t, err)
	require.Equal(t, []string{"a.yml", filepath.Join("utils", "b.yaml"), "z.yaml"}, scriptNames(scripts))
}

func TestVisibleRangeCentersCursor(t *testing.T) {
	start, end := visibleRange(5, 10, 4)

	require.Equal(t, 3, start)
	require.Equal(t, 7, end)
}

func TestSelectedRunConfigUsesSelectedScriptDevicesAndOptions(t *testing.T) {
	m := tuiModel{
		cfg: commandConfig{
			automationDir: "/config/automation",
			dataDir:       "/config/values",
			deviceMapPath: "/config/device-map.json",
		},
		scripts: []tuiScript{
			{Name: "first.yaml", Path: "/config/automation/first.yaml"},
			{Name: "fallback.yaml", Path: "/config/automation/fallback.yaml"},
		},
		devices:             []tuiDevice{{ID: 2, Serial: "device-b", Name: "Beta"}, {ID: 1, Serial: "device-a", Name: "Alpha"}},
		selectedDevices:     map[int]bool{0: true, 1: true},
		scriptIndex:         0,
		fallbackScriptIndex: 1,
		detectDeviceIDs:     true,
		startIndex:          3,
	}

	cfg, err := m.selectedRunConfig()

	require.NoError(t, err)
	require.Equal(t, "/config/automation/first.yaml", cfg.timeLinePath)
	require.Equal(t, "/config/automation/fallback.yaml", cfg.fallbackPath)
	require.Equal(t, "device-a,device-b", cfg.deviceSerials)
	require.True(t, cfg.detectDeviceIDs)
	require.Equal(t, 3, cfg.timeLineIndex)
	require.Equal(t, "/config/values", cfg.dataDir)
}

func TestTUIViewHighlightsSelectedScript(t *testing.T) {
	m := tuiModel{
		cfg: commandConfig{deviceMapPath: filepath.Join(t.TempDir(), "device-map.json")},
		scripts: []tuiScript{
			{Name: "first.yaml", Path: "first.yaml"},
			{Name: "selected.yaml", Path: "selected.yaml"},
		},
		devices:         []tuiDevice{{ID: 1, Serial: "emulator-5554", Name: "Pixel 8"}},
		selectedDevices: map[int]bool{0: true},
		scriptIndex:     1,
	}

	view := stripANSI(m.View())

	require.Contains(t, view, "* selected.yaml")
	require.Contains(t, view, "[x] #01 Pixel 8 [emulator-5554]")
}

func TestTUISelectsAndClearsAllDevices(t *testing.T) {
	m := tuiModel{
		devices: []tuiDevice{
			{ID: 1, Serial: "device-a", Name: "Alpha"},
			{ID: 2, Serial: "device-b", Name: "Beta"},
		},
		selectedDevices: map[int]bool{},
	}

	m.selectAllDevices()
	require.Equal(t, []string{"device-a", "device-b"}, m.selectedSerials())

	m.clearDevices()
	require.Empty(t, m.selectedSerials())
}

func TestLoadTUIDeviceIDs(t *testing.T) {
	path := writeDeviceMap(t, []detectedDevice{{ID: 7, Serial: "device-a", DataIndex: 6}})

	ids := loadTUIDeviceIDs(path)

	require.Equal(t, map[string]int{"device-a": 7}, ids)
}

func TestTUIViewShowsOutputPanel(t *testing.T) {
	m := tuiModel{
		cfg:             commandConfig{deviceMapPath: filepath.Join(t.TempDir(), "device-map.json")},
		logs:            []string{"running line", "finished"},
		selectedDevices: map[int]bool{},
	}

	view := stripANSI(m.View())

	require.Contains(t, view, "output")
	require.Contains(t, view, "running line")
	require.Contains(t, view, "finished")
}

func TestTUIOutputWriterEmitsLines(t *testing.T) {
	messages := make([]tea.Msg, 0)
	writer := &tuiOutputWriter{message: func(msg tea.Msg) {
		messages = append(messages, msg)
	}}

	n, err := writer.Write([]byte("first\nsecond"))
	writer.Close()

	require.NoError(t, err)
	require.Equal(t, len("first\nsecond"), n)
	require.Equal(t, []tea.Msg{tuiRunLogMsg("first"), tuiRunLogMsg("second")}, messages)
}

func scriptNames(scripts []tuiScript) []string {
	names := make([]string, len(scripts))
	for i, script := range scripts {
		names[i] = script.Name
	}
	return names
}

func stripANSI(value string) string {
	var builder strings.Builder
	inEscape := false
	for _, r := range value {
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		if r == '\x1b' {
			inEscape = true
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}
