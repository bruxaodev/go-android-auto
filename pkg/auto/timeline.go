package auto

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bruxaodev/go-android-auto/pkg/adb"
	"github.com/bruxaodev/go-android-auto/pkg/appium"
	"github.com/bruxaodev/go-android-auto/pkg/ocr"
	"gopkg.in/yaml.v3"
)

type Device interface {
	Devices(context.Context) ([]string, error)
	Connect(context.Context) (adb.CommandResult, error)
	Shell(ctx context.Context, args ...string) (adb.CommandResult, error)
	Tap(ctx context.Context, x, y int) (adb.CommandResult, error)
	Swipe(ctx context.Context, x1, y1, x2, y2 int) (adb.CommandResult, error)
	KeyEvent(ctx context.Context, keyCode string) (adb.CommandResult, error)
	Text(ctx context.Context, text string) (adb.CommandResult, error)
	LongPress(ctx context.Context, x, y, delay int) (adb.CommandResult, error)
	Pull(ctx context.Context, localPath, remotePath string) (adb.CommandResult, error)
	Screenshot(ctx context.Context, localPath string) (adb.CommandResult, error)
}

type Runner struct {
	Device          Device
	Ocr             ocr.Engine
	Appium          *appium.Client
	AppiumSession   *appium.Session
	AppiumServerURL string
	BootTimeout     time.Duration
	DataDir         string
	Output          io.Writer
	Variables       map[string]string
}

