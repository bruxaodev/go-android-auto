package adb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const defaultSerial = "emulator-5554"

type Device struct {
	Serial string
	ADB    string
}

type CommandResult struct {
	Stdout string
	Stderr string
}

func New(serial, adbPath string) *Device {

	if serial == "" {
		serial = defaultSerial
	}

	if adbPath == "" {
		adbPath = "adb"
	}

	return &Device{
		Serial: serial,
		ADB:    adbPath,
	}
}

func (d Device) Devices(ctx context.Context) ([]string, error) {
	result, err := d.adb(ctx, "devices")
	if err != nil {
		return nil, commandError("list devices", d.Serial, nil, result, err)
	}

	lines := strings.Split(result.Stdout, "\n")
	var devices []string
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "device" {
			devices = append(devices, fields[0])
		}
	}

	return devices, nil
}

func (d *Device) adb(ctx context.Context, args ...string) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, d.ADB, args...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := CommandResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		return result, err
	}

	if err := isResultError(result); err != nil {
		return result, err
	}

	return result, err
}

func (d *Device) Connect(ctx context.Context) (CommandResult, error) {
	result, err := d.adb(ctx, "connect", d.Serial)
	if err != nil {
		return result, commandError("connect", d.Serial, nil, result, err)
	}

	return result, nil
}

func (d *Device) Shell(ctx context.Context, args ...string) (CommandResult, error) {
	cmdArgs := append([]string{"-s", d.Serial, "shell"}, args...)
	result, err := d.adb(ctx, cmdArgs...)
	if err != nil {
		return result, commandError("shell", d.Serial, args, result, err)
	}

	return result, nil
}

func (d *Device) Tap(ctx context.Context, x, y int) (CommandResult, error) {
	result, err := d.Shell(ctx, "input", "tap", strconv.Itoa(x), strconv.Itoa(y))
	if err != nil {
		return result, commandError("tap", d.Serial, []string{strconv.Itoa(x), strconv.Itoa(y)}, result, err)
	}

	return result, nil
}

func (d *Device) Swipe(ctx context.Context, x1, y1, x2, y2 int) (CommandResult, error) {
	result, err := d.Shell(ctx, "input", "swipe", strconv.Itoa(x1), strconv.Itoa(y1), strconv.Itoa(x2), strconv.Itoa(y2))
	if err != nil {
		return result, commandError("swipe", d.Serial, []string{strconv.Itoa(x1), strconv.Itoa(y1), strconv.Itoa(x2), strconv.Itoa(y2)}, result, err)
	}

	return result, nil
}

func (d *Device) KeyEvent(ctx context.Context, keyCode string) (CommandResult, error) {
	result, err := d.Shell(ctx, "input", "keyevent", keyCode)
	if err != nil {
		return result, commandError("keyevent", d.Serial, []string{keyCode}, result, err)
	}

	return result, nil
}

func (d *Device) Text(ctx context.Context, text string) (CommandResult, error) {
	text = shellInputText(text)
	result, err := d.Shell(ctx, "input", "text", text)
	if err != nil {
		return result, commandError("text", d.Serial, []string{text}, result, err)
	}

	return result, nil
}

func shellInputText(text string) string {
	var builder strings.Builder
	for _, r := range text {
		switch r {
		case ' ':
			builder.WriteString("%s")
		case 'á', 'à', 'â', 'ã', 'ä':
			builder.WriteByte('a')
		case 'Á', 'À', 'Â', 'Ã', 'Ä':
			builder.WriteByte('A')
		case 'é', 'è', 'ê', 'ë':
			builder.WriteByte('e')
		case 'É', 'È', 'Ê', 'Ë':
			builder.WriteByte('E')
		case 'í', 'ì', 'î', 'ï':
			builder.WriteByte('i')
		case 'Í', 'Ì', 'Î', 'Ï':
			builder.WriteByte('I')
		case 'ó', 'ò', 'ô', 'õ', 'ö':
			builder.WriteByte('o')
		case 'Ó', 'Ò', 'Ô', 'Õ', 'Ö':
			builder.WriteByte('O')
		case 'ú', 'ù', 'û', 'ü':
			builder.WriteByte('u')
		case 'Ú', 'Ù', 'Û', 'Ü':
			builder.WriteByte('U')
		case 'ç':
			builder.WriteByte('c')
		case 'Ç':
			builder.WriteByte('C')
		case 'ñ':
			builder.WriteByte('n')
		case 'Ñ':
			builder.WriteByte('N')
		default:
			if r >= 0 && r < 128 {
				builder.WriteRune(r)
			}
		}
	}
	return builder.String()
}

