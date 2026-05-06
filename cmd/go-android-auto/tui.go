package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bruxaodev/go-android-auto/pkg/adb"
)

var (
	tuiTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("229"))
	tuiSubtleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))
	tuiSectionStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("81"))
	tuiFocusedSectionStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("121"))
	tuiSelectedScriptStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("229"))
	tuiSelectedDeviceStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("114"))
	tuiActionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))
	tuiStatusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))
	tuiSuccessStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("114"))
	tuiErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("203"))
	tuiHeaderStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(lipgloss.Color("238")).
			Padding(0, 1)
	tuiPanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)
	tuiFocusedPanelStyle = tuiPanelStyle.Copy().
				BorderForeground(lipgloss.Color("81"))
	tuiOutputPanelStyle = tuiPanelStyle.Copy().
				BorderForeground(lipgloss.Color("238"))
	tuiFocusedRowStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("230")).
				Background(lipgloss.Color("238"))
	tuiPillStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("232")).
			Background(lipgloss.Color("114")).
			Padding(0, 1)
	tuiRunningPillStyle = tuiPillStyle.Copy().
				Background(lipgloss.Color("214"))
	tuiErrorPillStyle = tuiPillStyle.Copy().
				Background(lipgloss.Color("203"))
	tuiFooterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("246")).
			Padding(0, 1)
)

const (
	tuiDefaultWidth   = 132
	tuiDefaultHeight  = 34
	tuiMinWideWidth   = 112
	tuiMinPanelWidth  = 42
	tuiMaxControlWide = 58
	tuiGapWidth       = 2
	tuiLogLimit       = 18
)

type tuiAction int

const (
	tuiActionNone tuiAction = iota
	tuiActionRun
	tuiActionSetup
)

type tuiSection int

const (
	tuiSectionScripts tuiSection = iota
	tuiSectionDevices
	tuiSectionOptions
)

type tuiOption int

const (
	tuiOptionRun tuiOption = iota
	tuiOptionSetup
	tuiOptionSelectAllDevices
	tuiOptionClearDevices
	tuiOptionDetectIDs
	tuiOptionStartIndex
	tuiOptionFallback
	tuiOptionRefresh
	tuiOptionOpenConfig
	tuiOptionQuit
)

type tuiScript struct {
	Name string
	Path string
}

type tuiDevice struct {
	ID     int
	Serial string
	Name   string
}

type tuiModel struct {
	ctx                 context.Context
	cfg                 commandConfig
	scripts             []tuiScript
	devices             []tuiDevice
	selectedDevices     map[int]bool
	section             tuiSection
	scriptIndex         int
	deviceIndex         int
	optionIndex         int
	fallbackScriptIndex int
	detectDeviceIDs     bool
	startIndex          int
	status              string
	action              tuiAction
	running             bool
	logs                []string
	logChan             <-chan tea.Msg
	width               int
	height              int
}

type tuiRunLogMsg string

type tuiRunDoneMsg struct {
	Err error
}

type tuiOutputWriter struct {
	mu      sync.Mutex
	line    strings.Builder
	message func(tea.Msg)
}

