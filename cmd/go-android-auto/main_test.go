package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/bruxaodev/go-android-auto/pkg/auto"
	"github.com/bruxaodev/go-android-auto/pkg/ocr"
	"github.com/stretchr/testify/require"
)

func init() {
	if os.Getenv("GO_ANDROID_AUTO_FAKE_APPIUM") == "1" {
		runFakeAppiumServer()
		os.Exit(0)
	}
}

func TestResolveDeviceDataIndexesDefault(t *testing.T) {
	indexes, err := resolveDeviceDataIndexes(context.Background(), commandConfig{}, []string{"device-a", "device-b", "device-c"})
	require.NoError(t, err)
	require.Equal(t, []int{0, 1, 2}, indexes)
}

func TestParseFlagsDefaultsUseAppConfigDir(t *testing.T) {
	paths := setTestConfigHome(t)
	cfg := commandConfig{}
	parseFlags(&cfg, nil)

	require.Equal(t, paths.DeviceMapPath, cfg.deviceMapPath)
	require.Equal(t, paths.AutomationDir, cfg.automationDir)
	require.Equal(t, paths.DataDir, cfg.dataDir)
	require.DirExists(t, paths.AutomationDir)
	require.DirExists(t, paths.DataDir)
}

func TestParseFlagsDefaultsAppiumCommand(t *testing.T) {
	setTestConfigHome(t)
	cfg := commandConfig{}
	parseFlags(&cfg, nil)

	require.Equal(t, "appium", cfg.appiumCommand)
}

func TestParseFlagsDefaultsDeviceRunModeParallel(t *testing.T) {
	setTestConfigHome(t)
	cfg := commandConfig{}
	parseFlags(&cfg, nil)

	require.Equal(t, deviceRunModeParallel, cfg.deviceRunMode)
}

func TestNormalizeDeviceRunMode(t *testing.T) {
	parallel, err := normalizeDeviceRunMode("parallel")
	require.NoError(t, err)
	require.Equal(t, deviceRunModeParallel, parallel)

	queue, err := normalizeDeviceRunMode("serial")
	require.NoError(t, err)
	require.Equal(t, deviceRunModeQueue, queue)

	_, err = normalizeDeviceRunMode("bad")
	require.Error(t, err)
}

func TestParseFlagsIgnoresLocalConfigFiles(t *testing.T) {
	paths := setTestConfigHome(t)
	workDir := t.TempDir()
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workDir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(previousDir))
	})
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "automation", "nested"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "values"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".tmp"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "automation", "nested", "flow.yaml"), []byte("[]"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "values", "user.csv"), []byte("name\nDevice One\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, ".tmp", "device-map.json"), []byte(`{"devices":[]}`), 0o644))

	cfg := commandConfig{}
	parseFlags(&cfg, nil)

	require.NoFileExists(t, filepath.Join(paths.AutomationDir, "nested", "flow.yaml"))
	require.NoFileExists(t, filepath.Join(paths.DataDir, "user.csv"))
	require.NoFileExists(t, paths.DeviceMapPath)
}

func TestParseFlagsCopiesEmbeddedDefaultsToAppConfig(t *testing.T) {
	paths := setTestConfigHome(t)
	workDir := t.TempDir()
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workDir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(previousDir))
	})

	cfg := commandConfig{}
	parseFlags(&cfg, nil)

	require.FileExists(t, filepath.Join(paths.AutomationDir, "search-google.yaml"))
	require.FileExists(t, filepath.Join(paths.AutomationDir, "utils", "chrome", "search.yaml"))
	require.FileExists(t, filepath.Join(paths.DataDir, "search.csv"))
}

