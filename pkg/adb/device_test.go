package adb

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDevice(t *testing.T) {
	type deviceTest struct {
		name       string
		serial     string
		adbPath    string
		stdout     string
		stderr     string
		wantErr    bool
		wantStdout string
		wantStderr string
	}

	tests := []deviceTest{
		{
			name:       "success",
			serial:     "127.0.0.1:5555",
			stdout:     "connected to 127.0.0.1:5555\n",
			wantStdout: "connected to 127.0.0.1:5555\n",
		},
		{
			name:       "adb textual failure",
			serial:     "invalid-serial",
			stderr:     "failed to resolve host: 'invalid-serial': Temporary failure in name resolution\n",
			wantErr:    true,
			wantStderr: "failed to resolve host: 'invalid-serial': Temporary failure in name resolution\n",
		},
		{
			name:    "command execution failure",
			serial:  "127.0.0.1:5555",
			adbPath: filepath.Join("/tmp", "adb-does-not-exist"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adbPath := tt.adbPath
			if adbPath == "" {
				adbPath = createFakeADB(t, tt.stdout, tt.stderr)
			}

			device := New(tt.serial, adbPath)
			result, err := device.Connect(t.Context())
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tt.wantStdout, result.Stdout)
			require.Equal(t, tt.wantStderr, result.Stderr)
		})
	}
}

func TestDeviceShell(t *testing.T) {
	adbPath := createFakeADB(t, "Hello, World!\n", "")
	device := New("localhost:5555", adbPath)
	result, err := device.Shell(t.Context(), "echo", "Hello, World!")
	require.NoError(t, err)
	require.Equal(t, "Hello, World!\n", result.Stdout)
	require.Equal(t, "", result.Stderr)
}

func TestDeviceTextEscapesSpaces(t *testing.T) {
	adbPath, argsPath := createRecordingADB(t)
	device := New("localhost:5555", adbPath)

	_, err := device.Text(t.Context(), "Santana da Silva")
	require.NoError(t, err)

	args, err := os.ReadFile(argsPath)
	require.NoError(t, err)
	require.Equal(t, "-s localhost:5555 shell input text Santana%sda%sSilva\n", string(args))
}

func TestDeviceTextNormalizesAccents(t *testing.T) {
	adbPath, argsPath := createRecordingADB(t)
	device := New("localhost:5555", adbPath)

	_, err := device.Text(t.Context(), "João da Silva")
	require.NoError(t, err)

	args, err := os.ReadFile(argsPath)
	require.NoError(t, err)
	require.Equal(t, "-s localhost:5555 shell input text Joao%sda%sSilva\n", string(args))
}

func TestDeviceScreenshotPullsWhenScreencapReturnsError(t *testing.T) {
	adbPath := createScreencapErrorADB(t)
	device := New("localhost:5555", adbPath)
	localPath := filepath.Join(t.TempDir(), "screen.png")

	_, err := device.Screenshot(t.Context(), localPath)

	require.NoError(t, err)
	content, err := os.ReadFile(localPath)
	require.NoError(t, err)
	require.Equal(t, "screenshot", string(content))
}

func createFakeADB(t *testing.T, stdout, stderr string) string {
	t.Helper()

	adbPath := filepath.Join(t.TempDir(), "adb")
	script := "#!/bin/sh\n"
	if stdout != "" {
		script += "cat <<'EOF'\n" + stdout + "EOF\n"
	}
	if stderr != "" {
		script += "cat <<'EOF' >&2\n" + stderr + "EOF\n"
	}

	if err := os.WriteFile(adbPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake adb script: %v", err)
	}

	return adbPath
}

func createRecordingADB(t *testing.T) (string, string) {
	t.Helper()

	dir := t.TempDir()
	adbPath := filepath.Join(dir, "adb")
	argsPath := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(argsPath) + "\n"

	if err := os.WriteFile(adbPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake adb script: %v", err)
	}

	return adbPath, argsPath
}

func createScreencapErrorADB(t *testing.T) string {
	t.Helper()

	adbPath := filepath.Join(t.TempDir(), "adb")
	script := `#!/bin/sh
if [ "$3" = "shell" ] && [ "$4" = "screencap" ]; then
  exit 1
fi
if [ "$3" = "pull" ]; then
  printf screenshot > "$5"
fi
`

	if err := os.WriteFile(adbPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake adb script: %v", err)
	}

	return adbPath
}
