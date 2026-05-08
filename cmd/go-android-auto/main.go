package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"log"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	goandroidauto "github.com/bruxaodev/go-android-auto"
	"github.com/bruxaodev/go-android-auto/pkg/adb"
	"github.com/bruxaodev/go-android-auto/pkg/auto"
	"github.com/bruxaodev/go-android-auto/pkg/ocr"
)

type configFS interface {
	fs.ReadDirFS
	fs.ReadFileFS
}

type commandConfig struct {
	timeLinePath               string
	fallbackPath               string
	timeLineIndex              int
	automationDir              string
	adbPath                    string
	deviceSerial               string
	deviceSerials              string
	deviceIDs                  string
	deviceRunMode              string
	detectDeviceIDs            bool
	deviceMapPath              string
	setupDevices               bool
	allDevices                 bool
	tesseractPath              string
	appiumCommand              string
	appiumURL                  string
	appiumURLs                 string
	appiumShards               int
	appiumSessionConcurrency   int
	appiumSessionLimiter       chan struct{}
	appiumSystemPortBase       int
	appiumMjpegPortBase        int
	appiumChromedriverPortBase int
	deviceLogDir               string
	dataDir                    string
	doctor                     bool
}

const (
	appConfigDirName        = "go-android-auto"
	configAutomationDirName = "automation"
	configValuesDirName     = "values"
	configDeviceMapFileName = "device-map.json"
)

type appConfigPaths struct {
	AutomationDir string
	DataDir       string
	DeviceMapPath string
}

const (
	defaultAppiumURL                  = "http://127.0.0.1:4723"
	defaultAppiumDevicesPerShard      = 10
	defaultAppiumSessionConcurrency   = 5
	defaultAppiumSystemPortBase       = 8200
	defaultAppiumMjpegPortBase        = 9200
	defaultAppiumChromedriverPortBase = 10200
	defaultDeviceLogDir               = ".tmp/device-logs"
	deviceRunModeParallel             = "parallel"
	deviceRunModeQueue                = "queue"
)

type appiumServerProcess struct {
	Command *exec.Cmd
	Done    chan error
	URL     string
}

type appiumServerPool struct {
	Servers []*appiumServerProcess
	URLs    []string
}

type deviceTarget struct {
	Serial    string
	DataIndex int
	PortIndex int
	AppiumURL string
}

type detectedDevice struct {
	ID        int    `json:"id"`
	Serial    string `json:"serial"`
	DataIndex int    `json:"data_index"`
}

type deviceMapFile struct {
	Devices []detectedDevice `json:"devices"`
}

func main() {
	log.SetFlags(0)
	ctx := context.Background()
	if len(os.Args) == 1 {
		if err := runTUI(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}

	cfg := commandConfig{}

	flags := parseFlags(&cfg, os.Args[1:])
	if err := runCommand(ctx, cfg, flags); err != nil {
		log.Fatal(err)
	}
}

func runCommand(ctx context.Context, cfg commandConfig, flags *flag.FlagSet) error {
	return runCommandWithOutput(ctx, cfg, flags, os.Stdout)
}

func runCommandWithOutput(ctx context.Context, cfg commandConfig, flags *flag.FlagSet, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}
	deviceRunMode, err := normalizeDeviceRunMode(cfg.deviceRunMode)
	if err != nil {
		return err
	}
	cfg.deviceRunMode = deviceRunMode
	if cfg.doctor {
		return runDoctor(ctx, cfg, output)
	}
	if cfg.setupDevices {
		if err := runDeviceSetup(ctx, cfg, output); err != nil {
			return fmt.Errorf("Error setting up devices: %w", err)
		}
		return nil
	}

	if cfg.timeLinePath == "" {
		if flags != nil {
			flags.Usage()
		}
		return fmt.Errorf("missing required -timeline flag")
	}

	timelinePath, err := resolveTimelinePath(cfg)
	if err != nil {
		return fmt.Errorf("Error resolving timeline: %w", err)
	}

	fmt.Fprintf(output, "Loading timeline from: %s\n", timelinePath)

	loadedTimeline, err := auto.LoadWithMetadata(timelinePath)
	if err != nil {
		return fmt.Errorf("Error loading timeline: %w", err)
	}
	timeline := loadedTimeline.Timeline
	fallbackTimeline, err := loadFallbackTimeline(cfg, loadedTimeline.FallbackPath, output)
	if err != nil {
		return fmt.Errorf("Error loading fallback timeline: %w", err)
	}

	dataSet, err := auto.LoadDataDir(cfg.dataDir)
	if err != nil {
		return fmt.Errorf("Error loading data: %w", err)
	}

	targets, err := resolveDeviceTargets(ctx, cfg, output)
	if err != nil {
		return fmt.Errorf("Error resolving devices: %w", err)
	}

	appiumTimeline := append(auto.Timeline(nil), timeline...)
	appiumTimeline = append(appiumTimeline, fallbackTimeline...)
	targets = assignAppiumTargets(targets, nil)
	if timelineUsesAppium(appiumTimeline) {
		cfg.appiumSessionLimiter = make(chan struct{}, appiumSessionConcurrency(cfg))
	}
	appiumPool, err := ensureAppiumServerPool(ctx, cfg, appiumTimeline, len(targets), output)
	if err != nil {
		return fmt.Errorf("Error starting Appium server: %w", err)
	}
	if appiumPool != nil {
		targets = assignAppiumTargets(targets, appiumPool.URLs)
		defer func() {
			if err := appiumPool.Close(); err != nil {
				fmt.Fprintf(output, "Error stopping Appium server(s): %v\n", err)
			}
		}()
	}

	if err := runTimelineOnDevices(ctx, cfg, timeline, fallbackTimeline, dataSet, targets, output); err != nil {
		return fmt.Errorf("Error running timeline: %w", err)
	}

	return nil
}

type doctorReport struct {
	checks []doctorCheck

	failures int
	warnings int
}

type doctorCheck struct {
	Status  string
	Subject string
	Detail  string
}

func runDoctor(ctx context.Context, cfg commandConfig, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}
	report := &doctorReport{}
	fmt.Fprintln(output, "go-android-auto doctor")

	loadedTimeline, timelineLoaded := report.checkConfig(cfg)
	report.checkBinaries(ctx, cfg, loadedTimeline, timelineLoaded)
	report.checkDevices(ctx, cfg)

	for _, check := range report.checks {
		fmt.Fprintf(output, "[%s] %s", check.Status, check.Subject)
		if strings.TrimSpace(check.Detail) != "" {
			fmt.Fprintf(output, " - %s", check.Detail)
		}
		fmt.Fprintln(output)
	}
	fmt.Fprintf(output, "summary: %d failed, %d warning(s)\n", report.failures, report.warnings)
	if report.failures > 0 {
		return fmt.Errorf("doctor found %d failed check(s)", report.failures)
	}
	return nil
}

func (r *doctorReport) ok(subject string, detail string) {
	r.checks = append(r.checks, doctorCheck{Status: "OK", Subject: subject, Detail: detail})
}

func (r *doctorReport) warn(subject string, detail string) {
	r.warnings++
	r.checks = append(r.checks, doctorCheck{Status: "WARN", Subject: subject, Detail: detail})
}

func (r *doctorReport) fail(subject string, detail string) {
	r.failures++
	r.checks = append(r.checks, doctorCheck{Status: "FAIL", Subject: subject, Detail: detail})
}