var variablePattern = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.-]+)\s*\}\}`)

const (
	defaultWaitTimeout  = 30 * time.Second
	defaultWaitInterval = time.Second
)

var ocrOperationSlots = make(chan struct{}, 1)

type CommandType string

const (
	CommandADB      CommandType = "adb"
	CommandOCR      CommandType = "ocr"
	CommandAppium   CommandType = "appium"
	CommandTimeline CommandType = "timeline"
)

type Action string

const (
	ActionHome               Action = "home"
	ActionKeyEvent           Action = "keyevent"
	ActionTap                Action = "tap"
	ActionInput              Action = "input"
	ActionCapture            Action = "capture"
	ActionGenerateIdentifier Action = "generate-identifier"
	ActionSwipe              Action = "swipe"
	ActionText               Action = "text"
	ActionInstall            Action = "install"
	ActionClearApp           Action = "clear-app"
	ActionScreenshot         Action = "screenshot"
	ActionSetDensity         Action = "set-density"
	ActionResetDensity       Action = "reset-density"
	ActionSetSize            Action = "set-size"
	ActionResetSize          Action = "reset-size"
	ActionLongPress          Action = "long-press"
	ActionPull               Action = "pull"
	ActionWait               Action = "wait"
	ActionShell              Action = "shell"
	ActionRace               Action = "race"
	ActionStartSession       Action = "start-session"
	ActionQuitSession        Action = "quit-session"
	ActionPageSource         Action = "page-source"
)

type Timeline []Command

type LoadedTimeline struct {
	Timeline     Timeline
	FallbackPath string
}

type Command struct {
	Name          string         `yaml:"name,omitempty"`
	Type          CommandType    `yaml:"type"`
	Action        Action         `yaml:"action"`
	Then          Action         `yaml:"then,omitempty"`
	Key           string         `yaml:"key,omitempty"`
	Text          string         `yaml:"text,omitempty"`
	Find          string         `yaml:"find,omitempty"`
	ValueSuffix   string         `yaml:"value_suffix,omitempty"`
	Apk           string         `yaml:"apk,omitempty"`
	Package       string         `yaml:"package,omitempty"`
	Args          []string       `yaml:"args,omitempty"`
	Screenshot    string         `yaml:"screenshot,omitempty"`
	Output        string         `yaml:"output,omitempty"`
	OCRLang       string         `yaml:"ocr_lang,omitempty"`
	OCRPSM        int            `yaml:"ocr_psm,omitempty"`
	AppiumURL     string         `yaml:"appium_url,omitempty"`
	Using         string         `yaml:"using,omitempty"`
	Capabilities  map[string]any `yaml:"capabilities,omitempty"`
	Size          string         `yaml:"size,omitempty"`
	HostPort      int            `yaml:"host_port,omitempty"`
	DevicePort    int            `yaml:"device_port,omitempty"`
	X             *int           `yaml:"x,omitempty"`
	Y             *int           `yaml:"y,omitempty"`
	X1            *int           `yaml:"x1,omitempty"`
	Y1            *int           `yaml:"y1,omitempty"`
	X2            *int           `yaml:"x2,omitempty"`
	Y2            *int           `yaml:"y2,omitempty"`
	WaitBefore    string         `yaml:"wait_before,omitempty"`
	WaitAfter     string         `yaml:"wait_after,omitempty"`
	Timeout       string         `yaml:"timeout,omitempty"`
	Interval      string         `yaml:"interval,omitempty"`
	MS            *int           `yaml:"ms,omitempty"`
	TimelinePath  string         `yaml:"timeline,omitempty"`
	TimelinePaths []string       `yaml:"timelines,omitempty"`
	Optional      bool           `yaml:"optional,omitempty"`
	FallbackPath  string         `yaml:"fallback,omitempty"`
	raceTimelines []Timeline
}

func Load(path string) (Timeline, error) {
	loaded, err := LoadWithMetadata(path)
	if err != nil {
		return nil, err
	}
	return loaded.Timeline, nil
}

func LoadWithMetadata(path string) (LoadedTimeline, error) {
	return load(path, nil)
}

func load(path string, stack []string) (LoadedTimeline, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return LoadedTimeline{}, fmt.Errorf("failed to resolve timeline file path: %w,\n path: %s", err, path)
	}
	if slices.Contains(stack, absPath) {
		cycle := append(append([]string(nil), stack...), absPath)
		return LoadedTimeline{}, fmt.Errorf("timeline include cycle: %s", strings.Join(cycle, " -> "))
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return LoadedTimeline{}, fmt.Errorf("failed to read timeline file: %w,\n path: %s", err, absPath)
	}
	var timeLine Timeline
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&timeLine); err != nil {
		return LoadedTimeline{}, fmt.Errorf("failed to parse timeline file: %w,\n path: %s", err, absPath)
	}
	if len(timeLine) == 0 {
		return LoadedTimeline{}, fmt.Errorf("timeline file is empty: %s", absPath)
	}

	fallbackPath, timeLine, err := extractFallbackPath(timeLine, absPath)
	if err != nil {
		return LoadedTimeline{}, err
	}
	if len(timeLine) == 0 {
		return LoadedTimeline{}, fmt.Errorf("timeline file is empty after metadata: %s", absPath)
	}

	expanded, err := expandTimeline(timeLine, absPath, append(stack, absPath))
	if err != nil {
		return LoadedTimeline{}, err
	}
	return LoadedTimeline{Timeline: expanded, FallbackPath: fallbackPath}, nil
}

func extractFallbackPath(timeline Timeline, sourcePath string) (string, Timeline, error) {
	fallbackPath := ""
	for i, cmd := range timeline {
		if strings.TrimSpace(cmd.FallbackPath) == "" {
			continue
		}
		if i != 0 {
			return "", nil, fmt.Errorf("fallback is only supported on the first timeline item")
		}

		fallbackPath = strings.TrimSpace(cmd.FallbackPath)
		if !filepath.IsAbs(fallbackPath) {
			fallbackPath = filepath.Join(filepath.Dir(sourcePath), fallbackPath)
		}

		cmd.FallbackPath = ""
		if reflect.DeepEqual(cmd, Command{}) {
			return fallbackPath, timeline[1:], nil
		}
		timeline[0] = cmd
		return fallbackPath, timeline, nil
	}

	return "", timeline, nil
}

func expandTimeline(timeline Timeline, sourcePath string, stack []string) (Timeline, error) {
	expanded := make(Timeline, 0, len(timeline))
	for i, cmd := range timeline {
		if cmd.Type == CommandTimeline && cmd.Action == ActionRace {
			raceTimelines, err := loadRaceTimelines(cmd, sourcePath, stack)
			if err != nil {
				return nil, fmt.Errorf("timeline race command %d (%s) failed: %w", i, cmd.Name, err)
			}
			cmd.raceTimelines = raceTimelines
			expanded = append(expanded, cmd)
			continue
		}
		if cmd.Type != CommandTimeline && !(cmd.Type == "" && cmd.TimelinePath != "") {
			expanded = append(expanded, cmd)
			continue
		}

		includePath := strings.TrimSpace(cmd.TimelinePath)
		if includePath == "" {
			return nil, fmt.Errorf("timeline command %d (%s) requires timeline", i, cmd.Name)
		}
		if !filepath.IsAbs(includePath) {
			includePath = filepath.Join(filepath.Dir(sourcePath), includePath)
		}

		includedTimeline, err := load(includePath, stack)
		if err != nil {
			return nil, fmt.Errorf("timeline command %d (%s) failed to load %s: %w", i, cmd.Name, includePath, err)
		}
		expanded = append(expanded, includedTimeline.Timeline...)
	}

	return expanded, nil
}

func loadRaceTimelines(cmd Command, sourcePath string, stack []string) ([]Timeline, error) {
	if len(cmd.TimelinePaths) == 0 {
		return nil, fmt.Errorf("requires timelines")
	}

	timelines := make([]Timeline, 0, len(cmd.TimelinePaths))
	for _, rawPath := range cmd.TimelinePaths {
		includePath := strings.TrimSpace(rawPath)
		if includePath == "" {
			return nil, fmt.Errorf("requires non-empty timeline path")
		}
		if !filepath.IsAbs(includePath) {
			includePath = filepath.Join(filepath.Dir(sourcePath), includePath)
		}

		includedTimeline, err := load(includePath, stack)
		if err != nil {
			return nil, fmt.Errorf("failed to load %s: %w", includePath, err)
		}
		timelines = append(timelines, includedTimeline.Timeline)
	}
	return timelines, nil
}

func (r *Runner) Run(ctx context.Context, timeline Timeline) error {
	return r.RunFrom(ctx, timeline, 0)
}

func (r *Runner) RunFrom(ctx context.Context, timeline Timeline, startIndex int) (err error) {
	return r.runFrom(ctx, timeline, startIndex, true)
}

func (r *Runner) runFrom(ctx context.Context, timeline Timeline, startIndex int, closeOnDone bool) (err error) {
	if startIndex < 0 || startIndex >= len(timeline) {
		return fmt.Errorf("invalid start index: %d", startIndex)
	}

	if r.Device == nil {
		return fmt.Errorf("device is not set")
	}

	if r.Ocr == nil {
		return fmt.Errorf("OCR engine is not set")
	}
	if closeOnDone {
		defer func() {
			if closeErr := r.Close(ctx); err == nil && closeErr != nil {
				err = closeErr
			}
		}()
	}

	timeline = timeline[startIndex:]
	for i, cmd := range timeline {
		if err := cmd.run(ctx, r, startIndex+i); err != nil {
			return err
		}
	}

	return nil
}

type timelineRaceResult struct {
	Index     int
	Err       error
	Variables map[string]string
}

func (c Command) runTimelineCommand(ctx context.Context, r *Runner) error {
	if c.Action != ActionRace {
		return fmt.Errorf("unsupported timeline action %q", c.Action)
	}
	if len(c.raceTimelines) == 0 {
		return fmt.Errorf("timeline race requires timelines")
	}

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan timelineRaceResult, len(c.raceTimelines))
	for i, timeline := range c.raceTimelines {
		go func(index int, branch Timeline) {
			branchRunner := *r
			branchRunner.Variables = cloneVariables(r.Variables)
			results <- timelineRaceResult{Index: index, Err: branchRunner.runFrom(raceCtx, branch, 0, false), Variables: branchRunner.Variables}
		}(i, timeline)
	}

	failures := make([]string, 0, len(c.raceTimelines))
	for received := 0; received < len(c.raceTimelines); received++ {
		result := <-results
		if result.Err == nil {
			copyVariables(r, result.Variables)
			cancel()
			for received++; received < len(c.raceTimelines); received++ {
				<-results
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		failures = append(failures, fmt.Sprintf("branch %d: %v", result.Index, result.Err))
	}

	if c.Optional {
		return nil
	}
	return fmt.Errorf("all race timelines failed: %s", strings.Join(failures, "; "))
}

func cloneVariables(variables map[string]string) map[string]string {
	if variables == nil {
		return nil
	}
	cloned := make(map[string]string, len(variables))
	maps.Copy(cloned, variables)
	return cloned
}

func copyVariables(r *Runner, variables map[string]string) {
	if len(variables) == 0 {
		return
	}
	if r.Variables == nil {
		r.Variables = make(map[string]string, len(variables))
	}
	maps.Copy(r.Variables, variables)
}

func (c Command) run(ctx context.Context, r *Runner, index int) error {
	var err error
	c, err = c.resolve(r.Variables)
	if err != nil {
		return fmt.Errorf("command %d (%s) failed to resolve variables: %w", index, c.Name, err)
	}
	logCommandStep(r.Output, index, c, r.Variables)

	if err = c.sleep(ctx, "wait_before", c.WaitBefore); err != nil {
		return err
	}

	switch c.Type {
	case CommandADB:
		err = c.runADB(ctx, r)
	case CommandOCR:
		err = c.runOCR(ctx, r)
	case CommandAppium:
		err = c.runAppium(ctx, r)
	case CommandTimeline:
		err = c.runTimelineCommand(ctx, r)
	default:
		err = fmt.Errorf("unsupported command type %q", c.Type)
	}
	if err != nil {
		return fmt.Errorf("command %d (%s) failed: %w", index, c.Name, err)
	}

	return c.sleep(ctx, "wait_after", c.WaitAfter)
}

func logCommandStep(output io.Writer, index int, c Command, variables map[string]string) {
	if output == nil {
		output = os.Stdout
	}
	parts := []string{fmt.Sprintf("[step %d]", index)}
	if id := strings.TrimSpace(variables["device.id"]); id != "" {
		parts = append(parts, "device "+id)
	}
	if serial := strings.TrimSpace(variables["device.serial"]); serial != "" {
		parts = append(parts, "serial "+serial)
	}
	if name := strings.TrimSpace(c.Name); name != "" {
		parts = append(parts, name)
	}
	parts = append(parts, string(c.Type)+"."+string(c.Action))
	if c.Then != "" {
		parts = append(parts, "then "+string(c.Then))
	}
	fmt.Fprintln(output, strings.Join(parts, " | "))
}

func (c Command) resolve(variables map[string]string) (Command, error) {
	var err error
	if c.Name, err = resolveVariables(c.Name, variables); err != nil {
		return Command{}, fmt.Errorf("name: %w", err)
	}
	var then string
	if then, err = resolveVariables(string(c.Then), variables); err != nil {
		return Command{}, fmt.Errorf("then: %w", err)
	}
	c.Then = Action(then)
	if c.Key, err = resolveVariables(c.Key, variables); err != nil {
		return Command{}, fmt.Errorf("key: %w", err)
	}
	if c.Text, err = resolveVariables(c.Text, variables); err != nil {
		return Command{}, fmt.Errorf("text: %w", err)
	}
	if c.Find, err = resolveVariables(c.Find, variables); err != nil {
		return Command{}, fmt.Errorf("find: %w", err)
	}
	if c.ValueSuffix, err = resolveVariables(c.ValueSuffix, variables); err != nil {
		return Command{}, fmt.Errorf("value_suffix: %w", err)
	}
	if c.Apk, err = resolveVariables(c.Apk, variables); err != nil {
		return Command{}, fmt.Errorf("apk: %w", err)
	}
	if c.Package, err = resolveVariables(c.Package, variables); err != nil {
		return Command{}, fmt.Errorf("package: %w", err)
	}
	if c.Screenshot, err = resolveVariables(c.Screenshot, variables); err != nil {
		return Command{}, fmt.Errorf("screenshot: %w", err)
	}
	if c.Output, err = resolveVariables(c.Output, variables); err != nil {
		return Command{}, fmt.Errorf("output: %w", err)
	}
	if c.OCRLang, err = resolveVariables(c.OCRLang, variables); err != nil {
		return Command{}, fmt.Errorf("ocr_lang: %w", err)
	}
	if c.AppiumURL, err = resolveVariables(c.AppiumURL, variables); err != nil {
		return Command{}, fmt.Errorf("appium_url: %w", err)
	}
	if c.Using, err = resolveVariables(c.Using, variables); err != nil {
		return Command{}, fmt.Errorf("using: %w", err)
	}
	if c.Size, err = resolveVariables(c.Size, variables); err != nil {
		return Command{}, fmt.Errorf("size: %w", err)
	}
	if c.Capabilities != nil {
		resolvedCapabilities, err := resolveCapabilityVariables(c.Capabilities, variables, "capabilities")
		if err != nil {
			return Command{}, err
		}
		capabilities, ok := resolvedCapabilities.(map[string]any)
		if !ok {
			return Command{}, fmt.Errorf("capabilities: expected map")
		}
		c.Capabilities = capabilities
	}
	if c.WaitBefore, err = resolveVariables(c.WaitBefore, variables); err != nil {
		return Command{}, fmt.Errorf("wait_before: %w", err)
	}
	if c.WaitAfter, err = resolveVariables(c.WaitAfter, variables); err != nil {
		return Command{}, fmt.Errorf("wait_after: %w", err)
	}
	if c.Timeout, err = resolveVariables(c.Timeout, variables); err != nil {
		return Command{}, fmt.Errorf("timeout: %w", err)
	}
	if c.Interval, err = resolveVariables(c.Interval, variables); err != nil {
		return Command{}, fmt.Errorf("interval: %w", err)
	}

	resolvedArgs := make([]string, len(c.Args))
	for i, arg := range c.Args {
		resolved, err := resolveVariables(arg, variables)
		if err != nil {
			return Command{}, fmt.Errorf("args[%d]: %w", i, err)
		}
		resolvedArgs[i] = resolved
	}
	c.Args = resolvedArgs

	return c, nil
}

func resolveCapabilityVariables(value any, variables map[string]string, field string) (any, error) {
	switch typed := value.(type) {
	case string:
		resolved, err := resolveVariables(typed, variables)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		return resolved, nil
	case []any:
		resolved := make([]any, len(typed))
		for i, item := range typed {
			value, err := resolveCapabilityVariables(item, variables, fmt.Sprintf("%s[%d]", field, i))
			if err != nil {
				return nil, err
			}
			resolved[i] = value
		}
		return resolved, nil
	case map[string]any:
		resolved := make(map[string]any, len(typed))
		for key, item := range typed {
			value, err := resolveCapabilityVariables(item, variables, field+"."+key)
			if err != nil {
				return nil, err
			}
			resolved[key] = value
		}
		return resolved, nil
	case map[any]any:
		resolved := make(map[string]any, len(typed))
		for key, item := range typed {
			keyText, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("%s: capability keys must be strings", field)
			}
			value, err := resolveCapabilityVariables(item, variables, field+"."+keyText)
			if err != nil {
				return nil, err
			}
			resolved[keyText] = value
		}
		return resolved, nil
	default:
		return value, nil
	}
}

func resolveVariables(value string, variables map[string]string) (string, error) {
	if value == "" || !strings.Contains(value, "{{") {
		return value, nil
	}

	var missing []string
	resolved := variablePattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := variablePattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}

		key := parts[1]
		if replacement, ok := variables[key]; ok {
			return replacement
		}

		missing = append(missing, key)
		return match
	})

	if len(missing) > 0 {
		return "", fmt.Errorf("missing variable(s): %s", strings.Join(missing, ", "))
	}

	return resolved, nil
}

func (c Command) sleep(ctx context.Context, field string, raw string) error {
	if raw == "" {
		return nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration for %s: %w", field, err)
	}

	if duration <= 0 {
		return nil
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c Command) waitDurations() (time.Duration, time.Duration, error) {
	timeout, err := parsePositiveDuration("timeout", c.Timeout, defaultWaitTimeout)
	if err != nil {
		return 0, 0, err
	}
	interval, err := parsePositiveDuration("interval", c.Interval, defaultWaitInterval)
	if err != nil {
		return 0, 0, err
	}
	return timeout, interval, nil
}

func parsePositiveDuration(field string, raw string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid duration for %s: %w", field, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be greater than 0", field)
	}
	return duration, nil
}

func (c Command) waitForCondition(ctx context.Context, description string, condition func(context.Context) error) error {
	timeout, interval, err := c.waitDurations()
	if err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error
	for {
		if err := condition(waitCtx); err == nil {
			return nil
		} else {
			lastErr = err
		}

		timer := time.NewTimer(interval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if lastErr != nil {
				return fmt.Errorf("timed out after %s waiting for %s: %w", timeout, description, lastErr)
			}
			return fmt.Errorf("timed out after %s waiting for %s", timeout, description)
		case <-timer.C:
		}
	}
}

func runOCROperation(ctx context.Context, operation func() error) error {
	select {
	case ocrOperationSlots <- struct{}{}:
		defer func() { <-ocrOperationSlots }()
		return operation()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c Command) runOCR(ctx context.Context, r *Runner) error {
	device := r.Device
	engine := r.Ocr
	variables := r.Variables
	if c.Then != "" && c.Action != ActionWait {
		return fmt.Errorf("then is only supported with ocr action %q", ActionWait)
	}

	switch c.Action {
	case ActionWait:
		if strings.TrimSpace(c.Find) == "" {
			if c.Then != "" {
				return fmt.Errorf("then requires find for ocr wait")
			}
			return c.sleep(ctx, "wait", c.Text)
		}
		if c.Then == ActionCapture {
			return c.waitForOCRCapture(ctx, device, engine, variables, r.DataDir)
		}
		bounds, err := c.waitForOCRText(ctx, device, engine)
		if err != nil {
			return err
		}
		if c.Then == ActionGenerateIdentifier {
			return c.generateIdentifierOCRAt(ctx, device, variables, bounds, r.DataDir)
		}
		return c.runOCRThen(ctx, device, bounds)
	case ActionTap, ActionInput:
		bounds, err := c.findOCRText(ctx, device, engine)
		if err != nil {
			return err
		}
		x, y := bounds.Center()

		switch c.Action {
		case ActionTap:
			_, err = device.Tap(ctx, x, y)
		case ActionInput:
			if _, err := device.Tap(ctx, x, y); err != nil {
				return err
			}
			_, err = device.Text(ctx, c.Text)
		}
		return err
	case ActionCapture:
		return c.captureOCRText(ctx, device, engine, variables, r.DataDir)
	case ActionGenerateIdentifier:
		return c.generateIdentifierOCR(ctx, device, engine, variables, r.DataDir)
	default:
		return fmt.Errorf("unsupported ocr action %q", c.Action)
	}
}

func (c Command) captureOCRText(ctx context.Context, device Device, engine ocr.Engine, variables map[string]string, dataDir string) error {
	if strings.TrimSpace(c.Find) == "" {
		return fmt.Errorf("find is required for ocr %s", c.Action)
	}
	if strings.TrimSpace(c.Output) == "" {
		return fmt.Errorf("output is required for ocr %s", c.Action)
	}

	captured, err := c.captureOCRValue(ctx, device, engine)
	if err != nil {
		return err
	}
	return c.saveOutputValue(variables, captured, dataDir)
}

func (c Command) waitForOCRCapture(ctx context.Context, device Device, engine ocr.Engine, variables map[string]string, dataDir string) error {
	if strings.TrimSpace(c.Output) == "" {
		return fmt.Errorf("output is required for ocr %s", c.Then)
	}

	var captured string
	err := c.waitForCondition(ctx, fmt.Sprintf("ocr capture regex %q", c.Find), func(ctx context.Context) error {
		var err error
		captured, err = c.captureOCRValue(ctx, device, engine)
		return err
	})
	if err != nil {
		return err
	}
	return c.saveOutputValue(variables, captured, dataDir)
}

func (c Command) captureOCRValue(ctx context.Context, device Device, engine ocr.Engine) (string, error) {
	if strings.TrimSpace(c.Find) == "" {
		return "", fmt.Errorf("find is required for ocr %s", c.Action)
	}

	tempDir, err := os.MkdirTemp("", "temp-*")
	if err != nil {
		return "", fmt.Errorf("create ocr temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	screenshotPath := filepath.Join(tempDir, "screen.png")
	var text string
	err = runOCROperation(ctx, func() error {
		if _, err := device.Screenshot(ctx, screenshotPath); err != nil {
			return err
		}

		var err error
		text, err = engine.Text(ctx, screenshotPath, ocr.Options{Lang: c.OCRLang, PSM: c.OCRPSM})
		return err
	})
	if err != nil {
		return "", err
	}

	pattern, err := regexp.Compile(c.Find)
	if err != nil {
		return "", fmt.Errorf("compile capture regex: %w", err)
	}
	matches := pattern.FindStringSubmatch(text)
	if len(matches) == 0 {
		return "", fmt.Errorf("capture regex %q did not match OCR text: %s", c.Find, strings.TrimSpace(text))
	}

	captured := matches[0]
	if len(matches) > 1 {
		captured = matches[1]
	}
	return strings.TrimSpace(captured), nil
}

func (c Command) saveOutputValue(variables map[string]string, value string, dataDir string) error {
	if strings.EqualFold(filepath.Ext(c.Output), ".csv") {
		output := resolveCSVOutputPath(c.Output, dataDir)
		deviceID, err := strconv.Atoi(strings.TrimSpace(variables["device.id"]))
		if err != nil || deviceID <= 0 {
			return fmt.Errorf("device.id is required to save output into CSV")
		}
		if err := SaveDeviceValueCSV(output, deviceID, variables["device.serial"], value); err != nil {
			return err
		}
		setOutputValueVariables(variables, output, value)
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(c.Output), 0o755); err != nil {
		return err
	}
	return os.WriteFile(c.Output, []byte(value+"\n"), 0o644)
}

func (c Command) generateIdentifierOCR(ctx context.Context, device Device, engine ocr.Engine, variables map[string]string, dataDir string) error {
	if strings.TrimSpace(c.Find) == "" {
		return fmt.Errorf("find is required for ocr %s", c.Action)
	}
	if strings.TrimSpace(c.Output) == "" {
		return fmt.Errorf("output is required for ocr %s", c.Action)
	}

	bounds, err := c.findOCRTextByTarget(ctx, device, engine, c.Find)
	if err != nil {
		return err
	}
	return c.generateIdentifierOCRAt(ctx, device, variables, bounds, dataDir)
}

func (c Command) generateIdentifierOCRAt(ctx context.Context, device Device, variables map[string]string, bounds *ocr.Bounds, dataDir string) error {
	if strings.TrimSpace(c.Output) == "" {
		return fmt.Errorf("output is required for ocr %s", ActionGenerateIdentifier)
	}
	if bounds == nil {
		return fmt.Errorf("ocr %s did not return bounds", ActionGenerateIdentifier)
	}

	x, y := bounds.Center()
	if _, err := device.Tap(ctx, x, y); err != nil {
		return err
	}
	identifier := generateIdentifier(c.Text, variables)
	if _, err := device.Text(ctx, identifier); err != nil {
		return err
	}

	return c.saveOutputValue(variables, identifier+c.ValueSuffix, dataDir)
}

func resolveCSVOutputPath(output string, dataDir string) string {
	output = strings.TrimSpace(output)
	dataDir = strings.TrimSpace(dataDir)
	if output == "" || dataDir == "" || filepath.IsAbs(output) {
		return output
	}

	cleanOutput := filepath.Clean(output)
	valuesPrefix := "values" + string(filepath.Separator)
	if strings.HasPrefix(cleanOutput, valuesPrefix) {
		return filepath.Join(dataDir, strings.TrimPrefix(cleanOutput, valuesPrefix))
	}
	if filepath.Dir(cleanOutput) == "." {
		return filepath.Join(dataDir, cleanOutput)
	}
	return output
}

func setOutputValueVariables(variables map[string]string, output string, value string) {
	if variables == nil {
		return
	}
	key := normalizeVariableKey(strings.TrimSuffix(filepath.Base(output), filepath.Ext(output)))
	if key == "" {
		return
	}
	value = strings.TrimSpace(value)
	baseKey := key + "." + valueHeader
	setVariable(variables, baseKey, value)
	setDerivedVariables(variables, baseKey, value)
	setVariable(variables, key, value)
	setDerivedVariables(variables, key, value)
}

func generateIdentifier(template string, variables map[string]string) string {
	base := normalizeIdentifier(template)
	if base == "" {
		base = "value"
	}

	hash := fnv.New32a()
	_, _ = hash.Write([]byte(strings.Join([]string{base, variables["device.id"], variables["device.serial"]}, "|")))
	return fmt.Sprintf("%s%03d", base, hash.Sum32()%1000)
}

func normalizeIdentifier(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == 'á' || r == 'à' || r == 'â' || r == 'ã' || r == 'ä':
			builder.WriteByte('a')
		case r == 'é' || r == 'è' || r == 'ê' || r == 'ë':
			builder.WriteByte('e')
		case r == 'í' || r == 'ì' || r == 'î' || r == 'ï':
			builder.WriteByte('i')
		case r == 'ó' || r == 'ò' || r == 'ô' || r == 'õ' || r == 'ö':
			builder.WriteByte('o')
		case r == 'ú' || r == 'ù' || r == 'û' || r == 'ü':
			builder.WriteByte('u')
		case r == 'ç':
			builder.WriteByte('c')
		}
	}
	return builder.String()
}

func (c Command) runOCRThen(ctx context.Context, device Device, bounds *ocr.Bounds) error {
	switch c.Then {
	case "":
		return nil
	case ActionTap:
		if bounds == nil {
			return fmt.Errorf("ocr wait did not return bounds for then action %q", c.Then)
		}
		x, y := bounds.Center()
		_, err := device.Tap(ctx, x, y)
		return err
	case ActionInput:
		if bounds == nil {
			return fmt.Errorf("ocr wait did not return bounds for then action %q", c.Then)
		}
		x, y := bounds.Center()
		if _, err := device.Tap(ctx, x, y); err != nil {
			return err
		}
		_, err := device.Text(ctx, c.Text)
		return err
	default:
		return fmt.Errorf("unsupported ocr wait then action %q", c.Then)
	}
}

func (c Command) findOCRText(ctx context.Context, device Device, engine ocr.Engine) (*ocr.Bounds, error) {
	if strings.TrimSpace(c.Find) == "" {
		return nil, fmt.Errorf("find is required for ocr %s", c.Action)
	}
	return c.findOCRTextByTarget(ctx, device, engine, c.Find)
}

func (c Command) findOCRTextByTarget(ctx context.Context, device Device, engine ocr.Engine, target string) (*ocr.Bounds, error) {
	if strings.TrimSpace(target) == "" {
		return nil, fmt.Errorf("find is required for ocr %s", c.Action)
	}
	tempDir, err := os.MkdirTemp("", "temp-*")
	if err != nil {
		return nil, fmt.Errorf("create ocr temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	screenshotPath := filepath.Join(tempDir, "screen.png")
	var bounds *ocr.Bounds
	err = runOCROperation(ctx, func() error {
		if _, err := device.Screenshot(ctx, screenshotPath); err != nil {
			return err
		}

		var err error
		bounds, err = engine.FindText(ctx, screenshotPath, target, ocr.Options{Lang: c.OCRLang, PSM: c.OCRPSM})
		return err
	})
	if err != nil {
		return nil, err
	}
	return bounds, nil
}

func (c Command) waitForOCRText(ctx context.Context, device Device, engine ocr.Engine) (*ocr.Bounds, error) {
	var bounds *ocr.Bounds
	err := c.waitForCondition(ctx, fmt.Sprintf("ocr text %q", c.Find), func(ctx context.Context) error {
		var err error
		bounds, err = c.findOCRText(ctx, device, engine)
		return err
	})
	return bounds, err
}

func (c Command) runADB(ctx context.Context, r *Runner) error {
	if c.Then != "" {
		return fmt.Errorf("then is not supported for adb")
	}

	var err error
	switch c.Action {
	case ActionHome:
		_, err = r.Device.KeyEvent(ctx, "KEYCODE_HOME")
	case ActionKeyEvent:
		_, err = r.Device.KeyEvent(ctx, c.Key)
	case ActionTap:
		_, err = r.Device.Tap(ctx, *c.X, *c.Y)
	case ActionInput:
		_, err = r.Device.Tap(ctx, *c.X, *c.Y)
		if err != nil {
			break
		}
		_, err = r.Device.Text(ctx, c.Text)
	case ActionSwipe:
		_, err = r.Device.Swipe(ctx, *c.X1, *c.Y1, *c.X2, *c.Y2)
	case ActionText:
		_, err = r.Device.Text(ctx, c.Text)
	case ActionInstall:
		_, err = r.Device.Shell(ctx, "pm", "install", "-r", c.Apk)
	case ActionClearApp:
		_, err = r.Device.Shell(ctx, "pm", "clear", c.Package)
	case ActionScreenshot:
		err = os.MkdirAll(filepath.Dir(c.Screenshot), 0o755)
		if err != nil {
			break
		}
		_, err = r.Device.Screenshot(ctx, c.Screenshot)
	case ActionSetDensity:
		_, err = r.Device.Shell(ctx, "wm", "density", c.Size)
	case ActionResetDensity:
		_, err = r.Device.Shell(ctx, "wm", "density", "reset")
	case ActionSetSize:
		_, err = r.Device.Shell(ctx, "wm", "size", c.Size)
	case ActionResetSize:
		_, err = r.Device.Shell(ctx, "wm", "size", "reset")
	case ActionLongPress:
		_, err = r.Device.LongPress(ctx, *c.X, *c.Y, *c.MS)
	case ActionPull:
		err = os.MkdirAll(filepath.Dir(c.Screenshot), 0o755)
		if err != nil {
			break
		}
		_, err = r.Device.Pull(ctx, c.Screenshot, c.Text)
	case ActionWait:
		err = c.sleep(ctx, "wait", c.Text)
	case ActionShell:
		_, err = r.Device.Shell(ctx, c.Args...)
	default:
		err = fmt.Errorf("unsupported adb action %q", c.Action)
	}

	return err
}

func (c Command) runAppium(ctx context.Context, r *Runner) error {
	if c.Then != "" && c.Action != ActionWait {
		return fmt.Errorf("then is only supported with appium action %q", ActionWait)
	}

	switch c.Action {
	case ActionStartSession:
		_, err := r.ensureAppiumSession(ctx, c)
		return err
	case ActionQuitSession:
		return r.Close(ctx)
	case ActionTap:
		session, elementID, err := r.findAppiumElement(ctx, c)
		if err != nil {
			return err
		}
		return session.ClickElement(ctx, elementID)
	case ActionInput:
		session, elementID, err := r.findAppiumElement(ctx, c)
		if err != nil {
			return err
		}
		if err := session.ClickElement(ctx, elementID); err != nil {
			return err
		}
		return session.SendKeys(ctx, elementID, c.Text)
	case ActionPageSource:
		session, err := r.ensureAppiumSession(ctx, c)
		if err != nil {
			return err
		}
		source, err := session.PageSource(ctx)
		if err != nil {
			return err
		}
		if c.Output == "" {
			return fmt.Errorf("output is required for appium page-source")
		}
		if err := os.MkdirAll(filepath.Dir(c.Output), 0o755); err != nil {
			return err
		}
		return os.WriteFile(c.Output, []byte(source), 0o644)
	case ActionWait:
		if strings.TrimSpace(c.Find) == "" {
			if c.Then != "" {
				return fmt.Errorf("then requires find for appium wait")
			}
			return c.sleep(ctx, "wait", c.Text)
		}
		session, elementID, err := c.waitForAppiumElement(ctx, r)
		if err != nil {
			return err
		}
		return c.runAppiumThen(ctx, session, elementID)
	default:
		return fmt.Errorf("unsupported appium action %q", c.Action)
	}
}

func (c Command) waitForAppiumElement(ctx context.Context, r *Runner) (*appium.Session, string, error) {
	if strings.TrimSpace(c.Find) == "" {
		return nil, "", fmt.Errorf("find is required for appium %s", c.Action)
	}
	using := strings.TrimSpace(c.Using)
	if using == "" {
		using = "xpath"
	}
	session, err := r.ensureAppiumSession(ctx, c)
	if err != nil {
		return nil, "", err
	}

	var elementID string
	err = c.waitForCondition(ctx, fmt.Sprintf("appium element %q", c.Find), func(ctx context.Context) error {
		var err error
		elementID, err = session.FindElement(ctx, using, c.Find)
		return err
	})
	return session, elementID, err
}

func (c Command) runAppiumThen(ctx context.Context, session *appium.Session, elementID string) error {
	switch c.Then {
	case "":
		return nil
	case ActionTap:
		return session.ClickElement(ctx, elementID)
	case ActionInput:
		if err := session.ClickElement(ctx, elementID); err != nil {
			return err
		}
		return session.SendKeys(ctx, elementID, c.Text)
	default:
		return fmt.Errorf("unsupported appium wait then action %q", c.Then)
	}
}

func (r *Runner) findAppiumElement(ctx context.Context, c Command) (*appium.Session, string, error) {
	if c.Find == "" {
		return nil, "", fmt.Errorf("find is required for appium %s", c.Action)
	}
	using := strings.TrimSpace(c.Using)
	if using == "" {
		using = "xpath"
	}
	session, err := r.ensureAppiumSession(ctx, c)
	if err != nil {
		return nil, "", err
	}
	elementID, err := session.FindElement(ctx, using, c.Find)
	if err != nil {
		return nil, "", err
	}
	return session, elementID, nil
}

func (r *Runner) ensureAppiumSession(ctx context.Context, c Command) (*appium.Session, error) {
	if r.AppiumSession != nil {
		return r.AppiumSession, nil
	}
	client := r.Appium
	if client == nil || c.AppiumURL != "" {
		serverURL := c.AppiumURL
		if serverURL == "" {
			serverURL = r.AppiumServerURL
		}
		client = appium.New(serverURL)
		r.Appium = client
	}

	capabilities := r.defaultAppiumCapabilities()
	maps.Copy(capabilities, c.Capabilities)
	session, err := client.CreateSession(ctx, capabilities)
	if err != nil {
		return nil, err
	}
	r.AppiumSession = session
	return session, nil
}

func (r *Runner) defaultAppiumCapabilities() map[string]any {
	capabilities := map[string]any{
		"platformName":          "Android",
		"appium:automationName": "UiAutomator2",
		"appium:noReset":        true,
	}
	if r.Variables == nil {
		return capabilities
	}
	if serial := r.Variables["device.serial"]; serial != "" {
		capabilities["appium:deviceName"] = serial
		capabilities["appium:udid"] = serial
	}
	if index, err := strconv.Atoi(r.Variables["device.index"]); err == nil && index >= 0 {
		capabilities["appium:systemPort"] = 8200 + index
	}
	return capabilities
}

func (r *Runner) Close(ctx context.Context) error {
	if r.AppiumSession == nil {
		return nil
	}
	session := r.AppiumSession
	r.AppiumSession = nil
	return session.Delete(ctx)
}
