package auto

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

type DataSet struct {
	Files []DataFile
}

type DataFile struct {
	Name    string
	Path    string
	Headers []string
	Rows    []map[string]string
}

const (
	deviceIDHeader = "id"
	deviceHeader   = "device"
	valueHeader    = "value"
)

var deviceValueCSVMu sync.Mutex

func LoadDataDir(path string) (*DataSet, error) {
	if path == "" {
		return &DataSet{}, nil
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create data dir %s: %w", path, err)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &DataSet{}, nil
		}
		return nil, fmt.Errorf("failed to read data dir %s: %w", path, err)
	}

	files := make([]DataFile, 0)
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".csv" {
			continue
		}

		dataFile, err := loadCSVDataFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return nil, err
		}
		files = append(files, dataFile)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})

	return &DataSet{Files: files}, nil
}

func loadCSVDataFile(path string) (DataFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return DataFile{}, fmt.Errorf("failed to open data file %s: %w", path, err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.TrimLeadingSpace = true

	headers, err := reader.Read()
	if err != nil {
		return DataFile{}, fmt.Errorf("failed to read header from data file %s: %w", path, err)
	}

	for i := range headers {
		headers[i] = normalizeVariableKey(headers[i])
		if headers[i] == "" {
			return DataFile{}, fmt.Errorf("data file %s has an empty header at column %d", path, i+1)
		}
	}

	rows := make([]map[string]string, 0)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return DataFile{}, fmt.Errorf("failed to read row from data file %s: %w", path, err)
		}

		row := make(map[string]string, len(headers))
		for i, header := range headers {
			if i >= len(record) {
				row[header] = ""
				continue
			}
			row[header] = strings.TrimSpace(record[i])
		}
		rows = append(rows, row)
	}

	name := normalizeVariableKey(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	if name == "" {
		return DataFile{}, fmt.Errorf("data file %s has an empty name", path)
	}

	return DataFile{
		Name:    name,
		Path:    path,
		Headers: headers,
		Rows:    rows,
	}, nil
}

func (d *DataSet) VariablesFor(index int) (map[string]string, error) {
	return d.VariablesForDevice(index, "")
}

func (d *DataSet) VariablesForDevice(index int, serial string) (map[string]string, error) {
	if index < 0 {
		return nil, fmt.Errorf("invalid data index %d", index)
	}
	if d == nil {
		return map[string]string{}, nil
	}

	variables := make(map[string]string)
	for _, file := range d.Files {
		row, err := file.rowForDevice(index, serial)
		if err != nil {
			return nil, err
		}
		if row == nil {
			if file.isDeviceScoped() {
				continue
			}
			return nil, fmt.Errorf("data file %s has %d rows, missing row for device index %d", file.Path, len(file.Rows), index)
		}

		for _, header := range file.Headers {
			if file.isDeviceScoped() && (header == deviceIDHeader || header == deviceHeader) {
				continue
			}
			value := row[header]
			baseKey := file.Name + "." + header
			setVariable(variables, baseKey, value)
			setDerivedVariables(variables, baseKey, value)

			if file.singleValueHeader(header) {
				setVariable(variables, file.Name, value)
				setDerivedVariables(variables, file.Name, value)
			}
		}
	}

	return variables, nil
}

func (f DataFile) rowForDevice(index int, serial string) (map[string]string, error) {
	if !f.isDeviceScoped() {
		if len(f.Rows) == 1 {
			return f.Rows[0], nil
		}
		if index >= len(f.Rows) {
			return nil, nil
		}
		return f.Rows[index], nil
	}

	deviceID := strconv.Itoa(index + 1)
	serial = strings.TrimSpace(serial)
	var idMatch map[string]string
	for _, row := range f.Rows {
		if serial != "" && strings.TrimSpace(row[deviceHeader]) == serial {
			return row, nil
		}
		if strings.TrimSpace(row[deviceIDHeader]) == deviceID {
			idMatch = row
		}
	}
	if idMatch != nil {
		return idMatch, nil
	}
	return nil, nil
}

func (f DataFile) isDeviceScoped() bool {
	return containsString(f.Headers, deviceIDHeader) || containsString(f.Headers, deviceHeader)
}