func (r *doctorReport) checkConfig(cfg commandConfig) (auto.Timeline, bool) {
	r.checkDir("automation dir", cfg.automationDir)
	r.checkDir("values dir", cfg.dataDir)
	r.checkTimelineScripts(cfg.automationDir)
	r.checkCSVFiles(cfg.dataDir)
	r.checkDeviceMap(cfg.deviceMapPath)

	if strings.TrimSpace(cfg.timeLinePath) == "" {
		r.warn("timeline", "not provided; pass -timeline to validate a specific run")
		return nil, false
	}

	timelinePath, err := resolveTimelinePath(cfg)
	if err != nil {
		r.fail("timeline", err.Error())
		return nil, false
	}
	loaded, err := auto.LoadWithMetadata(timelinePath)
	if err != nil {
		r.fail("timeline", err.Error())
		return nil, false
	}
	r.ok("timeline", fmt.Sprintf("loaded %s (%d command(s))", timelinePath, len(loaded.Timeline)))
	if strings.TrimSpace(cfg.fallbackPath) != "" || strings.TrimSpace(loaded.FallbackPath) != "" {
		fallback, err := loadFallbackTimeline(cfg, loaded.FallbackPath, io.Discard)
		if err != nil {
			r.fail("fallback timeline", err.Error())
		} else if len(fallback) == 0 {
			r.warn("fallback timeline", "configured but loaded no commands")
		} else {
			r.ok("fallback timeline", fmt.Sprintf("loaded %d command(s)", len(fallback)))
		}
	}
	return loaded.Timeline, true
}

func (r *doctorReport) checkDir(subject string, path string) {
	if strings.TrimSpace(path) == "" {
		r.fail(subject, "path is empty")
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		r.fail(subject, err.Error())
		return
	}
	if !info.IsDir() {
		r.fail(subject, fmt.Sprintf("%s is not a directory", path))
		return
	}
	r.ok(subject, path)
}

func (r *doctorReport) checkTimelineScripts(automationDir string) {
	scripts, err := loadTimelineScripts(automationDir)
	if err != nil {
		r.fail("automation scripts", err.Error())
		return
	}
	if len(scripts) == 0 {
		r.fail("automation scripts", "no .yaml/.yml files found")
		return
	}
	r.ok("automation scripts", fmt.Sprintf("found %d script(s)", len(scripts)))
}

func (r *doctorReport) checkCSVFiles(dataDir string) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		r.fail("values csv", err.Error())
		return
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".csv") {
			count++
		}
	}
	if count == 0 {
		r.warn("values csv", "no .csv files found")
		return
	}
	r.ok("values csv", fmt.Sprintf("found %d file(s)", count))
}

func (r *doctorReport) checkDeviceMap(path string) {
	if strings.TrimSpace(path) == "" {
		r.warn("device map", "not configured")
		return
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			r.warn("device map", "missing; run setup devices or enable detect ids when needed")
			return
		}
		r.fail("device map", err.Error())
		return
	}
	var deviceMap deviceMapFile
	if err := json.Unmarshal(content, &deviceMap); err != nil {
		r.fail("device map", err.Error())
		return
	}
	if _, err := validateDeviceMap(path, deviceMap); err != nil {
		r.fail("device map", err.Error())
		return
	}
	r.ok("device map", fmt.Sprintf("%s (%d device(s))", path, len(deviceMap.Devices)))
}

func (r *doctorReport) checkBinaries(ctx context.Context, cfg commandConfig, timeline auto.Timeline, timelineLoaded bool) {
	adbCommand := strings.TrimSpace(cfg.adbPath)
	if adbCommand == "" {
		adbCommand = "adb"
	}
	r.checkCommand("adb", adbCommand)

	needsOCR := cfg.setupDevices || cfg.detectDeviceIDs || (timelineLoaded && timelineUsesOCR(timeline))
	if needsOCR {
		tesseractCommand := strings.TrimSpace(cfg.tesseractPath)
		if tesseractCommand == "" {
			tesseractCommand = "tesseract"
		}
		r.checkCommand("tesseract", tesseractCommand)
	} else {
		r.warn("tesseract", "not checked; no OCR requirement detected")
	}

	if timelineLoaded && timelineUsesAppium(timeline) {
		serverURL := strings.TrimSpace(cfg.appiumURL)
		if serverURL == "" {
			serverURL = "http://127.0.0.1:4723"
		}
		if appiumStatusOK(ctx, serverURL) {
			r.ok("appium server", serverURL)
			return
		}
		appiumCommand := strings.TrimSpace(cfg.appiumCommand)
		if appiumCommand == "" {
			appiumCommand = "appium"
		}
		r.checkCommand("appium", appiumCommand)
		return
	}
	r.warn("appium", "not checked; selected timeline does not require it")
}

func (r *doctorReport) checkCommand(subject string, command string) {
	if strings.TrimSpace(command) == "" {
		r.fail(subject, "command path is empty")
		return
	}
	if filepath.IsAbs(command) || strings.ContainsRune(command, filepath.Separator) {
		info, err := os.Stat(command)
		if err != nil {
			r.fail(subject, err.Error())
			return
		}
		if info.IsDir() {
			r.fail(subject, fmt.Sprintf("%s is a directory", command))
			return
		}
		if info.Mode()&0o111 == 0 {
			r.fail(subject, fmt.Sprintf("%s is not executable", command))
			return
		}
		r.ok(subject, command)
		return
	}
	path, err := exec.LookPath(command)
	if err != nil {
		r.fail(subject, err.Error())
		return
	}
	r.ok(subject, path)
}

func (r *doctorReport) checkDevices(ctx context.Context, cfg commandConfig) {
	if cfg.deviceIDs != "" && cfg.detectDeviceIDs {
		r.fail("device ids", "use only one of -device-ids or -detect-device-ids")
	}
	serials, err := resolveDeviceSerials(ctx, cfg)
	if err != nil {
		r.fail("devices", err.Error())
		return
	}
	if len(serials) == 0 || (len(serials) == 1 && strings.TrimSpace(serials[0]) == "") {
		r.warn("devices", "no explicit device selected; default adb serial will be used")
		return
	}
	r.ok("devices", strings.Join(displaySerials(serials), ", "))
	if _, err := resolveDeviceDataIndexes(ctx, cfg, serials, io.Discard); err != nil {
		r.fail("device data mapping", err.Error())
		return
	}
	r.ok("device data mapping", "ready")
}

func timelineUsesOCR(timeline auto.Timeline) bool {
	for _, command := range timeline {
		if command.Type == auto.CommandOCR {
			return true
		}
	}
	return false
}

func displaySerials(serials []string) []string {
	displayed := make([]string, 0, len(serials))
	for _, serial := range serials {
		displayed = append(displayed, displaySerial(serial))
	}
	return displayed
}