func TestParseFlagsDoesNotCopyDefaultsWhenConfigDirsHaveFiles(t *testing.T) {
	paths := setTestConfigHome(t)
	require.NoError(t, os.MkdirAll(paths.AutomationDir, 0o755))
	require.NoError(t, os.MkdirAll(paths.DataDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(paths.AutomationDir, "custom.yaml"), []byte("[]"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(paths.DataDir, "custom.csv"), []byte("value\ncustom\n"), 0o644))

	cfg := commandConfig{}
	parseFlags(&cfg, nil)

	require.FileExists(t, filepath.Join(paths.AutomationDir, "custom.yaml"))
	require.FileExists(t, filepath.Join(paths.DataDir, "custom.csv"))
	require.NoFileExists(t, filepath.Join(paths.AutomationDir, "search-google.yaml"))
	require.NoFileExists(t, filepath.Join(paths.DataDir, "search.csv"))
}

func TestRunCommandOnlyValidatesCSVFilesReferencedByTimeline(t *testing.T) {
	dataDir := t.TempDir()
	timelinePath := filepath.Join(t.TempDir(), "timeline.yaml")
	adbPath := createDevicesADB(t, []string{"device-b"})
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "user.csv"), []byte("username\none\ntwo\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "search.csv"), []byte("query\nfirst\n"), 0o644))
	require.NoError(t, os.WriteFile(timelinePath, []byte(`
- type: adb
  action: text
  text: '{{user.username}}'
`), 0o644))
	cfg := commandConfig{timeLinePath: timelinePath, dataDir: dataDir, adbPath: adbPath, deviceSerial: "device-b", deviceIDs: "2", deviceLogDir: ""}

	err := runCommandWithOutput(context.Background(), cfg, nil, io.Discard)

	require.NoError(t, err)
}

func TestResolveTimelinePathFallsBackToAutomationDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "automation"), 0o755))
	timelinePath := filepath.Join(dir, "automation", "gmail-create-account.yaml")
	require.NoError(t, os.WriteFile(timelinePath, []byte("[]"), 0o644))

	path, err := resolveTimelinePath(commandConfig{timeLinePath: "gmail-create-account.yaml", automationDir: filepath.Join(dir, "automation")})

	require.NoError(t, err)
	require.Equal(t, timelinePath, path)
}

func TestResolveTimelinePathUsesConfigAutomationForLegacyPrefix(t *testing.T) {
	dir := t.TempDir()
	automationDir := filepath.Join(dir, "automation")
	require.NoError(t, os.Mkdir(automationDir, 0o755))
	timelinePath := filepath.Join(automationDir, "gmail-create-account.yaml")
	require.NoError(t, os.WriteFile(timelinePath, []byte("[]"), 0o644))

	path, err := resolveTimelinePath(commandConfig{timeLinePath: filepath.Join("automation", "gmail-create-account.yaml"), automationDir: automationDir})

	require.NoError(t, err)
	require.Equal(t, timelinePath, path)
}

func TestTimelineUsesAppium(t *testing.T) {
	require.False(t, timelineUsesAppium(nil))
	require.False(t, timelineUsesAppium(auto.Timeline{{Type: auto.CommandADB}}))
	require.True(t, timelineUsesAppium(auto.Timeline{{Type: auto.CommandAppium}}))
}

func TestAppiumServerArgsUsesConfiguredURL(t *testing.T) {
	args, err := appiumServerArgs("http://127.0.0.1:4729/wd/hub")

	require.NoError(t, err)
	require.Equal(t, []string{"server", "--address", "127.0.0.1", "--port", "4729", "--base-path", "/wd/hub"}, args)
}

func TestAppiumShardURLsIncrementPorts(t *testing.T) {
	urls, err := appiumShardURLs("http://127.0.0.1:4723/wd/hub", 3)

	require.NoError(t, err)
	require.Equal(t, []string{
		"http://127.0.0.1:4723/wd/hub",
		"http://127.0.0.1:4724/wd/hub",
		"http://127.0.0.1:4725/wd/hub",
	}, urls)
}

func TestAppiumServerURLsUsesExplicitShardList(t *testing.T) {
	urls, err := appiumServerURLs(commandConfig{appiumURLs: "http://127.0.0.1:5000, http://127.0.0.1:5001/"}, 100)

	require.NoError(t, err)
	require.Equal(t, []string{"http://127.0.0.1:5000", "http://127.0.0.1:5001"}, urls)
}

func TestAppiumServerURLsAutoSizesByDeviceCount(t *testing.T) {
	urls, err := appiumServerURLs(commandConfig{}, 21)

	require.NoError(t, err)
	require.Len(t, urls, 3)
	require.Equal(t, "http://127.0.0.1:4723", urls[0])
	require.Equal(t, "http://127.0.0.1:4725", urls[2])
}

func TestAppiumServerURLsQueueModeUsesSingleAutoShard(t *testing.T) {
	urls, err := appiumServerURLs(commandConfig{deviceRunMode: deviceRunModeQueue}, 21)

	require.NoError(t, err)
	require.Equal(t, []string{"http://127.0.0.1:4723"}, urls)
}

func TestAssignAppiumTargetsRoundRobinsURLsAndPortIndexes(t *testing.T) {
	targets := assignAppiumTargets([]deviceTarget{
		{Serial: "device-a", DataIndex: 7},
		{Serial: "device-b", DataIndex: 3},
		{Serial: "device-c", DataIndex: 1},
	}, []string{"http://127.0.0.1:4723", "http://127.0.0.1:4724"})

	require.Equal(t, []deviceTarget{
		{Serial: "device-a", DataIndex: 7, PortIndex: 0, AppiumURL: "http://127.0.0.1:4723"},
		{Serial: "device-b", DataIndex: 3, PortIndex: 1, AppiumURL: "http://127.0.0.1:4724"},
		{Serial: "device-c", DataIndex: 1, PortIndex: 2, AppiumURL: "http://127.0.0.1:4723"},
	}, targets)
}