func (f DataFile) singleValueHeader(header string) bool {
	if len(f.Headers) == 1 {
		return true
	}
	if !f.isDeviceScoped() || header != valueHeader {
		return false
	}
	for _, candidate := range f.Headers {
		if candidate != deviceIDHeader && candidate != deviceHeader && candidate != valueHeader {
			return false
		}
	}
	return true
}

func SaveDeviceValueCSV(path string, deviceID int, serial string, value string) error {
	if deviceID <= 0 {
		return fmt.Errorf("device id must be positive")
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("output is required")
	}

	deviceValueCSVMu.Lock()
	defer deviceValueCSVMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	file := DataFile{
		Name:    normalizeVariableKey(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))),
		Path:    path,
		Headers: []string{deviceIDHeader, deviceHeader, valueHeader},
	}
	if _, err := os.Stat(path); err == nil {
		loaded, err := loadCSVDataFile(path)
		if err != nil {
			return err
		}
		file = loaded
	} else if !os.IsNotExist(err) {
		return err
	}

	file.Headers = ensureHeader(file.Headers, deviceIDHeader, 0)
	file.Headers = ensureHeader(file.Headers, deviceHeader, 1)
	file.Headers = ensureHeader(file.Headers, valueHeader, len(file.Headers))

	deviceIDText := strconv.Itoa(deviceID)
	serial = strings.TrimSpace(serial)
	updated := false
	for _, row := range file.Rows {
		if (serial != "" && strings.TrimSpace(row[deviceHeader]) == serial) || strings.TrimSpace(row[deviceIDHeader]) == deviceIDText {
			row[deviceIDHeader] = deviceIDText
			row[deviceHeader] = serial
			row[valueHeader] = strings.TrimSpace(value)
			updated = true
			break
		}
	}
	if !updated {
		file.Rows = append(file.Rows, map[string]string{
			deviceIDHeader: deviceIDText,
			deviceHeader:   serial,
			valueHeader:    strings.TrimSpace(value),
		})
	}

	return writeCSVDataFile(path, file.Headers, file.Rows)
}

func ensureHeader(headers []string, header string, index int) []string {
	if containsString(headers, header) {
		return headers
	}
	if index > len(headers) {
		index = len(headers)
	}
	headers = append(headers, "")
	copy(headers[index+1:], headers[index:])
	headers[index] = header
	return headers
}

func writeCSVDataFile(path string, headers []string, rows []map[string]string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	if err := writer.Write(headers); err != nil {
		return err
	}
	for _, row := range rows {
		record := make([]string, len(headers))
		for i, header := range headers {
			record[i] = row[header]
		}
		if err := writer.Write(record); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func DeviceVariables(index int, serial string) map[string]string {
	return map[string]string{
		"device.index":  strconv.Itoa(index),
		"device.id":     strconv.Itoa(index + 1),
		"device.serial": serial,
	}
}

func setVariable(variables map[string]string, key, value string) {
	variables[key] = value
	variables["data."+key] = value
}

func setDerivedVariables(variables map[string]string, key, value string) {
	parts := strings.Fields(value)
	if len(parts) > 0 {
		setVariable(variables, key+".first", parts[0])
		setVariable(variables, key+".last", strings.Join(parts[1:], " "))
	}

	proxyParts := strings.Split(value, ":")
	if len(proxyParts) == 4 {
		host := strings.TrimSpace(proxyParts[0])
		port := strings.TrimSpace(proxyParts[1])
		user := strings.TrimSpace(proxyParts[2])
		password := strings.TrimSpace(proxyParts[3])
		address := host + ":" + port

		setVariable(variables, key+".host", host)
		setVariable(variables, key+".port", port)
		setVariable(variables, key+".user", user)
		setVariable(variables, key+".password", password)
		setVariable(variables, key+".address", address)
	}
}

func normalizeVariableKey(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var builder strings.Builder
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			builder.WriteRune(r)
		case r == '_' || r == '-' || r == '.':
			builder.WriteRune(r)
		case unicode.IsSpace(r):
			builder.WriteRune('_')
		}
	}
	return builder.String()
}
