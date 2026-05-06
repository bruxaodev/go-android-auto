package auto

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadDataDirVariablesFor(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "names.csv"), []byte("nome\nJoão Santana da Silva\nYoshiharu Kohayakawa\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "proxy.csv"), []byte("proxy\n130.180.253.101:9102:user1:pass1\n199.115.178.30:6814:user2:pass2\n"), 0o644))

	dataSet, err := LoadDataDir(dataDir)
	require.NoError(t, err)

	variables, err := dataSet.VariablesFor(1)
	require.NoError(t, err)
	require.Equal(t, "Yoshiharu Kohayakawa", variables["names.nome"])
	require.Equal(t, "Yoshiharu", variables["names.nome.first"])
	require.Equal(t, "Kohayakawa", variables["names.nome.last"])
	require.Equal(t, "199.115.178.30:6814:user2:pass2", variables["proxy"])
	require.Equal(t, "199.115.178.30:6814", variables["proxy.address"])
	require.Equal(t, "user2", variables["proxy.user"])
	require.Equal(t, "pass2", variables["proxy.password"])
}

func TestLoadDataDirVariablesForMissingRow(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "proxy.csv"), []byte("proxy\n130.180.253.101:9102:user1:pass1\n199.115.178.30:6814:user2:pass2\n"), 0o644))

	dataSet, err := LoadDataDir(dataDir)
	require.NoError(t, err)

	_, err = dataSet.VariablesFor(2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing row for device index 2")
}

func TestLoadDataDirVariablesForUsesSingleRowCSVForEveryDevice(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "password.csv"), []byte("password\nshared-secret\n"), 0o644))

	dataSet, err := LoadDataDir(dataDir)
	require.NoError(t, err)

	firstDeviceVariables, err := dataSet.VariablesForDevice(0, "emulator-5554")
	require.NoError(t, err)
	secondDeviceVariables, err := dataSet.VariablesForDevice(1, "emulator-5556")
	require.NoError(t, err)
	require.Equal(t, "shared-secret", firstDeviceVariables["password"])
	require.Equal(t, "shared-secret", secondDeviceVariables["password"])
}

func TestLoadDataDirVariablesForDeviceUsesDeviceScopedCSV(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "gmail.csv"), []byte("id,device,value\n2,emulator-5556,two@gmail.com\n1,emulator-5554,one@gmail.com\n"), 0o644))

	dataSet, err := LoadDataDir(dataDir)
	require.NoError(t, err)

	variables, err := dataSet.VariablesForDevice(0, "emulator-5554")
	require.NoError(t, err)
	require.Equal(t, "one@gmail.com", variables["gmail"])
	require.Equal(t, "one@gmail.com", variables["gmail.value"])
	require.Empty(t, variables["gmail.id"])
	require.Empty(t, variables["gmail.device"])
}

func TestLoadDataDirVariablesForDeviceSkipsMissingDeviceScopedCSVRow(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "user.csv"), []byte("name\nDevice One\nDevice Two\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "gmail.csv"), []byte("id,device,value\n1,emulator-5554,one@gmail.com\n"), 0o644))

	dataSet, err := LoadDataDir(dataDir)
	require.NoError(t, err)

	variables, err := dataSet.VariablesForDevice(1, "emulator-5556")
	require.NoError(t, err)
	require.Equal(t, "Device Two", variables["user"])
	require.Empty(t, variables["gmail"])
	require.Empty(t, variables["gmail.value"])
}

func TestLoadDataDirCreatesMissingDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "values")

	dataSet, err := LoadDataDir(dataDir)

	require.NoError(t, err)
	require.Empty(t, dataSet.Files)
	require.DirExists(t, dataDir)
}

func TestSaveDeviceValueCSVUpsertsByDevice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gmail.csv")

	require.NoError(t, SaveDeviceValueCSV(path, 2, "emulator-5556", "first@gmail.com"))
	require.NoError(t, SaveDeviceValueCSV(path, 2, "emulator-5556", "second@gmail.com"))

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "id,device,value\n2,emulator-5556,second@gmail.com\n", string(content))
}

func TestDeviceVariables(t *testing.T) {
	variables := DeviceVariables(0, "emulator-5554")
	require.Equal(t, "0", variables["device.index"])
	require.Equal(t, "1", variables["device.id"])
	require.Equal(t, "emulator-5554", variables["device.serial"])
}