func TestDoctorReportsReadyForDefaultConfig(t *testing.T) {
	paths := setTestConfigHome(t)
	adbPath := createDevicesADB(t, []string{"device-a"})
	deviceMapPath := writeDeviceMap(t, []detectedDevice{{ID: 1, Serial: "device-a", DataIndex: 0}})
	cfg := commandConfig{}
	parseFlags(&cfg, []string{
		"-doctor",
		"-timeline", "search-google.yaml",
		"-serial", "device-a",
		"-adb", adbPath,
		"-device-map", deviceMapPath,
	})
	var output strings.Builder

	err := runCommandWithOutput(context.Background(), cfg, nil, &output)

	require.NoError(t, err)
	require.Contains(t, output.String(), "go-android-auto doctor")
	require.Contains(t, output.String(), "[OK] automation dir - "+paths.AutomationDir)
	require.Contains(t, output.String(), "[OK] timeline - loaded")
	require.Contains(t, output.String(), "[OK] adb - "+adbPath)
	require.Contains(t, output.String(), "[OK] devices - device-a")
	require.Contains(t, output.String(), "summary: 0 failed")
}

func TestDoctorFailsWhenTimelineIsMissing(t *testing.T) {
	setTestConfigHome(t)
	adbPath := createDevicesADB(t, []string{"device-a"})
	cfg := commandConfig{}
	parseFlags(&cfg, []string{
		"-doctor",
		"-timeline", "missing.yaml",
		"-serial", "device-a",
		"-adb", adbPath,
	})
	var output strings.Builder

	err := runCommandWithOutput(context.Background(), cfg, nil, &output)

	require.Error(t, err)
	require.Contains(t, output.String(), "[FAIL] timeline")
	require.Contains(t, output.String(), "summary: 1 failed")
}

func TestEnsureAppiumServerSkipsTimelinesWithoutAppium(t *testing.T) {
	process, err := ensureAppiumServer(context.Background(), commandConfig{appiumCommand: filepath.Join(t.TempDir(), "missing-appium")}, auto.Timeline{{Type: auto.CommandADB}})

	require.NoError(t, err)
	require.Nil(t, process)
}

func TestEnsureAppiumServerStartsForAppiumTimeline(t *testing.T) {
	port := freeTCPPort(t)
	appiumPath := createFakeAppiumCommand(t)

	process, err := ensureAppiumServer(context.Background(), commandConfig{
		appiumCommand: appiumPath,
		appiumURL:     "http://127.0.0.1:" + port,
	}, auto.Timeline{{Type: auto.CommandAppium, Action: auto.ActionStartSession}})

	require.NoError(t, err)
	require.NotNil(t, process)
	require.Equal(t, "http://127.0.0.1:"+port, process.URL)
	require.NoError(t, process.Close())
}

func TestResolveDeviceDataIndexesFromIDs(t *testing.T) {
	indexes, err := resolveDeviceDataIndexes(context.Background(), commandConfig{deviceIDs: "2,1"}, []string{"device-a", "device-b"})
	require.NoError(t, err)
	require.Equal(t, []int{1, 0}, indexes)
}

func TestResolveDeviceDataIndexesFromDeviceMap(t *testing.T) {
	deviceMapPath := writeDeviceMap(t, []detectedDevice{
		{ID: 1, Serial: "device-b", DataIndex: 0},
		{ID: 2, Serial: "device-a", DataIndex: 1},
	})

	indexes, err := resolveDeviceDataIndexes(context.Background(), commandConfig{deviceMapPath: deviceMapPath}, []string{"device-a", "device-b"})

	require.NoError(t, err)
	require.Equal(t, []int{1, 0}, indexes)
}

func TestResolveDeviceDataIndexesMissingDeviceMapFallsBackToSerialOrder(t *testing.T) {
	indexes, err := resolveDeviceDataIndexes(context.Background(), commandConfig{deviceMapPath: filepath.Join(t.TempDir(), "missing.json")}, []string{"device-a", "device-b"})

	require.NoError(t, err)
	require.Equal(t, []int{0, 1}, indexes)
}