func parseFlags(cfg *commandConfig, args []string) *flag.FlagSet {
	configPaths, err := defaultAppConfigPaths()
	if err != nil {
		log.Fatal(err)
	}

	flags := flag.NewFlagSet("go-android-auto", flag.ExitOnError)
	flags.StringVar(&cfg.timeLinePath, "timeline", "", "Path to the timeline YAML file")
	flags.StringVar(&cfg.fallbackPath, "fallback-timeline", "", "Timeline YAML file to run on a device after its main timeline fails")
	flags.IntVar(&cfg.timeLineIndex, "index", 0, "Starting index in the timeline")
	flags.StringVar(&cfg.automationDir, "automation", configPaths.AutomationDir, "Default directory used to resolve timeline YAML files")
	flags.StringVar(&cfg.adbPath, "adb", "", "Path to the adb binary")
	flags.StringVar(&cfg.deviceSerial, "serial", "", "ADB device serial to target")
	flags.StringVar(&cfg.deviceSerials, "serials", "", "Comma-separated ADB device serials to target; order defines device ids")
	flags.StringVar(&cfg.deviceIDs, "device-ids", "", "Comma-separated 1-based device ids matching selected devices; id 1 uses data row 0")
	flags.StringVar(&cfg.deviceRunMode, "device-run-mode", deviceRunModeParallel, "How multiple devices run: parallel or queue")
	flags.BoolVar(&cfg.detectDeviceIDs, "detect-device-ids", false, "Detect each 1-based device id from the wallpaper/background number using OCR")
	flags.StringVar(&cfg.deviceMapPath, "device-map", configPaths.DeviceMapPath, "Path to read/save the device id to ADB serial map")
	flags.BoolVar(&cfg.setupDevices, "setup-devices", false, "Detect device ids from wallpaper/background OCR, save -device-map, and exit")
	flags.BoolVar(&cfg.allDevices, "all-devices", false, "Run the timeline on every connected ADB device")
	flags.StringVar(&cfg.tesseractPath, "tesseract", "", "Path to the tesseract binary")
	flags.StringVar(&cfg.appiumCommand, "appium", "appium", "Path to the Appium executable used when appium timeline actions are present")
	flags.StringVar(&cfg.appiumURL, "appium-url", "", "Appium server URL used by appium timeline actions")
	flags.StringVar(&cfg.appiumURLs, "appium-urls", "", "Comma-separated Appium server URLs used as shards for multi-device runs")
	flags.IntVar(&cfg.appiumShards, "appium-shards", 0, "Number of Appium server shards to use/start; 0 auto-sizes by device count")
	flags.IntVar(&cfg.appiumSessionConcurrency, "appium-session-concurrency", defaultAppiumSessionConcurrency, "Maximum concurrent Appium session creations")
	flags.IntVar(&cfg.appiumSystemPortBase, "appium-system-port-base", defaultAppiumSystemPortBase, "Base appium:systemPort assigned as base + device port index")
	flags.IntVar(&cfg.appiumMjpegPortBase, "appium-mjpeg-port-base", defaultAppiumMjpegPortBase, "Base appium:mjpegServerPort assigned as base + device port index")
	flags.IntVar(&cfg.appiumChromedriverPortBase, "appium-chromedriver-port-base", defaultAppiumChromedriverPortBase, "Base appium:chromedriverPort assigned as base + device port index")
	flags.StringVar(&cfg.deviceLogDir, "device-log-dir", defaultDeviceLogDir, "Directory for per-device log files during multi-device runs; empty disables files")
	flags.StringVar(&cfg.dataDir, "values", configPaths.DataDir, "Directory with CSV data files used by {{file.column}} timeline variables")
	flags.BoolVar(&cfg.doctor, "doctor", false, "Check config, dependencies, devices, and selected timeline readiness")
	if err := flags.Parse(args); err != nil {
		log.Fatal(err)
	}
	if err := ensureDefaultAppConfigDirs(configPaths); err != nil {
		log.Fatal(err)
	}
	if err := installDefaultAppConfigFiles(configPaths, flags); err != nil {
		log.Fatal(err)
	}
	return flags
}

func defaultAppConfigPaths() (appConfigPaths, error) {
	baseDir, err := os.UserConfigDir()
	if err != nil {
		return appConfigPaths{}, fmt.Errorf("resolve user config dir: %w", err)
	}

	root := filepath.Join(baseDir, appConfigDirName)
	return appConfigPaths{
		AutomationDir: filepath.Join(root, configAutomationDirName),
		DataDir:       filepath.Join(root, configValuesDirName),
		DeviceMapPath: filepath.Join(root, configDeviceMapFileName),
	}, nil
}

func ensureDefaultAppConfigDirs(paths appConfigPaths) error {
	for _, dir := range []string{paths.AutomationDir, paths.DataDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create app config directory %s: %w", dir, err)
		}
	}
	return nil
}

func installDefaultAppConfigFiles(paths appConfigPaths, flags *flag.FlagSet) error {
	if !flagProvided(flags, "automation") {
		if err := copyEmbeddedConfigDir(goandroidauto.DefaultFiles, configAutomationDirName, paths.AutomationDir, map[string]struct{}{".yaml": {}, ".yml": {}}); err != nil {
			return err
		}
	}
	if !flagProvided(flags, "values") {
		if err := copyEmbeddedConfigDir(goandroidauto.DefaultFiles, configValuesDirName, paths.DataDir, map[string]struct{}{".csv": {}}); err != nil {
			return err
		}
	}
	return nil
}

func flagProvided(flags *flag.FlagSet, name string) bool {
	provided := false
	flags.Visit(func(flag *flag.Flag) {
		if flag.Name == name {
			provided = true
		}
	})
	return provided
}

func copyEmbeddedConfigDir(sourceFS configFS, sourceDir string, destinationDir string, allowedExtensions map[string]struct{}) error {
	entries, err := sourceFS.ReadDir(sourceDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read embedded config directory %s: %w", sourceDir, err)
	}

	for _, entry := range entries {
		sourcePath := filepath.ToSlash(filepath.Join(sourceDir, entry.Name()))
		destinationPath := filepath.Join(destinationDir, entry.Name())
		if entry.IsDir() {
			if err := copyEmbeddedConfigDir(sourceFS, sourcePath, destinationPath, allowedExtensions); err != nil {
				return err
			}
			continue
		}
		if _, ok := allowedExtensions[strings.ToLower(filepath.Ext(entry.Name()))]; !ok {
			continue
		}
		if err := copyEmbeddedConfigFile(sourceFS, destinationPath, sourcePath); err != nil {
			return err
		}
	}
	return nil
}

func copyEmbeddedConfigFile(sourceFS configFS, destinationPath string, sourcePath string) error {
	if _, err := os.Stat(destinationPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read app config file %s: %w", destinationPath, err)
	}

	content, err := sourceFS.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read embedded config file %s: %w", sourcePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return fmt.Errorf("create app config directory %s: %w", filepath.Dir(destinationPath), err)
	}
	if err := os.WriteFile(destinationPath, content, 0o644); err != nil {
		return fmt.Errorf("write app config file %s: %w", destinationPath, err)
	}
	return nil
}

func resolveTimelinePath(cfg commandConfig) (string, error) {
	return resolveTimelinePathValue(cfg.timeLinePath, cfg.automationDir, "-timeline")
}