func runTUI(ctx context.Context) error {
	cfg := commandConfig{}
	parseFlags(&cfg, nil)

	program := tea.NewProgram(newTUIModel(ctx, cfg), tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func newTUIModel(ctx context.Context, cfg commandConfig) tuiModel {
	m := tuiModel{
		ctx:                 ctx,
		cfg:                 cfg,
		selectedDevices:     make(map[int]bool),
		fallbackScriptIndex: -1,
		logs:                []string{"Output will appear here after you run a script."},
	}
	m.reload()
	return m
}

func (m tuiModel) Init() tea.Cmd {
	return nil
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tuiRunLogMsg:
		m.appendLog(string(msg))
		return m, waitForTUILog(m.logChan)
	case tuiRunDoneMsg:
		m.running = false
		m.logChan = nil
		if msg.Err != nil {
			m.status = "run failed"
			m.appendLog(tuiErrorStyle.Render("failed: " + msg.Err.Error()))
		} else {
			m.status = "run finished"
			m.appendLog(tuiSuccessStyle.Render("finished successfully"))
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			return m, tea.Quit
		case "tab":
			m.nextSection()
		case "shift+tab":
			m.previousSection()
		case "up", "k":
			m.moveCursor(-1)
		case "down", "j":
			m.moveCursor(1)
		case "left", "h":
			m.adjustOption(-1)
		case "right", "l":
			m.adjustOption(1)
		case " ":
			m.toggleCurrent()
		case "a":
			if m.section == tuiSectionDevices {
				m.selectAllDevices()
			}
		case "x":
			if m.section == tuiSectionDevices {
				m.clearDevices()
			}
		case "enter":
			cmd := m.activateCurrent()
			return m, cmd
		}
	}

	return m, nil
}

func (m tuiModel) View() string {
	width, height := m.viewportSize()
	header := m.renderHeader(width)
	footer := m.renderFooter(width)
	bodyHeight := height - lipgloss.Height(header) - lipgloss.Height(footer)
	if bodyHeight < 12 {
		bodyHeight = 12
	}

	if width >= tuiMinWideWidth {
		controlsWidth := clampValue(width*42/100, tuiMinPanelWidth, tuiMaxControlWide)
		outputWidth := width - controlsWidth - tuiGapWidth
		if outputWidth >= tuiMinPanelWidth {
			body := lipgloss.JoinHorizontal(
				lipgloss.Top,
				m.renderControls(controlsWidth),
				strings.Repeat(" ", tuiGapWidth),
				m.renderOutputPanel(outputWidth, bodyHeight),
			)
			return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
		}
	}

	body := lipgloss.JoinVertical(
		lipgloss.Left,
		m.renderControls(width),
		m.renderOutputPanel(width, maxValue(10, bodyHeight/2)),
	)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m tuiModel) viewportSize() (int, int) {
	width := m.width
	if width <= 0 {
		width = tuiDefaultWidth
	}
	height := m.height
	if height <= 0 {
		height = tuiDefaultHeight
	}
	return maxValue(60, width), maxValue(20, height)
}

func (m tuiModel) renderHeader(width int) string {
	state, stateStyle := m.statePill()
	style := tuiHeaderStyle.Width(width)
	innerWidth := maxValue(20, width-style.GetHorizontalFrameSize())
	lines := []string{
		lipgloss.JoinHorizontal(lipgloss.Center, tuiTitleStyle.Render("go-android-auto"), " ", stateStyle.Render(state)),
		tuiSubtleStyle.Render("config " + filepath.Dir(m.cfg.deviceMapPath)),
		m.renderSummary(innerWidth),
	}
	if strings.TrimSpace(m.status) != "" {
		lines = append(lines, tuiStatusStyle.Render("status "+trimLogLine(m.status, innerWidth-7)))
	}
	return style.Render(strings.Join(lines, "\n"))
}

func (m tuiModel) statePill() (string, lipgloss.Style) {
	if m.running {
		return "RUNNING", tuiRunningPillStyle
	}
	status := strings.ToLower(m.status)
	if strings.Contains(status, "failed") || strings.Contains(status, "error") {
		return "ERROR", tuiErrorPillStyle
	}
	return "READY", tuiPillStyle
}

func (m tuiModel) renderControls(width int) string {
	contentWidth := cardContentWidth(width)
	return lipgloss.JoinVertical(
		lipgloss.Left,
		m.renderCard("scripts", m.section == tuiSectionScripts, m.renderScripts(contentWidth), width),
		"",
		m.renderCard("devices", m.section == tuiSectionDevices, m.renderDevices(contentWidth), width),
		"",
		m.renderCard("actions", m.section == tuiSectionOptions, m.renderOptions(contentWidth), width),
	)
}

func (m tuiModel) renderCard(title string, focused bool, content string, width int) string {
	style := tuiPanelStyle
	if focused {
		style = tuiFocusedPanelStyle
	}
	return style.Width(width).Render(strings.TrimRight(sectionTitle(title, focused)+content, "\n"))
}

func (m tuiModel) renderOutputPanel(width int, height int) string {
	height = maxValue(8, height)
	style := tuiOutputPanelStyle.Width(width).Height(height)
	contentWidth := maxValue(20, width-style.GetHorizontalFrameSize())
	contentHeight := maxValue(4, height-style.GetVerticalFrameSize())
	return style.
		Height(height).
		Render(strings.TrimRight(m.renderOutput(contentWidth, contentHeight), "\n"))
}

func (m tuiModel) renderFooter(width int) string {
	keys := "tab sections | up/down move | left/right adjust | space select | a all | x none | enter action | q quit"
	if width < 88 {
		keys = "tab sections | arrows move/adjust | space select | enter action | q quit"
	}
	return tuiFooterStyle.Width(width).Render(trimLogLine(keys, width-2))
}

func (m tuiModel) renderOutput(width int, limit int) string {
	var builder strings.Builder
	builder.WriteString(sectionTitle("output", false))
	logs := m.visibleLogs(limit)
	if len(logs) == 0 {
		builder.WriteString(tuiSubtleStyle.Render("  no output yet") + "\n")
		return builder.String()
	}
	for _, line := range logs {
		builder.WriteString(tuiSubtleStyle.Render(trimLogLine(line, width)) + "\n")
	}
	return builder.String()
}

func (m tuiModel) renderSummary(width int) string {
	script := "none"
	if m.scriptIndex >= 0 && m.scriptIndex < len(m.scripts) {
		script = m.scripts[m.scriptIndex].Name
	}
	devices := m.selectedDeviceLabels()
	deviceSummary := "no devices"
	if len(devices) > 0 {
		deviceSummary = strings.Join(devices, ", ")
	}
	return tuiSubtleStyle.Render("selected: ") + tuiSelectedScriptStyle.Render(trimLogLine(script, width/2)) + tuiSubtleStyle.Render(" | devices: "+trimLogLine(deviceSummary, width/2))
}

func (m tuiModel) renderScripts(width int) string {
	var builder strings.Builder
	if len(m.scripts) == 0 {
		builder.WriteString("  no yaml scripts found\n")
		return builder.String()
	}
	for _, script := range visibleScriptWindow(m.scriptIndex, m.scripts, 6) {
		focused := m.section == tuiSectionScripts && script.Index == m.scriptIndex
		selected := script.Index == m.scriptIndex
		line := "  " + script.Value.Name
		if selected {
			line = "* " + script.Value.Name
		}
		style := tuiSubtleStyle
		if selected {
			style = tuiSelectedScriptStyle
		}
		builder.WriteString(renderRow(trimLogLine(line, width-3), focused, width, style))
	}
	return builder.String()
}

func (m tuiModel) renderDevices(width int) string {
	var builder strings.Builder
	if len(m.devices) == 0 {
		builder.WriteString("  no devices\n")
		return builder.String()
	}
	for _, device := range visibleDeviceWindow(m.deviceIndex, m.devices, 5) {
		focused := m.section == tuiSectionDevices && device.Index == m.deviceIndex
		line := "[ ] " + device.Value.Label()
		style := tuiSubtleStyle
		if m.selectedDevices[device.Index] {
			line = "[x] " + device.Value.Label()
			style = tuiSelectedDeviceStyle
		}
		builder.WriteString(renderRow(trimLogLine(line, width-3), focused, width, style))
	}
	return builder.String()
}

func (m tuiModel) renderOptions(width int) string {
	var builder strings.Builder
	options := m.optionLabels()
	for i, option := range options {
		focused := m.section == tuiSectionOptions && i == m.optionIndex
		builder.WriteString(renderRow(trimLogLine(option, width-3), focused, width, tuiActionStyle))
	}
	return builder.String()
}

func sectionTitle(title string, focused bool) string {
	if focused {
		return tuiFocusedSectionStyle.Render("> "+title) + "\n"
	}
	return tuiSectionStyle.Render("  "+title) + "\n"
}

func renderRow(line string, focused bool, width int, style lipgloss.Style) string {
	if width < 12 {
		width = 12
	}
	if focused {
		return tuiFocusedRowStyle.Width(width).Render("> "+line) + "\n"
	}
	return "  " + style.Render(line) + "\n"
}

func clampValue(value int, min int, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func maxValue(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func cardContentWidth(width int) int {
	return maxValue(20, width-tuiPanelStyle.GetHorizontalFrameSize())
}

type visibleItem[T any] struct {
	Index int
	Value T
}

func visibleScriptWindow(cursor int, values []tuiScript, limit int) []visibleItem[tuiScript] {
	start, end := visibleRange(cursor, len(values), limit)
	items := make([]visibleItem[tuiScript], 0, end-start)
	for i := start; i < end; i++ {
		items = append(items, visibleItem[tuiScript]{Index: i, Value: values[i]})
	}
	return items
}

func visibleDeviceWindow(cursor int, values []tuiDevice, limit int) []visibleItem[tuiDevice] {
	start, end := visibleRange(cursor, len(values), limit)
	items := make([]visibleItem[tuiDevice], 0, end-start)
	for i := start; i < end; i++ {
		items = append(items, visibleItem[tuiDevice]{Index: i, Value: values[i]})
	}
	return items
}

func visibleRange(cursor int, total int, limit int) (int, int) {
	if total <= 0 {
		return 0, 0
	}
	if limit <= 0 || total <= limit {
		return 0, total
	}
	start := cursor - limit/2
	if start < 0 {
		start = 0
	}
	if start+limit > total {
		start = total - limit
	}
	return start, start + limit
}

func (m tuiModel) optionLabels() []string {
	detect := "off"
	if m.detectDeviceIDs {
		detect = "on"
	}
	return []string{
		"run selected script",
		"setup device map",
		"select all devices",
		"clear device selection",
		"detect device ids: " + detect,
		fmt.Sprintf("start index: %d", m.startIndex),
		"fallback override: " + m.fallbackLabel(),
		"refresh scripts/devices",
		"open config folder",
		"quit",
	}
}

func (m tuiModel) fallbackLabel() string {
	if m.fallbackScriptIndex < 0 || m.fallbackScriptIndex >= len(m.scripts) {
		return "embedded/default"
	}
	return m.scripts[m.fallbackScriptIndex].Name
}

func (m *tuiModel) nextSection() {
	m.section = (m.section + 1) % 3
}

func (m *tuiModel) previousSection() {
	m.section--
	if m.section < 0 {
		m.section = tuiSectionOptions
	}
}

func (m *tuiModel) moveCursor(delta int) {
	switch m.section {
	case tuiSectionScripts:
		m.scriptIndex = clampIndex(m.scriptIndex+delta, len(m.scripts))
	case tuiSectionDevices:
		m.deviceIndex = clampIndex(m.deviceIndex+delta, len(m.devices))
	case tuiSectionOptions:
		m.optionIndex = clampIndex(m.optionIndex+delta, len(m.optionLabels()))
	}
}

func (m *tuiModel) adjustOption(delta int) {
	if m.section != tuiSectionOptions {
		return
	}
	switch tuiOption(m.optionIndex) {
	case tuiOptionStartIndex:
		m.startIndex += delta
		if m.startIndex < 0 {
			m.startIndex = 0
		}
	case tuiOptionFallback:
		m.cycleFallback(delta)
	}
}

func (m *tuiModel) toggleCurrent() {
	if m.section == tuiSectionDevices && len(m.devices) > 0 {
		if m.selectedDevices[m.deviceIndex] {
			delete(m.selectedDevices, m.deviceIndex)
		} else {
			m.selectedDevices[m.deviceIndex] = true
		}
	}
}

func (m *tuiModel) activateCurrent() tea.Cmd {
	switch m.section {
	case tuiSectionScripts:
		m.section = tuiSectionDevices
	case tuiSectionDevices:
		m.toggleCurrent()
	case tuiSectionOptions:
		return m.activateOption(tuiOption(m.optionIndex))
	}
	return nil
}

func (m *tuiModel) activateOption(option tuiOption) tea.Cmd {
	if m.running && option != tuiOptionQuit {
		m.status = "wait for current run to finish"
		return nil
	}
	switch option {
	case tuiOptionRun:
		cfg, err := m.selectedRunConfig()
		if err != nil {
			m.status = err.Error()
			return nil
		}
		return m.startCommand("run", func(ctx context.Context, output io.Writer) error {
			return runCommandWithOutput(ctx, cfg, nil, output)
		})
	case tuiOptionSetup:
		cfg, err := m.selectedDeviceConfig()
		if err != nil {
			m.status = err.Error()
			return nil
		}
		return m.startCommand("setup device map", func(ctx context.Context, output io.Writer) error {
			return runDeviceSetup(ctx, cfg, output)
		})
	case tuiOptionSelectAllDevices:
		m.selectAllDevices()
	case tuiOptionClearDevices:
		m.clearDevices()
	case tuiOptionDetectIDs:
		m.detectDeviceIDs = !m.detectDeviceIDs
	case tuiOptionStartIndex:
		m.startIndex++
	case tuiOptionFallback:
		m.cycleFallback(1)
	case tuiOptionRefresh:
		m.reload()
	case tuiOptionOpenConfig:
		if err := openConfigFolder(filepath.Dir(m.cfg.deviceMapPath)); err != nil {
			m.status = err.Error()
		} else {
			m.status = "opened config folder"
		}
	case tuiOptionQuit:
		return tea.Quit
	}
	return nil
}

func (m *tuiModel) startCommand(label string, run func(context.Context, io.Writer) error) tea.Cmd {
	messages := make(chan tea.Msg, 64)
	writer := &tuiOutputWriter{message: func(msg tea.Msg) { messages <- msg }}
	m.running = true
	m.status = label + " started"
	m.logChan = messages
	m.logs = []string{tuiStatusStyle.Render("starting " + label + "...")}
	go func() {
		err := run(m.ctx, writer)
		writer.Close()
		messages <- tuiRunDoneMsg{Err: err}
		close(messages)
	}()
	return waitForTUILog(messages)
}

func waitForTUILog(messages <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		if messages == nil {
			return nil
		}
		msg, ok := <-messages
		if !ok {
			return nil
		}
		return msg
	}
}

func (m *tuiModel) selectAllDevices() {
	m.selectedDevices = make(map[int]bool, len(m.devices))
	for i := range m.devices {
		m.selectedDevices[i] = true
	}
	m.status = "selected all devices"
}

func (m *tuiModel) clearDevices() {
	m.selectedDevices = make(map[int]bool)
	m.status = "cleared device selection"
}

func (m *tuiModel) appendLog(line string) {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return
	}
	m.logs = append(m.logs, line)
	if len(m.logs) > 300 {
		m.logs = append([]string(nil), m.logs[len(m.logs)-300:]...)
	}
}

func (m tuiModel) visibleLogs(limit int) []string {
	if limit <= 0 {
		limit = tuiLogLimit
	}
	if len(m.logs) <= limit {
		return m.logs
	}
	return m.logs[len(m.logs)-limit:]
}

func trimLogLine(line string, limit int) string {
	plain := strings.ReplaceAll(line, "\t", "  ")
	runes := []rune(plain)
	if limit <= 0 || len(runes) <= limit {
		return plain
	}
	return string(runes[:limit-1]) + "..."
}

func (m *tuiModel) cycleFallback(delta int) {
	if len(m.scripts) == 0 {
		m.fallbackScriptIndex = -1
		return
	}
	next := m.fallbackScriptIndex + delta
	if next >= len(m.scripts) {
		next = -1
	}
	if next < -1 {
		next = len(m.scripts) - 1
	}
	m.fallbackScriptIndex = next
}

func (m *tuiModel) reload() {
	scripts, scriptErr := loadTimelineScripts(m.cfg.automationDir)
	devices, deviceStatus := loadTUIDevices(m.ctx, m.cfg)
	m.scripts = scripts
	m.devices = devices
	m.scriptIndex = clampIndex(m.scriptIndex, len(m.scripts))
	m.deviceIndex = clampIndex(m.deviceIndex, len(m.devices))
	m.selectedDevices = keepSelectedDevices(m.selectedDevices, len(m.devices))
	if len(m.selectedDevices) == 0 {
		for i := range m.devices {
			m.selectedDevices[i] = true
		}
	}
	if m.fallbackScriptIndex >= len(m.scripts) {
		m.fallbackScriptIndex = -1
	}
	if scriptErr != nil {
		m.status = scriptErr.Error()
	} else {
		m.status = deviceStatus
	}
}

func keepSelectedDevices(selected map[int]bool, count int) map[int]bool {
	kept := make(map[int]bool, len(selected))
	for index, enabled := range selected {
		if enabled && index >= 0 && index < count {
			kept[index] = true
		}
	}
	return kept
}

func clampIndex(index int, count int) int {
	if count <= 0 {
		return 0
	}
	if index < 0 {
		return 0
	}
	if index >= count {
		return count - 1
	}
	return index
}

func (m tuiModel) selectedRunConfig() (commandConfig, error) {
	if len(m.scripts) == 0 {
		return commandConfig{}, fmt.Errorf("no timeline scripts found in %s", m.cfg.automationDir)
	}
	cfg, err := m.selectedDeviceConfig()
	if err != nil {
		return commandConfig{}, err
	}
	cfg.timeLinePath = m.scripts[m.scriptIndex].Path
	cfg.fallbackPath = ""
	if m.fallbackScriptIndex >= 0 && m.fallbackScriptIndex < len(m.scripts) {
		cfg.fallbackPath = m.scripts[m.fallbackScriptIndex].Path
	}
	cfg.timeLineIndex = m.startIndex
	return cfg, nil
}

func (m tuiModel) selectedDeviceConfig() (commandConfig, error) {
	serials := m.selectedSerials()
	if len(serials) == 0 {
		return commandConfig{}, fmt.Errorf("select at least one device")
	}

	cfg := m.cfg
	cfg.detectDeviceIDs = m.detectDeviceIDs
	cfg.deviceSerial = ""
	cfg.deviceSerials = ""
	cfg.deviceIDs = ""
	cfg.allDevices = false
	if len(serials) == 1 {
		cfg.deviceSerial = serials[0]
	} else {
		cfg.deviceSerials = strings.Join(serials, ",")
	}
	return cfg, nil
}

func (m tuiModel) selectedSerials() []string {
	serials := make([]string, 0, len(m.selectedDevices))
	for index, selected := range m.selectedDevices {
		if selected && index >= 0 && index < len(m.devices) {
			serials = append(serials, m.devices[index].Serial)
		}
	}
	sort.Strings(serials)
	return serials
}

func (m tuiModel) selectedDeviceLabels() []string {
	labels := make([]string, 0, len(m.selectedDevices))
	for index, selected := range m.selectedDevices {
		if selected && index >= 0 && index < len(m.devices) {
			labels = append(labels, m.devices[index].ShortLabel())
		}
	}
	sort.Strings(labels)
	return labels
}

func loadTimelineScripts(automationDir string) ([]tuiScript, error) {
	entries := make([]tuiScript, 0)
	err := filepath.WalkDir(automationDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		relativePath, err := filepath.Rel(automationDir, path)
		if err != nil {
			return err
		}
		entries = append(entries, tuiScript{Name: relativePath, Path: path})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load scripts from %s: %w", automationDir, err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

func loadTUIDevices(ctx context.Context, cfg commandConfig) ([]tuiDevice, string) {
	devices, err := adb.New("", cfg.adbPath).Devices(ctx)
	if err != nil {
		fallback := adb.New("", cfg.adbPath).Serial
		return []tuiDevice{{ID: 1, Serial: fallback, Name: "default"}}, "adb devices failed; using " + fallback
	}
	if len(devices) == 0 {
		fallback := adb.New("", cfg.adbPath).Serial
		return []tuiDevice{{ID: 1, Serial: fallback, Name: "default"}}, "no adb devices; using " + fallback
	}
	sort.Strings(devices)
	ids := loadTUIDeviceIDs(cfg.deviceMapPath)
	items := make([]tuiDevice, 0, len(devices))
	for i, serial := range devices {
		id := ids[displaySerial(serial)]
		if id <= 0 {
			id = i + 1
		}
		items = append(items, tuiDevice{ID: id, Serial: serial, Name: loadTUIDeviceName(ctx, cfg, serial)})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ID == items[j].ID {
			return items[i].Serial < items[j].Serial
		}
		return items[i].ID < items[j].ID
	})
	return items, ""
}

func loadTUIDeviceIDs(path string) map[string]int {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var deviceMap deviceMapFile
	if err := json.Unmarshal(content, &deviceMap); err != nil {
		return nil
	}
	ids := make(map[string]int, len(deviceMap.Devices))
	for _, device := range deviceMap.Devices {
		if device.ID > 0 && strings.TrimSpace(device.Serial) != "" {
			ids[displaySerial(device.Serial)] = device.ID
		}
	}
	return ids
}

func loadTUIDeviceName(ctx context.Context, cfg commandConfig, serial string) string {
	result, err := adb.New(serial, cfg.adbPath).Shell(ctx, "getprop", "ro.product.model")
	if err != nil {
		return "unknown"
	}
	name := strings.Join(strings.Fields(result.Stdout), " ")
	if name == "" {
		return "unknown"
	}
	return name
}

func (d tuiDevice) Label() string {
	name := strings.TrimSpace(d.Name)
	if name == "" {
		name = "unknown"
	}
	return fmt.Sprintf("#%02d %s [%s]", d.ID, name, d.Serial)
}

func (d tuiDevice) ShortLabel() string {
	name := strings.TrimSpace(d.Name)
	if name == "" {
		name = d.Serial
	}
	return fmt.Sprintf("#%02d %s", d.ID, name)
}

func openConfigFolder(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("config folder path is empty")
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

func (w *tuiOutputWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range string(p) {
		if r == '\n' {
			w.flushLocked()
			continue
		}
		if r != '\r' {
			w.line.WriteRune(r)
		}
	}
	return len(p), nil
}

func (w *tuiOutputWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked()
}

func (w *tuiOutputWriter) flushLocked() {
	line := strings.TrimRight(w.line.String(), "\r")
	w.line.Reset()
	if strings.TrimSpace(line) == "" || w.message == nil {
		return
	}
	w.message(tuiRunLogMsg(line))
}