func TestResolveDeviceDataIndexesDeviceMapMissingSerial(t *testing.T) {
	deviceMapPath := writeDeviceMap(t, []detectedDevice{{ID: 1, Serial: "device-a", DataIndex: 0}})

	_, err := resolveDeviceDataIndexes(context.Background(), commandConfig{deviceMapPath: deviceMapPath}, []string{"device-b"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "does not contain serial device-b")
}

func TestResolveDeviceDataIndexesInvalidCount(t *testing.T) {
	_, err := resolveDeviceDataIndexes(context.Background(), commandConfig{deviceIDs: "1"}, []string{"device-a", "device-b"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must contain 2 id(s)")
}

func TestResolveDeviceDataIndexesInvalidID(t *testing.T) {
	_, err := resolveDeviceDataIndexes(context.Background(), commandConfig{deviceIDs: "0"}, []string{"device-a"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid device id")
}

func TestResolveDeviceDataIndexesRejectsManualAndDetectedIDs(t *testing.T) {
	_, err := resolveDeviceDataIndexes(context.Background(), commandConfig{deviceIDs: "1", detectDeviceIDs: true}, []string{"device-a"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "use only one")
}

func TestParseWallpaperDeviceIDFromWordsUsesLargestNumericWord(t *testing.T) {
	id, err := parseWallpaperDeviceIDFromWordsWithMax([]ocr.Word{
		{Text: "12", Bounds: ocr.Bounds{Left: 0, Top: 0, Right: 5, Bottom: 5}},
		{Text: "BG", Bounds: ocr.Bounds{Left: 0, Top: 0, Right: 20, Bottom: 20}},
		{Text: "01", Bounds: ocr.Bounds{Left: 10, Top: 10, Right: 110, Bottom: 110}},
	}, 0)
	require.NoError(t, err)
	require.Equal(t, 1, id)
}

func TestParseWallpaperDeviceIDFromWordsRejectsMissingNumber(t *testing.T) {
	_, err := parseWallpaperDeviceIDFromWordsWithMax([]ocr.Word{{Text: "wallpaper"}}, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no numeric device id")
}

func TestParseWallpaperDeviceIDFromWordsRejectsSmallNumbers(t *testing.T) {
	_, err := parseWallpaperDeviceIDFromWordsWithMax([]ocr.Word{
		{Text: "11:27", Bounds: ocr.Bounds{Left: 0, Top: 0, Right: 120, Bottom: 30}},
		{Text: "9", Bounds: ocr.Bounds{Left: 180, Top: 30, Right: 207, Bottom: 83}},
	}, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no wallpaper-sized numeric device id")
}

func TestParseWallpaperDeviceIDFromWordsRejectsAmbiguousSameArea(t *testing.T) {
	_, err := parseWallpaperDeviceIDFromWordsWithMax([]ocr.Word{
		{Text: "01", Bounds: ocr.Bounds{Left: 0, Top: 0, Right: 100, Bottom: 100}},
		{Text: "02", Bounds: ocr.Bounds{Left: 120, Top: 0, Right: 220, Bottom: 100}},
	}, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ambiguous")
}

func TestDigitIDsFromText(t *testing.T) {
	require.Equal(t, []int{2}, validDigitIDsFromText("02\n", 0))
	require.Equal(t, []int{1, 2}, validDigitIDsFromText("01\n02\n01", 0))
	require.Empty(t, validDigitIDsFromText("abc 0", 0))
}

func TestValidDigitIDsFromTextRejectsOutOfRangeOCRNoise(t *testing.T) {
	require.Equal(t, []int{2, 1}, validDigitIDsFromText("60900 02 01", 2))
	require.Empty(t, validDigitIDsFromText("60900", 2))
}

func TestParseWallpaperDeviceIDFromWordsRejectsOutOfRangeID(t *testing.T) {
	_, err := parseWallpaperDeviceIDFromWordsWithMax([]ocr.Word{{Text: "60900", Bounds: ocr.Bounds{Left: 0, Top: 0, Right: 200, Bottom: 200}}}, 2)

	require.Error(t, err)
	require.Contains(t, err.Error(), "out-of-range")
}

func TestFilterWallpaperOCRWordsKeepsOnlyCentralWallpaperBand(t *testing.T) {
	words := []ocr.Word{
		{Text: "2", Bounds: ocr.Bounds{Left: 100, Top: 20, Right: 200, Bottom: 120}},
		{Text: "02", Bounds: ocr.Bounds{Left: 100, Top: 220, Right: 220, Bottom: 330}},
		{Text: "3", Bounds: ocr.Bounds{Left: 100, Top: 500, Right: 200, Bottom: 560}},
	}

	filtered := filterWallpaperOCRWords(words, image.Rect(0, 0, 300, 600))

	require.Equal(t, []ocr.Word{words[1]}, filtered)
}

func TestBestDigitIDFromVotesUsesConsensus(t *testing.T) {
	votes := map[int]int{}
	recordDigitVotes(votes, []int{2})
	recordDigitVotes(votes, []int{2, 1})
	recordDigitVotes(votes, []int{1})
	recordDigitVotes(votes, []int{1})

	id, err := bestDigitIDFromVotes(votes, nil)

	require.NoError(t, err)
	require.Equal(t, 1, id)
}

func TestBestDigitIDFromVotesRejectsTie(t *testing.T) {
	_, err := bestDigitIDFromVotes(map[int]int{1: 2, 2: 2}, []string{"attempt"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "ambiguous")
}

func TestDetectWallpaperDeviceIDFromImageUsesPreprocessedDigitText(t *testing.T) {
	screenshotPath := writeDigitScreenshot(t)
	tesseractPath := createDigitTextTesseract(t, "02\n")

	id, err := detectWallpaperDeviceIDFromImage(context.Background(), ocr.NewTesseract(tesseractPath), screenshotPath, 2)

	require.NoError(t, err)
	require.Equal(t, 2, id)
}

func TestWallpaperDigitCropBoundsFindsLargeBackgroundDigits(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 300, 600))
	for y := range 600 {
		for x := range 300 {
			img.Set(x, y, color.RGBA{R: 12, G: 12, B: 12, A: 255})
		}
	}
	for y := 190; y < 330; y++ {
		for x := 40; x < 130; x++ {
			img.Set(x, y, color.RGBA{R: 110, G: 110, B: 110, A: 255})
		}
		for x := 170; x < 260; x++ {
			img.Set(x, y, color.RGBA{R: 110, G: 110, B: 110, A: 255})
		}
	}
	for y := 70; y < 90; y++ {
		for x := 10; x < 40; x++ {
			img.Set(x, y, color.RGBA{R: 110, G: 110, B: 110, A: 255})
		}
	}

	crop, ok := wallpaperDigitCropBounds(img)

	require.True(t, ok)
	require.LessOrEqual(t, crop.Min.X, 40)
	require.GreaterOrEqual(t, crop.Max.X, 260)
	require.LessOrEqual(t, crop.Min.Y, 190)
	require.GreaterOrEqual(t, crop.Max.Y, 330)
	require.Greater(t, crop.Min.Y, 90)
}

func TestDetectDeviceDataIndexesUsesOCRText(t *testing.T) {
	adbPath := createScreenshotADB(t)
	tesseractPath := createTesseractTSV(t, []string{"BG 02", "BG 01"})
	deviceMapPath := filepath.Join(t.TempDir(), "device-map.json")

	indexes, err := resolveDeviceDataIndexes(context.Background(), commandConfig{
		adbPath:         adbPath,
		tesseractPath:   tesseractPath,
		detectDeviceIDs: true,
		deviceMapPath:   deviceMapPath,
	}, []string{"device-a", "device-b"})

	require.NoError(t, err)
	require.Equal(t, []int{1, 0}, indexes)

	content, err := os.ReadFile(deviceMapPath)
	require.NoError(t, err)
	var deviceMap deviceMapFile
	require.NoError(t, json.Unmarshal(content, &deviceMap))
	require.Equal(t, []detectedDevice{
		{ID: 1, Serial: "device-b", DataIndex: 0},
		{ID: 2, Serial: "device-a", DataIndex: 1},
	}, deviceMap.Devices)
}

func TestDetectDeviceIDOpensHomeBeforeScreenshot(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "adb.log")
	adbPath := createRecordingScreenshotADB(t, logPath)
	tesseractPath := createTesseractTSV(t, []string{"BG 01"})

	id, err := detectDeviceID(context.Background(), commandConfig{adbPath: adbPath, tesseractPath: tesseractPath}, "device-a", 1)

	require.NoError(t, err)
	require.Equal(t, 1, id)

	content, err := os.ReadFile(logPath)
	require.NoError(t, err)
	logContent := string(content)
	wakeupCommand := "-s device-a shell input keyevent KEYCODE_WAKEUP"
	dismissCommand := "-s device-a shell wm dismiss-keyguard"
	homeCommand := "-s device-a shell input keyevent KEYCODE_HOME"
	pullCommand := "-s device-a pull"
	require.Contains(t, logContent, wakeupCommand)
	require.Contains(t, logContent, dismissCommand)
	require.Contains(t, logContent, homeCommand)
	require.Contains(t, logContent, pullCommand)
	require.Less(t, strings.Index(logContent, wakeupCommand), strings.Index(logContent, dismissCommand))
	require.Less(t, strings.Index(logContent, dismissCommand), strings.Index(logContent, homeCommand))
	require.Less(t, strings.Index(logContent, homeCommand), strings.Index(logContent, pullCommand))
}

func TestRunDeviceSetupDetectsAndSavesDeviceMapWithoutTimeline(t *testing.T) {
	adbPath := createScreenshotADB(t)
	tesseractPath := createTesseractTSV(t, []string{"BG 02", "BG 01"})
	deviceMapPath := filepath.Join(t.TempDir(), "device-map.json")

	err := runDeviceSetup(context.Background(), commandConfig{
		adbPath:         adbPath,
		tesseractPath:   tesseractPath,
		deviceSerials:   "device-a,device-b",
		deviceMapPath:   deviceMapPath,
		detectDeviceIDs: false,
	})

	require.NoError(t, err)
	content, err := os.ReadFile(deviceMapPath)
	require.NoError(t, err)
	var deviceMap deviceMapFile
	require.NoError(t, json.Unmarshal(content, &deviceMap))
	require.Equal(t, []detectedDevice{
		{ID: 1, Serial: "device-b", DataIndex: 0},
		{ID: 2, Serial: "device-a", DataIndex: 1},
	}, deviceMap.Devices)
}

func TestRunDeviceSetupRequiresDeviceMapPath(t *testing.T) {
	err := runDeviceSetup(context.Background(), commandConfig{deviceMapPath: ""})

	require.Error(t, err)
	require.Contains(t, err.Error(), "-device-map is required")
}

func TestRunDeviceSetupRejectsManualDeviceIDs(t *testing.T) {
	err := runDeviceSetup(context.Background(), commandConfig{deviceMapPath: "device-map.json", deviceIDs: "1"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "without -device-ids")
}

func TestRunDeviceSetupRemovesStaleDeviceMapBeforeDetection(t *testing.T) {
	deviceMapPath := writeDeviceMap(t, []detectedDevice{{ID: 99, Serial: "stale", DataIndex: 98}})

	err := runDeviceSetup(context.Background(), commandConfig{
		adbPath:       createScreenshotADB(t),
		tesseractPath: createTesseractTSV(t, []string{""}),
		deviceSerials: "device-a",
		deviceMapPath: deviceMapPath,
	})

	require.Error(t, err)
	require.NoFileExists(t, deviceMapPath)
}

func TestSaveDeviceMapCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".tmp", "device-map.json")

	err := saveDeviceMap(path, []detectedDevice{{ID: 1, Serial: "device-a", DataIndex: 0}})

	require.NoError(t, err)
	require.FileExists(t, path)
}

func TestRunTimelineOnDevicesContinuesAndRunsFallbackOnFailedDevice(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "adb.log")
	adbPath := createFailingTimelineADB(t, logPath)
	cfg := commandConfig{adbPath: adbPath, deviceLogDir: ""}
	timeline := auto.Timeline{
		{Type: auto.CommandADB, Action: auto.ActionShell, Args: []string{"ok-main"}},
		{Type: auto.CommandADB, Action: auto.ActionShell, Args: []string{"fail-main"}},
		{Type: auto.CommandADB, Action: auto.ActionShell, Args: []string{"after-main"}},
	}
	fallbackTimeline := auto.Timeline{
		{Type: auto.CommandADB, Action: auto.ActionShell, Args: []string{"reset-display"}},
	}
	targets := []deviceTarget{
		{Serial: "device-a", DataIndex: 0},
		{Serial: "device-b", DataIndex: 1},
	}

	err := runTimelineOnDevices(context.Background(), cfg, timeline, fallbackTimeline, nil, targets)

	require.Error(t, err)
	require.Contains(t, err.Error(), "1 device(s) failed")
	require.Contains(t, err.Error(), "device 1 (device-a)")

	content, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	logContent := string(content)
	require.Contains(t, logContent, "-s device-a shell ok-main")
	require.Contains(t, logContent, "-s device-a shell fail-main")
	require.Contains(t, logContent, "-s device-a shell reset-display")
	require.Contains(t, logContent, "-s device-b shell ok-main")
	require.Contains(t, logContent, "-s device-b shell fail-main")
	require.Contains(t, logContent, "-s device-b shell after-main")
	require.NotContains(t, logContent, "-s device-b shell reset-display")
}

func TestRunTimelineOnDevicesQueueRunsOneDeviceAtATime(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "adb.log")
	adbPath := createSlowTimelineADB(t, logPath)
	cfg := commandConfig{adbPath: adbPath, deviceLogDir: "", deviceRunMode: deviceRunModeQueue}
	timeline := auto.Timeline{{Type: auto.CommandADB, Action: auto.ActionShell, Args: []string{"slow-main"}}}
	targets := []deviceTarget{
		{Serial: "device-a", DataIndex: 0},
		{Serial: "device-b", DataIndex: 1},
	}

	err := runTimelineOnDevices(context.Background(), cfg, timeline, nil, nil, targets)

	require.NoError(t, err)
	content, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	require.Equal(t, []string{"device-a start", "device-a end", "device-b start", "device-b end"}, strings.FieldsFunc(strings.TrimSpace(string(content)), func(r rune) bool { return r == '\n' }))
}

func TestRunTimelineOnDevicesWritesPerDeviceLogs(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	adbPath := createFailingTimelineADB(t, filepath.Join(t.TempDir(), "adb.log"))
	cfg := commandConfig{adbPath: adbPath, deviceLogDir: logDir}
	timeline := auto.Timeline{{Type: auto.CommandADB, Action: auto.ActionShell, Args: []string{"ok-main"}}}
	targets := []deviceTarget{
		{Serial: "device/a", DataIndex: 0, PortIndex: 0},
		{Serial: "device-b", DataIndex: 1, PortIndex: 1},
	}

	err := runTimelineOnDevices(context.Background(), cfg, timeline, nil, nil, targets)

	require.NoError(t, err)
	firstLog := filepath.Join(logDir, "device-01-device_a.log")
	secondLog := filepath.Join(logDir, "device-02-device-b.log")
	require.FileExists(t, firstLog)
	require.FileExists(t, secondLog)
	content, err := os.ReadFile(firstLog)
	require.NoError(t, err)
	require.Contains(t, string(content), "Starting timeline with data index 0 port index 0")
}

func TestLoadFallbackTimelineUsesEmbeddedPath(t *testing.T) {
	dir := t.TempDir()
	fallbackPath := filepath.Join(dir, "reset-display.yaml")
	require.NoError(t, os.WriteFile(fallbackPath, []byte(`
- name: reset display
  type: adb
  action: reset-size
`), 0o644))

	fallbackTimeline, err := loadFallbackTimeline(commandConfig{}, fallbackPath)

	require.NoError(t, err)
	require.Len(t, fallbackTimeline, 1)
	require.Equal(t, "reset display", fallbackTimeline[0].Name)
}

func TestLoadFallbackTimelineFlagOverridesEmbeddedPath(t *testing.T) {
	dir := t.TempDir()
	embeddedPath := filepath.Join(dir, "embedded.yaml")
	flagPath := filepath.Join(dir, "flag.yaml")
	require.NoError(t, os.WriteFile(embeddedPath, []byte(`
- name: embedded
  type: adb
  action: reset-size
`), 0o644))
	require.NoError(t, os.WriteFile(flagPath, []byte(`
- name: flag
  type: adb
  action: reset-size
`), 0o644))

	fallbackTimeline, err := loadFallbackTimeline(commandConfig{fallbackPath: flagPath}, embeddedPath)

	require.NoError(t, err)
	require.Len(t, fallbackTimeline, 1)
	require.Equal(t, "flag", fallbackTimeline[0].Name)
}

func writeDeviceMap(t *testing.T, devices []detectedDevice) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "device-map.json")
	content, err := json.Marshal(deviceMapFile{Devices: devices})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, content, 0o644))
	return path
}