func resolveTimelinePathValue(rawPath string, automationDir string, flagName string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", fmt.Errorf("%s is required", flagName)
	}
	if !filepath.IsAbs(path) && automationDir != "" {
		if relativePath, ok := trimRelativeDirPrefix(path, configAutomationDirName); ok {
			defaultPath := filepath.Join(automationDir, relativePath)
			if _, err := os.Stat(defaultPath); err == nil {
				return defaultPath, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	if filepath.IsAbs(path) || automationDir == "" {
		return path, nil
	}

	defaultPath := filepath.Join(automationDir, path)
	if _, err := os.Stat(defaultPath); err == nil {
		return defaultPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	return path, nil
}

func trimRelativeDirPrefix(path string, dirName string) (string, bool) {
	cleanPath := filepath.Clean(path)
	if cleanPath == "." || filepath.IsAbs(cleanPath) {
		return "", false
	}
	prefix := dirName + string(filepath.Separator)
	if !strings.HasPrefix(cleanPath, prefix) {
		return "", false
	}
	return strings.TrimPrefix(cleanPath, prefix), true
}

func loadFallbackTimeline(cfg commandConfig, embeddedFallbackPath string, output ...io.Writer) (auto.Timeline, error) {
	writer := outputWriter(output...)
	fallbackPath := strings.TrimSpace(cfg.fallbackPath)
	flagName := "-fallback-timeline"
	if fallbackPath == "" {
		fallbackPath = strings.TrimSpace(embeddedFallbackPath)
		flagName = "fallback"
	}
	if fallbackPath == "" {
		return nil, nil
	}

	fallbackPath, err := resolveTimelinePathValue(fallbackPath, cfg.automationDir, flagName)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(writer, "Loading fallback timeline from: %s\n", fallbackPath)
	return auto.Load(fallbackPath)
}

func runDeviceSetup(ctx context.Context, cfg commandConfig, output ...io.Writer) error {
	writer := outputWriter(output...)
	if cfg.deviceMapPath == "" {
		return fmt.Errorf("-device-map is required for -setup-devices")
	}
	if cfg.deviceIDs != "" {
		return fmt.Errorf("use -setup-devices without -device-ids; setup detects ids from OCR")
	}
	if err := removeExistingDeviceMap(cfg.deviceMapPath); err != nil {
		return err
	}

	serials, err := resolveDeviceSerials(ctx, cfg)
	if err != nil {
		return err
	}

	if _, err := detectDeviceDataIndexes(ctx, cfg, serials, writer); err != nil {
		return err
	}

	fmt.Fprintf(writer, "Device setup complete. Future runs will use %s when matching serials are selected.\n", cfg.deviceMapPath)
	return nil
}

func outputWriter(output ...io.Writer) io.Writer {
	if len(output) == 0 || output[0] == nil {
		return os.Stdout
	}
	return output[0]
}

func removeExistingDeviceMap(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove existing device map %s: %w", path, err)
	}
	return nil
}

func resolveDeviceTargets(ctx context.Context, cfg commandConfig, output ...io.Writer) ([]deviceTarget, error) {
	serials, err := resolveDeviceSerials(ctx, cfg)
	if err != nil {
		return nil, err
	}

	dataIndexes, err := resolveDeviceDataIndexes(ctx, cfg, serials, output...)
	if err != nil {
		return nil, err
	}

	targets := make([]deviceTarget, len(serials))
	for i, serial := range serials {
		targets[i] = deviceTarget{Serial: serial, DataIndex: dataIndexes[i], PortIndex: i}
	}

	return targets, nil
}

func assignAppiumTargets(targets []deviceTarget, appiumURLs []string) []deviceTarget {
	assigned := make([]deviceTarget, len(targets))
	copy(assigned, targets)
	for i := range assigned {
		assigned[i].PortIndex = i
		if len(appiumURLs) > 0 {
			assigned[i].AppiumURL = appiumURLs[i%len(appiumURLs)]
		}
	}
	return assigned
}

func appiumSessionConcurrency(cfg commandConfig) int {
	if cfg.appiumSessionConcurrency <= 0 {
		return defaultAppiumSessionConcurrency
	}
	return cfg.appiumSessionConcurrency
}

func resolveDeviceSerials(ctx context.Context, cfg commandConfig) ([]string, error) {
	selectedModes := 0
	if cfg.allDevices {
		selectedModes++
	}
	if cfg.deviceSerial != "" {
		selectedModes++
	}
	if cfg.deviceSerials != "" {
		selectedModes++
	}
	if selectedModes > 1 {
		return nil, fmt.Errorf("use only one of -serial, -serials, or -all-devices")
	}

	if cfg.allDevices {
		devices, err := adb.New("", cfg.adbPath).Devices(ctx)
		if err != nil {
			return nil, err
		}
		sort.Strings(devices)
		if len(devices) == 0 {
			return nil, fmt.Errorf("no connected ADB devices found")
		}
		return devices, nil
	}

	if cfg.deviceSerials != "" {
		parts := strings.Split(cfg.deviceSerials, ",")
		serials := make([]string, 0, len(parts))
		for _, part := range parts {
			serial := strings.TrimSpace(part)
			if serial == "" {
				continue
			}
			serials = append(serials, serial)
		}
		if len(serials) == 0 {
			return nil, fmt.Errorf("-serials did not contain any serial")
		}
		return serials, nil
	}

	return []string{cfg.deviceSerial}, nil
}

func resolveDeviceDataIndexes(ctx context.Context, cfg commandConfig, serials []string, output ...io.Writer) ([]int, error) {
	writer := outputWriter(output...)
	if cfg.detectDeviceIDs && cfg.deviceIDs != "" {
		return nil, fmt.Errorf("use only one of -device-ids or -detect-device-ids")
	}

	if cfg.detectDeviceIDs {
		return detectDeviceDataIndexes(ctx, cfg, serials, writer)
	}

	if cfg.deviceIDs != "" {
		return parseDeviceIDIndexes(cfg.deviceIDs, len(serials))
	}

	if indexes, ok, err := deviceMapDataIndexes(cfg.deviceMapPath, serials); err != nil {
		return nil, err
	} else if ok {
		fmt.Fprintf(writer, "Using device map from %s\n", cfg.deviceMapPath)
		return indexes, nil
	}

	{
		indexes := make([]int, len(serials))
		for i := range indexes {
			indexes[i] = i
		}
		return indexes, nil
	}
}

func parseDeviceIDIndexes(raw string, count int) ([]int, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != count {
		return nil, fmt.Errorf("-device-ids must contain %d id(s), got %d", count, len(parts))
	}

	indexes := make([]int, count)
	for i, part := range parts {
		id, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid device id %q at position %d", part, i+1)
		}
		indexes[i] = id - 1
	}

	return indexes, nil
}

func deviceMapDataIndexes(path string, serials []string) ([]int, bool, error) {
	if path == "" {
		return nil, false, nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read device map %s: %w", path, err)
	}

	var deviceMap deviceMapFile
	if err := json.Unmarshal(content, &deviceMap); err != nil {
		return nil, false, fmt.Errorf("parse device map %s: %w", path, err)
	}
	if len(deviceMap.Devices) == 0 {
		return nil, false, fmt.Errorf("device map %s has no devices", path)
	}

	bySerial, err := validateDeviceMap(path, deviceMap)
	if err != nil {
		return nil, false, err
	}

	indexes := make([]int, len(serials))
	for i, serial := range serials {
		mappedDevice, ok := bySerial[displaySerial(serial)]
		if !ok {
			return nil, false, fmt.Errorf("device map %s does not contain serial %s; run setup again or use -detect-device-ids", path, displaySerial(serial))
		}
		indexes[i] = mappedDevice.DataIndex
	}

	return indexes, true, nil
}

func validateDeviceMap(path string, deviceMap deviceMapFile) (map[string]detectedDevice, error) {
	bySerial := make(map[string]detectedDevice, len(deviceMap.Devices))
	seenIDs := make(map[int]string, len(deviceMap.Devices))
	for _, device := range deviceMap.Devices {
		if device.ID <= 0 {
			return nil, fmt.Errorf("device map %s has invalid id %d for serial %s", path, device.ID, device.Serial)
		}
		if device.DataIndex != device.ID-1 {
			return nil, fmt.Errorf("device map %s has data_index %d for id %d; expected %d", path, device.DataIndex, device.ID, device.ID-1)
		}
		if previousSerial, ok := seenIDs[device.ID]; ok {
			return nil, fmt.Errorf("device map %s has duplicate id %d for %s and %s", path, device.ID, previousSerial, device.Serial)
		}
		if _, ok := bySerial[device.Serial]; ok {
			return nil, fmt.Errorf("device map %s has duplicate serial %s", path, device.Serial)
		}
		seenIDs[device.ID] = device.Serial
		bySerial[device.Serial] = device
	}
	return bySerial, nil
}

func detectDeviceDataIndexes(ctx context.Context, cfg commandConfig, serials []string, output ...io.Writer) ([]int, error) {
	writer := outputWriter(output...)
	indexes := make([]int, len(serials))
	seenIDs := make(map[int]string, len(serials))
	detected := make([]detectedDevice, 0, len(serials))
	maxDeviceID := len(serials)
	for i, serial := range serials {
		id, err := detectDeviceID(ctx, cfg, serial, maxDeviceID)
		if err != nil {
			return nil, fmt.Errorf("detect device id for %s: %w", displaySerial(serial), err)
		}
		if previousSerial, ok := seenIDs[id]; ok {
			return nil, fmt.Errorf("detected duplicate device id %d for %s and %s", id, previousSerial, displaySerial(serial))
		}
		seenIDs[id] = displaySerial(serial)
		indexes[i] = id - 1
		detected = append(detected, detectedDevice{ID: id, Serial: displaySerial(serial), DataIndex: id - 1})
		fmt.Fprintf(writer, "[device %d serial %s] Detected wallpaper id %02d via OCR\n", id, displaySerial(serial), id)
	}

	if cfg.deviceMapPath != "" {
		if err := saveDeviceMap(cfg.deviceMapPath, detected); err != nil {
			return nil, err
		}
		fmt.Fprintf(writer, "Saved detected device map to %s\n", cfg.deviceMapPath)
	}
	return indexes, nil
}

func detectDeviceID(ctx context.Context, cfg commandConfig, serial string, maxDeviceID int) (int, error) {
	device := adb.New(serial, cfg.adbPath)
	if _, err := device.KeyEvent(ctx, "KEYCODE_WAKEUP"); err != nil {
		return 0, fmt.Errorf("wake device before screenshot: %w", err)
	}
	if _, err := device.Shell(ctx, "wm", "dismiss-keyguard"); err != nil {
		return 0, fmt.Errorf("dismiss keyguard before screenshot: %w", err)
	}
	if _, err := device.KeyEvent(ctx, "KEYCODE_HOME"); err != nil {
		return 0, fmt.Errorf("open home before screenshot: %w", err)
	}
	if err := waitForWallpaper(ctx); err != nil {
		return 0, err
	}

	screenshot, err := createLocalTempFile("go-android-auto-device-id-*.png")
	if err != nil {
		return 0, fmt.Errorf("create temp screenshot: %w", err)
	}
	screenshotPath := screenshot.Name()
	if err := screenshot.Close(); err != nil {
		return 0, fmt.Errorf("close temp screenshot: %w", err)
	}
	defer os.Remove(screenshotPath)

	if _, err := device.Screenshot(ctx, screenshotPath); err != nil {
		return 0, err
	}

	tesseract := ocr.NewTesseract(cfg.tesseractPath)
	words, err := tesseract.Words(ctx, screenshotPath, ocr.Options{PSM: 11})
	if err != nil {
		return 0, err
	}

	filteredWords := words
	if screenshotBounds, boundsErr := imageFileBounds(screenshotPath); boundsErr == nil {
		filteredWords = filterWallpaperOCRWords(words, screenshotBounds)
	}
	id, err := parseWallpaperDeviceIDFromWordsWithMax(filteredWords, maxDeviceID)
	if err == nil {
		return id, nil
	}

	preprocessedID, preprocessedErr := detectWallpaperDeviceIDFromImage(ctx, tesseract, screenshotPath, maxDeviceID)
	if preprocessedErr == nil {
		return preprocessedID, nil
	}

	return 0, fmt.Errorf("%w; preprocessed digit OCR failed: %v", err, preprocessedErr)
}

var wallpaperDeviceIDPattern = regexp.MustCompile(`\d+`)

const (
	minWallpaperDeviceIDWidth  = 40
	minWallpaperDeviceIDHeight = 40
	minWallpaperDeviceIDArea   = 2500
)

type wallpaperDeviceIDCandidate struct {
	ID   int
	Raw  string
	Area int
}

type preprocessedOCRImage struct {
	Path      string
	CropIndex int
	Threshold int
}

type digitComponent struct {
	Rect image.Rectangle
	Area int
}

func parseWallpaperDeviceIDFromWordsWithMax(words []ocr.Word, maxDeviceID int) (int, error) {
	candidates := make([]wallpaperDeviceIDCandidate, 0)
	smallNumericWords := make([]string, 0)
	outOfRangeNumericWords := make([]string, 0)
	visibleWords := make([]string, 0, len(words))
	for _, word := range words {
		text := strings.TrimSpace(word.Text)
		if text == "" {
			continue
		}
		visibleWords = append(visibleWords, text)

		matches := wallpaperDeviceIDPattern.FindAllString(text, -1)
		if len(matches) != 1 {
			continue
		}

		id, err := strconv.Atoi(matches[0])
		if err != nil || id <= 0 {
			continue
		}
		if !validWallpaperDeviceID(id, maxDeviceID) {
			outOfRangeNumericWords = append(outOfRangeNumericWords, text)
			continue
		}

		width := word.Bounds.Right - word.Bounds.Left
		height := word.Bounds.Bottom - word.Bounds.Top
		area := width * height
		if width < minWallpaperDeviceIDWidth || height < minWallpaperDeviceIDHeight || area < minWallpaperDeviceIDArea {
			smallNumericWords = append(smallNumericWords, text)
			continue
		}
		candidates = append(candidates, wallpaperDeviceIDCandidate{ID: id, Raw: text, Area: area})
	}

	if len(candidates) == 0 {
		if len(outOfRangeNumericWords) > 0 {
			return 0, fmt.Errorf("no valid wallpaper device id found in OCR words %v; ignored out-of-range numeric words %v for selected device count %d", visibleWords, outOfRangeNumericWords, maxDeviceID)
		}
		if len(smallNumericWords) > 0 {
			return 0, fmt.Errorf("no wallpaper-sized numeric device id found in OCR words %v; ignored small numeric words %v", visibleWords, smallNumericWords)
		}
		return 0, fmt.Errorf("no numeric device id found in OCR words %v", visibleWords)
	}

	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.Area > best.Area {
			best = candidate
		}
	}

	for _, candidate := range candidates {
		if candidate.Area == best.Area && candidate.ID != best.ID {
			return 0, fmt.Errorf("ambiguous numeric device ids with same OCR area: %q and %q in words %v", best.Raw, candidate.Raw, visibleWords)
		}
	}

	return best.ID, nil
}