func (d *Device) LongPress(ctx context.Context, x, y, delay int) (CommandResult, error) {
	result, err := d.Shell(ctx, "input", "swipe", strconv.Itoa(x), strconv.Itoa(y), strconv.Itoa(x), strconv.Itoa(y), strconv.Itoa(delay))
	if err != nil {
		return result, commandError("longpress", d.Serial, []string{strconv.Itoa(x), strconv.Itoa(y)}, result, err)
	}

	return result, nil
}

func (d *Device) Pull(ctx context.Context, remotepath, localpath string) (CommandResult, error) {
	args := []string{"-s", d.Serial, "pull", remotepath, localpath}
	result, err := d.adb(ctx, args...)
	if err != nil {
		return result, commandError("pull", d.Serial, []string{remotepath, localpath}, result, err)
	}

	return result, nil
}

func (d *Device) RemoveForward(ctx context.Context, localPort int) (CommandResult, error) {
	if localPort <= 0 {
		return CommandResult{}, fmt.Errorf("local port must be greater than 0")
	}

	target := "tcp:" + strconv.Itoa(localPort)
	result, err := d.adb(ctx, "-s", d.Serial, "forward", "--remove", target)
	if err != nil {
		if isForwardNotFound(result) {
			return result, nil
		}
		return result, commandError("remove forward", d.Serial, []string{target}, result, err)
	}

	return result, nil
}

func (d *Device) Screenshot(ctx context.Context, localpath string) (CommandResult, error) {
	if result, err := d.execOutScreenshot(ctx, localpath); err == nil {
		return result, nil
	}

	remotePath := fmt.Sprintf("/data/local/tmp/screenshot_%d.png", time.Now().Unix())
	screenshotResult, screenshotErr := d.Shell(ctx, "screencap", "-p", remotePath)

	result, err := d.Pull(ctx, remotePath, localpath)
	if err != nil {
		if screenshotErr != nil {
			return screenshotResult, commandError("screenshot", d.Serial, nil, screenshotResult, screenshotErr)
		}
		return result, commandError("screenshot", d.Serial, nil, result, err)
	}

	return result, nil
}

func (d *Device) execOutScreenshot(ctx context.Context, localpath string) (CommandResult, error) {
	file, err := os.Create(localpath)
	if err != nil {
		return CommandResult{}, err
	}

	cmd := exec.CommandContext(ctx, d.ADB, "-s", d.Serial, "exec-out", "screencap", "-p")
	var stderr bytes.Buffer
	cmd.Stdout = file
	cmd.Stderr = &stderr
	cmdErr := cmd.Run()
	closeErr := file.Close()
	result := CommandResult{Stderr: stderr.String()}
	if cmdErr != nil {
		return result, cmdErr
	}
	if closeErr != nil {
		return result, closeErr
	}

	info, err := os.Stat(localpath)
	if err != nil {
		return result, err
	}
	if info.Size() == 0 {
		return result, fmt.Errorf("empty screenshot")
	}

	return result, nil
}

func isResultError(result CommandResult) error {
	combined := strings.TrimSpace(strings.Join([]string{result.Stdout, result.Stderr}, "\n"))
	if combined == "" {
		return nil
	}

	lower := strings.ToLower(combined)
	if strings.Contains(lower, "failed to") || strings.Contains(lower, "cannot resolve") {
		return errors.New(combined)
	}

	return nil
}

func isForwardNotFound(result CommandResult) bool {
	combined := strings.ToLower(strings.TrimSpace(strings.Join([]string{result.Stdout, result.Stderr}, "\n")))
	return strings.Contains(combined, "not found")
}