func setTestConfigHome(t *testing.T) appConfigPaths {
	t.Helper()

	baseDir := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("AppData", baseDir)
		t.Setenv("APPDATA", baseDir)
	case "darwin":
		t.Setenv("HOME", baseDir)
	default:
		t.Setenv("XDG_CONFIG_HOME", baseDir)
	}
	paths, err := defaultAppConfigPaths()
	require.NoError(t, err)
	return paths
}

func writeDigitScreenshot(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "screenshot.png")
	img := image.NewRGBA(image.Rect(0, 0, 300, 600))
	for y := range 600 {
		for x := range 300 {
			img.Set(x, y, color.RGBA{R: 20, G: 20, B: 20, A: 255})
		}
	}
	for y := 150; y < 360; y++ {
		for x := 80; x < 220; x++ {
			if x < 120 || x > 180 || y < 190 || y > 320 {
				img.Set(x, y, color.RGBA{R: 230, G: 230, B: 230, A: 255})
			}
		}
	}

	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, png.Encode(file, img))
	require.NoError(t, file.Close())
	return path
}

func createScreenshotADB(t *testing.T) string {
	t.Helper()

	adbPath := filepath.Join(t.TempDir(), "adb")
	script := `#!/bin/sh
if [ "$3" = "pull" ]; then
  : > "$5"
fi
`
	require.NoError(t, os.WriteFile(adbPath, []byte(script), 0o755))
	return adbPath
}