func filterWallpaperOCRWords(words []ocr.Word, bounds image.Rectangle) []ocr.Word {
	if bounds.Empty() {
		return nil
	}
	minY := bounds.Min.Y + bounds.Dy()*30/100
	maxY := bounds.Min.Y + bounds.Dy()*58/100
	minX := bounds.Min.X + bounds.Dx()*5/100
	maxX := bounds.Min.X + bounds.Dx()*95/100
	filtered := make([]ocr.Word, 0, len(words))
	for _, word := range words {
		centerX := (word.Bounds.Left + word.Bounds.Right) / 2
		centerY := (word.Bounds.Top + word.Bounds.Bottom) / 2
		if centerX < minX || centerX > maxX || centerY < minY || centerY > maxY {
			continue
		}
		filtered = append(filtered, word)
	}
	return filtered
}

func imageFileBounds(path string) (image.Rectangle, error) {
	file, err := os.Open(path)
	if err != nil {
		return image.Rectangle{}, fmt.Errorf("open screenshot for bounds: %w", err)
	}
	defer file.Close()

	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return image.Rectangle{}, fmt.Errorf("decode screenshot bounds: %w", err)
	}
	return image.Rect(0, 0, config.Width, config.Height), nil
}

func validWallpaperDeviceID(id int, maxDeviceID int) bool {
	return id > 0 && (maxDeviceID <= 0 || id <= maxDeviceID)
}