func createDevicesADB(t *testing.T, serials []string) string {
	t.Helper()

	adbPath := filepath.Join(t.TempDir(), "adb")
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	script.WriteString("if [ \"$1\" = \"devices\" ]; then\n")
	script.WriteString("  printf 'List of devices attached\\n'\n")
	for _, serial := range serials {
		script.WriteString("  printf '%s\\tdevice\\n' " + strconv.Quote(serial) + "\n")
	}
	script.WriteString("  exit 0\n")
	script.WriteString("fi\n")
	script.WriteString("exit 0\n")
	require.NoError(t, os.WriteFile(adbPath, []byte(script.String()), 0o755))
	return adbPath
}

func createRecordingScreenshotADB(t *testing.T, logPath string) string {
	t.Helper()

	adbPath := filepath.Join(t.TempDir(), "adb")
	script := `#!/bin/sh
printf '%s\n' "$*" >> ` + strconv.Quote(logPath) + `
if [ "$3" = "pull" ]; then
  : > "$5"
fi
`
	require.NoError(t, os.WriteFile(adbPath, []byte(script), 0o755))
	return adbPath
}

func createFailingTimelineADB(t *testing.T, logPath string) string {
	t.Helper()

	adbPath := filepath.Join(t.TempDir(), "adb")
	script := `#!/bin/sh
printf '%s\n' "$*" >> ` + strconv.Quote(logPath) + `
case "$*" in
  *"-s device-a shell fail-main"*) exit 7 ;;
esac
`
	require.NoError(t, os.WriteFile(adbPath, []byte(script), 0o755))
	return adbPath
}

func createSlowTimelineADB(t *testing.T, logPath string) string {
	t.Helper()

	adbPath := filepath.Join(t.TempDir(), "adb")
	script := `#!/bin/sh
serial=""
if [ "$1" = "-s" ]; then serial="$2"; fi
printf '%s start\n' "$serial" >> ` + strconv.Quote(logPath) + `
sleep 0.05
printf '%s end\n' "$serial" >> ` + strconv.Quote(logPath) + `
`
	require.NoError(t, os.WriteFile(adbPath, []byte(script), 0o755))
	return adbPath
}

func createTesseractTSV(t *testing.T, texts []string) string {
	t.Helper()

	dir := t.TempDir()
	tesseractPath := filepath.Join(dir, "tesseract")
	countPath := filepath.Join(dir, "count")
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	script.WriteString("count=0\n")
	script.WriteString("if [ -f " + strconv.Quote(countPath) + " ]; then count=$(cat " + strconv.Quote(countPath) + "); fi\n")
	script.WriteString("count=$((count + 1))\n")
	script.WriteString("printf '%s' \"$count\" > " + strconv.Quote(countPath) + "\n")
	script.WriteString("case \"$count\" in\n")
	for i, text := range texts {
		script.WriteString(strconv.Itoa(i+1) + ") text=" + strconv.Quote(text) + " ;;\n")
	}
	script.WriteString("*) text='' ;;\n")
	script.WriteString("esac\n")
	script.WriteString("printf 'level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\\n'\n")
	script.WriteString("word_num=0\n")
	script.WriteString("for word in $text; do word_num=$((word_num + 1)); printf '5\t1\t1\t1\t1\t%s\t0\t0\t100\t100\t96\t%s\\n' \"$word_num\" \"$word\"; done\n")

	require.NoError(t, os.WriteFile(tesseractPath, []byte(script.String()), 0o755))
	return tesseractPath
}

func createDigitTextTesseract(t *testing.T, text string) string {
	t.Helper()

	tesseractPath := filepath.Join(t.TempDir(), "tesseract")
	script := `#!/bin/sh
printf %s ` + strconv.Quote(text) + `
`
	require.NoError(t, os.WriteFile(tesseractPath, []byte(script), 0o755))
	return tesseractPath
}

func freeTCPPort(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	return port
}

func createFakeAppiumCommand(t *testing.T) string {
	t.Helper()

	executable, err := os.Executable()
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "appium")
	script := "#!/bin/sh\nGO_ANDROID_AUTO_FAKE_APPIUM=1 exec " + strconv.Quote(executable) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func runFakeAppiumServer() {
	address := "127.0.0.1:4723"
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--address":
			if i+1 < len(args) {
				address = args[i+1] + ":" + strings.Split(address, ":")[1]
				i++
			}
		case "--port":
			if i+1 < len(args) {
				host := strings.Split(address, ":")[0]
				address = host + ":" + args[i+1]
				i++
			}
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"value":{"ready":true}}`))
	})
	server := &http.Server{Addr: address, Handler: mux}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		_, _ = fmt.Fprintf(os.Stderr, "fake appium failed: %v\n", err)
		os.Exit(1)
	}
}