func waitForWallpaper(ctx context.Context) error {
	timer := time.NewTimer(800 * time.Millisecond)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func detectWallpaperDeviceIDFromImage(ctx context.Context, tesseract *ocr.Tesseract, screenshotPath string, maxDeviceID int) (int, error) {
	images, err := createWallpaperDigitOCRImages(screenshotPath)
	if err != nil {
		return 0, err
	}
	for _, image := range images {
		defer os.Remove(image.Path)
	}

	psmValues := []int{7, 6, 13, 11}
	attempts := make([]string, 0, len(images)*len(psmValues))
	for _, cropIndex := range preprocessedCropIndexes(images) {
		for _, psm := range psmValues {
			votes := make(map[int]int)
			for _, image := range images {
				if image.CropIndex != cropIndex {
					continue
				}
				text, err := tesseract.DigitText(ctx, image.Path, ocr.Options{PSM: psm})
				if err != nil {
					attempts = append(attempts, fmt.Sprintf("crop %d threshold %d psm %d: %v", image.CropIndex, image.Threshold, psm, err))
					continue
				}

				ids := validDigitIDsFromText(text, maxDeviceID)
				attempts = append(attempts, fmt.Sprintf("crop %d threshold %d psm %d: %q", image.CropIndex, image.Threshold, psm, strings.TrimSpace(text)))
				recordDigitVotes(votes, ids)
			}
			id, err := bestDigitIDFromVotes(votes, attempts)
			if err == nil {
				return id, nil
			}
		}
	}

	return 0, fmt.Errorf("no unambiguous numeric device id found after preprocessing; OCR attempts %v", attempts)
}

func createWallpaperDigitOCRImages(screenshotPath string) ([]preprocessedOCRImage, error) {
	file, err := os.Open(screenshotPath)
	if err != nil {
		return nil, fmt.Errorf("open screenshot for preprocessing: %w", err)
	}
	defer file.Close()

	screenshot, _, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode screenshot for preprocessing: %w", err)
	}

	bounds := screenshot.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	cropBounds := make([]image.Rectangle, 0, 3)
	if digitCrop, ok := wallpaperDigitCropBounds(screenshot); ok {
		cropBounds = append(cropBounds, digitCrop)
	}
	cropBounds = append(cropBounds,
		image.Rect(bounds.Min.X, bounds.Min.Y+height*30/100, bounds.Min.X+width, bounds.Min.Y+height*58/100),
		image.Rect(bounds.Min.X, bounds.Min.Y+height*26/100, bounds.Min.X+width, bounds.Min.Y+height*62/100),
	)
	thresholds := []int{170, 210, 245, 255}

	images := make([]preprocessedOCRImage, 0, len(cropBounds)*len(thresholds))
	for cropIndex, crop := range cropBounds {
		for _, threshold := range thresholds {
			path, err := writePreprocessedDigitImage(screenshot, crop, threshold, cropIndex)
			if err != nil {
				for _, created := range images {
					os.Remove(created.Path)
				}
				return nil, err
			}
			images = append(images, preprocessedOCRImage{Path: path, CropIndex: cropIndex, Threshold: threshold})
		}
	}

	return images, nil
}

func wallpaperDigitCropBounds(source image.Image) (image.Rectangle, bool) {
	bounds := source.Bounds()
	search := image.Rect(
		bounds.Min.X,
		bounds.Min.Y+bounds.Dy()*30/100,
		bounds.Max.X,
		bounds.Min.Y+bounds.Dy()*58/100,
	)
	components := wallpaperDigitComponents(source, search)
	if len(components) == 0 {
		return image.Rectangle{}, false
	}

	sort.Slice(components, func(i, j int) bool {
		return components[i].Area > components[j].Area
	})
	if len(components) > 3 {
		components = components[:3]
	}

	crop := components[0].Rect
	for _, component := range components[1:] {
		crop = crop.Union(component.Rect)
	}
	crop = image.Rect(crop.Min.X-24, crop.Min.Y-24, crop.Max.X+24, crop.Max.Y+24).Intersect(bounds)
	return crop, !crop.Empty()
}

func wallpaperDigitComponents(source image.Image, search image.Rectangle) []digitComponent {
	search = search.Intersect(source.Bounds())
	if search.Empty() {
		return nil
	}

	width := search.Dx()
	height := search.Dy()
	visited := make([]bool, width*height)
	components := make([]digitComponent, 0)
	stack := make([]image.Point, 0, 4096)

	for y := search.Min.Y; y < search.Max.Y; y++ {
		for x := search.Min.X; x < search.Max.X; x++ {
			idx := (y-search.Min.Y)*width + (x - search.Min.X)
			if visited[idx] || !isWallpaperDigitPixel(source, x, y, 255) {
				visited[idx] = true
				continue
			}

			area := 0
			rect := image.Rect(x, y, x+1, y+1)
			stack = append(stack[:0], image.Point{X: x, Y: y})
			visited[idx] = true
			for len(stack) > 0 {
				point := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				area++
				if point.X < rect.Min.X {
					rect.Min.X = point.X
				}
				if point.Y < rect.Min.Y {
					rect.Min.Y = point.Y
				}
				if point.X+1 > rect.Max.X {
					rect.Max.X = point.X + 1
				}
				if point.Y+1 > rect.Max.Y {
					rect.Max.Y = point.Y + 1
				}

				neighbors := [...]image.Point{
					{X: point.X + 1, Y: point.Y},
					{X: point.X - 1, Y: point.Y},
					{X: point.X, Y: point.Y + 1},
					{X: point.X, Y: point.Y - 1},
				}
				for _, next := range neighbors {
					if !next.In(search) {
						continue
					}
					nextIdx := (next.Y-search.Min.Y)*width + (next.X - search.Min.X)
					if visited[nextIdx] {
						continue
					}
					visited[nextIdx] = true
					if isWallpaperDigitPixel(source, next.X, next.Y, 255) {
						stack = append(stack, next)
					}
				}
			}

			if area >= 5000 && rect.Dx() >= 40 && rect.Dy() >= 120 {
				components = append(components, digitComponent{Rect: rect, Area: area})
			}
		}
	}

	return components
}

func preprocessedCropIndexes(images []preprocessedOCRImage) []int {
	seen := make(map[int]struct{}, len(images))
	indexes := make([]int, 0)
	for _, image := range images {
		if _, ok := seen[image.CropIndex]; ok {
			continue
		}
		seen[image.CropIndex] = struct{}{}
		indexes = append(indexes, image.CropIndex)
	}
	sort.Ints(indexes)
	return indexes
}

func writePreprocessedDigitImage(source image.Image, crop image.Rectangle, threshold int, cropIndex int) (string, error) {
	const scale = 2
	const border = 32
	out := image.NewGray(image.Rect(0, 0, crop.Dx()*scale+border*2, crop.Dy()*scale+border*2))
	for y := out.Bounds().Min.Y; y < out.Bounds().Max.Y; y++ {
		for x := out.Bounds().Min.X; x < out.Bounds().Max.X; x++ {
			out.SetGray(x, y, color.Gray{Y: 255})
		}
	}
	for y := crop.Min.Y; y < crop.Max.Y; y++ {
		for x := crop.Min.X; x < crop.Max.X; x++ {
			value := uint8(255)
			if isWallpaperDigitPixel(source, x, y, threshold) {
				value = 0
			}
			for dy := range scale {
				for dx := range scale {
					out.SetGray(border+(x-crop.Min.X)*scale+dx, border+(y-crop.Min.Y)*scale+dy, color.Gray{Y: value})
				}
			}
		}
	}

	file, err := createLocalTempFile(fmt.Sprintf("go-android-auto-device-id-crop-%d-threshold-%d-*.png", cropIndex, threshold))
	if err != nil {
		return "", fmt.Errorf("create preprocessed OCR image: %w", err)
	}
	path := file.Name()
	if err := png.Encode(file, out); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("encode preprocessed OCR image: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close preprocessed OCR image: %w", err)
	}
	return path, nil
}

func createLocalTempFile(pattern string) (*os.File, error) {
	if err := os.MkdirAll(".tmp", 0o755); err != nil {
		return nil, fmt.Errorf("create .tmp directory: %w", err)
	}
	return os.CreateTemp(".tmp", pattern)
}

func isWallpaperDigitPixel(source image.Image, x int, y int, maxLuma int) bool {
	r, g, b, _ := source.At(x, y).RGBA()
	luma := int((299*int(r>>8) + 587*int(g>>8) + 114*int(b>>8)) / 1000)
	return luma >= 40 && luma <= maxLuma
}

func validDigitIDsFromText(text string, maxDeviceID int) []int {
	matches := wallpaperDeviceIDPattern.FindAllString(text, -1)
	ids := make([]int, 0, len(matches))
	seen := make(map[int]struct{}, len(matches))
	for _, match := range matches {
		id, err := strconv.Atoi(match)
		if err != nil || !validWallpaperDeviceID(id, maxDeviceID) {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func recordDigitVotes(votes map[int]int, ids []int) {
	if len(ids) == 0 {
		return
	}
	weight := 1
	if len(ids) == 1 {
		weight = 2
	}
	for _, id := range ids {
		votes[id] += weight
	}
}

func bestDigitIDFromVotes(votes map[int]int, attempts []string) (int, error) {
	if len(votes) == 0 {
		return 0, fmt.Errorf("no numeric device id found after preprocessing; OCR attempts %v", attempts)
	}

	candidates := make([]wallpaperDeviceIDCandidate, 0, len(votes))
	for id, count := range votes {
		candidates = append(candidates, wallpaperDeviceIDCandidate{ID: id, Raw: strconv.Itoa(id), Area: count})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Area == candidates[j].Area {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].Area > candidates[j].Area
	})

	if len(candidates) > 1 && candidates[0].Area == candidates[1].Area {
		return 0, fmt.Errorf("ambiguous preprocessed numeric device ids %v from OCR attempts %v", candidates, attempts)
	}
	return candidates[0].ID, nil
}

func displaySerial(serial string) string {
	if serial == "" {
		return adb.New("", "").Serial
	}
	return serial
}

func saveDeviceMap(path string, devices []detectedDevice) error {
	sortedDevices := append([]detectedDevice(nil), devices...)
	sort.Slice(sortedDevices, func(i, j int) bool {
		return sortedDevices[i].ID < sortedDevices[j].ID
	})

	content, err := json.MarshalIndent(deviceMapFile{Devices: sortedDevices}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal device map: %w", err)
	}
	content = append(content, '\n')
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create device map directory %s: %w", dir, err)
		}
	}

	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("write device map %s: %w", path, err)
	}
	return nil
}

func ensureAppiumServer(ctx context.Context, cfg commandConfig, timeline auto.Timeline, output ...io.Writer) (*appiumServerProcess, error) {
	pool, err := ensureAppiumServerPool(ctx, cfg, timeline, 1, output...)
	if err != nil || pool == nil || len(pool.Servers) == 0 {
		return nil, err
	}
	return pool.Servers[0], nil
}

func ensureAppiumServerPool(ctx context.Context, cfg commandConfig, timeline auto.Timeline, targetCount int, output ...io.Writer) (*appiumServerPool, error) {
	writer := outputWriter(output...)
	if !timelineUsesAppium(timeline) {
		return nil, nil
	}
	urls, err := appiumServerURLs(cfg, targetCount)
	if err != nil {
		return nil, err
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("no Appium server URLs resolved")
	}

	pool := &appiumServerPool{
		Servers: make([]*appiumServerProcess, 0, len(urls)),
		URLs:    urls,
	}
	for _, serverURL := range urls {
		process, err := ensureAppiumServerAtURL(ctx, cfg, serverURL, writer)
		if err != nil {
			_ = pool.Close()
			return nil, err
		}
		pool.Servers = append(pool.Servers, process)
	}
	if len(urls) > 1 {
		fmt.Fprintf(writer, "Using %d Appium server shard(s): %s\n", len(urls), strings.Join(urls, ", "))
	}
	return pool, nil
}

func appiumServerURLs(cfg commandConfig, targetCount int) ([]string, error) {
	if strings.TrimSpace(cfg.appiumURLs) != "" {
		if strings.TrimSpace(cfg.appiumURL) != "" || cfg.appiumShards > 0 {
			return nil, fmt.Errorf("use only one of -appium-urls, -appium-url, or -appium-shards")
		}
		return parseAppiumURLList(cfg.appiumURLs)
	}

	serverURL := strings.TrimSpace(cfg.appiumURL)
	if serverURL == "" {
		serverURL = defaultAppiumURL
	}
	shards := cfg.appiumShards
	if shards <= 0 {
		if cfg.deviceRunMode == deviceRunModeQueue {
			shards = 1
		} else {
			shards = autoAppiumShardCount(targetCount)
		}
	}
	return appiumShardURLs(serverURL, shards)
}

func normalizeDeviceRunMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", deviceRunModeParallel:
		return deviceRunModeParallel, nil
	case deviceRunModeQueue, "serial", "sequential":
		return deviceRunModeQueue, nil
	default:
		return "", fmt.Errorf("device run mode must be %q or %q, got %q", deviceRunModeParallel, deviceRunModeQueue, mode)
	}
}

func parseAppiumURLList(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	urls := make([]string, 0, len(parts))
	for _, part := range parts {
		serverURL := strings.TrimSpace(part)
		if serverURL == "" {
			continue
		}
		urls = append(urls, strings.TrimRight(serverURL, "/"))
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("-appium-urls did not contain any URL")
	}
	return urls, nil
}

func autoAppiumShardCount(targetCount int) int {
	if targetCount <= 1 {
		return 1
	}
	return (targetCount + defaultAppiumDevicesPerShard - 1) / defaultAppiumDevicesPerShard
}

func appiumShardURLs(baseURL string, count int) ([]string, error) {
	if count <= 0 {
		return nil, fmt.Errorf("Appium shard count must be greater than 0")
	}
	parsedURL, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("parse Appium URL %q: %w", baseURL, err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("Appium URL must use http or https, got %q", parsedURL.Scheme)
	}
	host := parsedURL.Hostname()
	if host == "" {
		return nil, fmt.Errorf("Appium URL %q is missing host", baseURL)
	}
	port := parsedURL.Port()
	if port == "" {
		if parsedURL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	basePort, err := strconv.Atoi(port)
	if err != nil {
		return nil, fmt.Errorf("Appium URL %q has invalid port %q", baseURL, port)
	}
	urls := make([]string, count)
	for i := range urls {
		shardURL := *parsedURL
		shardURL.Host = net.JoinHostPort(host, strconv.Itoa(basePort+i))
		urls[i] = strings.TrimRight(shardURL.String(), "/")
	}
	return urls, nil
}

func ensureAppiumServerAtURL(ctx context.Context, cfg commandConfig, serverURL string, writer io.Writer) (*appiumServerProcess, error) {
	if appiumStatusOK(ctx, serverURL) {
		fmt.Fprintf(writer, "Using existing Appium server at %s\n", serverURL)
		return &appiumServerProcess{URL: serverURL}, nil
	}

	commandPath := strings.TrimSpace(cfg.appiumCommand)
	if commandPath == "" {
		commandPath = "appium"
	}

	args, err := appiumServerArgs(serverURL)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, commandPath, args...)
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", commandPath, err)
	}

	process := &appiumServerProcess{
		Command: cmd,
		Done:    make(chan error, 1),
		URL:     serverURL,
	}
	go func() {
		process.Done <- cmd.Wait()
	}()

	if err := waitForAppiumStatus(ctx, serverURL, process.Done); err != nil {
		_ = process.Close()
		return nil, err
	}
	fmt.Fprintf(writer, "Started Appium server at %s\n", serverURL)
	return process, nil
}

func (p *appiumServerPool) Close() error {
	if p == nil {
		return nil
	}
	errors := make([]string, 0)
	for _, server := range p.Servers {
		if err := server.Close(); err != nil {
			errors = append(errors, err.Error())
		}
	}
	if len(errors) > 0 {
		return fmt.Errorf("%s", strings.Join(errors, "; "))
	}
	return nil
}

func timelineUsesAppium(timeline auto.Timeline) bool {
	for _, command := range timeline {
		if command.Type == auto.CommandAppium {
			return true
		}
	}
	return false
}

func appiumServerArgs(serverURL string) ([]string, error) {
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse Appium URL %q: %w", serverURL, err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("Appium URL must use http or https, got %q", parsedURL.Scheme)
	}

	host := parsedURL.Hostname()
	if host == "" {
		return nil, fmt.Errorf("Appium URL %q is missing host", serverURL)
	}
	port := parsedURL.Port()
	if port == "" {
		if parsedURL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if _, err := strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("Appium URL %q has invalid port %q", serverURL, port)
	}

	args := []string{"server", "--address", host, "--port", port}
	basePath := strings.TrimRight(parsedURL.EscapedPath(), "/")
	if basePath != "" {
		args = append(args, "--base-path", basePath)
	}
	return args, nil
}

func appiumStatusOK(ctx context.Context, serverURL string) bool {
	statusURL, err := appiumStatusURL(serverURL)
	if err != nil {
		return false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return false
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 300
}

func waitForAppiumStatus(ctx context.Context, serverURL string, done <-chan error) error {
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		if appiumStatusOK(waitCtx, serverURL) {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("wait for Appium server at %s: %w", serverURL, waitCtx.Err())
		case err := <-done:
			if err != nil {
				return fmt.Errorf("Appium server exited before becoming ready: %w", err)
			}
			return fmt.Errorf("Appium server exited before becoming ready")
		case <-ticker.C:
		}
	}
}

func appiumStatusURL(serverURL string) (string, error) {
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	parsedURL.Path = strings.TrimRight(parsedURL.Path, "/") + "/status"
	parsedURL.RawQuery = ""
	parsedURL.Fragment = ""
	return parsedURL.String(), nil
}

func (p *appiumServerProcess) Close() error {
	if p == nil || p.Command == nil || p.Command.Process == nil {
		return nil
	}
	if p.Done != nil {
		select {
		case err := <-p.Done:
			return ignoreAppiumWaitError(err)
		default:
		}
	}
	if err := p.Command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	if p.Done == nil {
		return nil
	}
	err := <-p.Done
	return ignoreAppiumWaitError(err)
}

func ignoreAppiumWaitError(err error) error {
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return err
		}
	}
	return nil
}

func runTimelineOnDevices(ctx context.Context, cfg commandConfig, timeline auto.Timeline, fallbackTimeline auto.Timeline, dataSet *auto.DataSet, targets []deviceTarget, output ...io.Writer) error {
	writer := outputWriter(output...)
	if len(targets) == 1 {
		return runTimelineOnDeviceWithFallback(ctx, cfg, timeline, fallbackTimeline, dataSet, targets[0], writer)
	}
	if cfg.deviceRunMode == deviceRunModeQueue {
		return runTimelineOnDevicesQueued(ctx, cfg, timeline, fallbackTimeline, dataSet, targets, writer)
	}

	errors := make(chan error, len(targets))
	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Go(func() {
			if err := runTimelineOnDeviceWithFallback(ctx, cfg, timeline, fallbackTimeline, dataSet, target, writer); err != nil {
				errors <- err
			}
		})
	}

	wg.Wait()
	close(errors)

	messages := make([]string, 0)
	for err := range errors {
		messages = append(messages, err.Error())
	}
	if len(messages) > 0 {
		sort.Strings(messages)
		return fmt.Errorf("%d device(s) failed:\n%s", len(messages), strings.Join(messages, "\n"))
	}

	return nil
}

func runTimelineOnDevicesQueued(ctx context.Context, cfg commandConfig, timeline auto.Timeline, fallbackTimeline auto.Timeline, dataSet *auto.DataSet, targets []deviceTarget, output io.Writer) error {
	messages := make([]string, 0)
	for _, target := range targets {
		if err := runTimelineOnDeviceWithFallback(ctx, cfg, timeline, fallbackTimeline, dataSet, target, output); err != nil {
			messages = append(messages, err.Error())
		}
	}
	if len(messages) > 0 {
		return fmt.Errorf("%d device(s) failed:\n%s", len(messages), strings.Join(messages, "\n"))
	}
	return nil
}

func runTimelineOnDeviceWithFallback(ctx context.Context, cfg commandConfig, timeline auto.Timeline, fallbackTimeline auto.Timeline, dataSet *auto.DataSet, target deviceTarget, output io.Writer) error {
	mainErr := runTimelineOnDevice(ctx, cfg, "timeline", timeline, dataSet, target, output)
	if mainErr == nil {
		return nil
	}
	if len(fallbackTimeline) == 0 {
		return mainErr
	}

	fallbackCfg := cfg
	fallbackCfg.timeLineIndex = 0
	device := adb.New(target.Serial, cfg.adbPath)
	fmt.Fprintf(output, "[device %d serial %s] Main timeline failed; running fallback timeline\n", target.DataIndex+1, device.Serial)
	if fallbackErr := runTimelineOnDevice(ctx, fallbackCfg, "fallback timeline", fallbackTimeline, dataSet, target, output); fallbackErr != nil {
		return fmt.Errorf("%w; fallback failed: %v", mainErr, fallbackErr)
	}
	return mainErr
}

func runTimelineOnDevice(ctx context.Context, cfg commandConfig, label string, timeline auto.Timeline, dataSet *auto.DataSet, target deviceTarget, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}
	output, closeOutput, err := deviceOutputWriter(output, cfg.deviceLogDir, target)
	if err != nil {
		return fmt.Errorf("device %d (%s): %w", target.DataIndex+1, displaySerial(target.Serial), err)
	}
	defer closeOutput()
	device := adb.New(target.Serial, cfg.adbPath)
	if dataSet == nil {
		dataSet = &auto.DataSet{}
	}
	variables, err := dataSet.VariablesForDevice(target.DataIndex, device.Serial)
	if err != nil {
		return fmt.Errorf("device %d (%s): %w", target.DataIndex+1, device.Serial, err)
	}
	maps.Copy(variables, auto.DeviceVariables(target.DataIndex, device.Serial))
	variables["device.port_index"] = strconv.Itoa(target.PortIndex)

	appiumURL := target.AppiumURL
	if appiumURL == "" {
		appiumURL = cfg.appiumURL
	}
	fmt.Fprintf(output, "[device %d serial %s] Starting %s with data index %d port index %d\n", target.DataIndex+1, device.Serial, label, target.DataIndex, target.PortIndex)
	runner := auto.Runner{
		Device:                     device,
		Ocr:                        ocr.NewTesseract(cfg.tesseractPath),
		AppiumServerURL:            appiumURL,
		AppiumSessionLimiter:       cfg.appiumSessionLimiter,
		AppiumSystemPortBase:       cfg.appiumSystemPortBase,
		AppiumMjpegPortBase:        cfg.appiumMjpegPortBase,
		AppiumChromedriverPortBase: cfg.appiumChromedriverPortBase,
		DataDir:                    cfg.dataDir,
		Output:                     output,
		Variables:                  variables,
	}
	if err := runner.RunFrom(ctx, timeline, cfg.timeLineIndex); err != nil {
		return fmt.Errorf("device %d (%s): %w", target.DataIndex+1, device.Serial, err)
	}
	fmt.Fprintf(output, "[device %d serial %s] Finished %s\n", target.DataIndex+1, device.Serial, label)

	return nil
}

func deviceOutputWriter(output io.Writer, logDir string, target deviceTarget) (io.Writer, func(), error) {
	logDir = strings.TrimSpace(logDir)
	if logDir == "" {
		return output, func() {}, nil
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create device log dir %s: %w", logDir, err)
	}
	path := filepath.Join(logDir, fmt.Sprintf("device-%02d-%s.log", target.DataIndex+1, safeLogFileName(displaySerial(target.Serial))))
	file, err := os.Create(path)
	if err != nil {
		return nil, nil, fmt.Errorf("create device log %s: %w", path, err)
	}
	fmt.Fprintf(output, "[device %d serial %s] Logging to %s\n", target.DataIndex+1, displaySerial(target.Serial), path)
	return io.MultiWriter(output, file), func() { _ = file.Close() }, nil
}

func safeLogFileName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "default"
	}
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}
