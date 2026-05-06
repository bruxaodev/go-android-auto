package ocr

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"unicode"
)

const (
	defaultOCRPSM  = 6
	fallbackOCRPSM = 11
)

type Tesseract struct {
	Path        string
	DefultPSM   int
	FallbackPSM int
}

func NewTesseract(path string) *Tesseract {
	if path == "" {
		path = "tesseract"
	}
	return &Tesseract{
		Path: path,
	}
}

type Options struct {
	Lang string
	PSM  int
}

type Bounds struct {
	Left   int
	Top    int
	Right  int
	Bottom int
}

type Word struct {
	Text   string
	Bounds Bounds
}

type ocrWord struct {
	Text   string
	Left   int
	Top    int
	Width  int
	Height int
}

type ocrLine struct {
	key   string
	words []ocrWord
}

type Engine interface {
	FindText(ctx context.Context, imagePath string, target string, options Options) (*Bounds, error)
	Text(ctx context.Context, imagePath string, options Options) (string, error)
}

func (t *Tesseract) Text(ctx context.Context, imagePath string, options Options) (string, error) {
	psmValues := t.psmValues(options.PSM)

	var lastErr error
	for _, psm := range psmValues {
		stdout, err := t.runTesseractText(ctx, imagePath, options.Lang, psm, nil)
		if err != nil {
			lastErr = fmt.Errorf("tesseract OCR failed with PSM=%d: %w", psm, err)
			continue
		}

		return string(stdout), nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no OCR attempts were executed")
	}

	return "", lastErr
}

func (t *Tesseract) Words(ctx context.Context, imagePath string, options Options) ([]Word, error) {
	psmValues := t.psmValues(options.PSM)

	var lastErr error
	for _, psm := range psmValues {
		stdout, err := t.runTesseractTSV(ctx, imagePath, options.Lang, psm)
		if err != nil {
			lastErr = fmt.Errorf("tesseract OCR failed with PSM=%d: %w", psm, err)
			continue
		}

		words, err := wordsFromTSV(bytes.NewReader(stdout))
		if err == nil {
			return words, nil
		}

		lastErr = fmt.Errorf("failed to read OCR words with PSM=%d: %w", psm, err)
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no OCR attempts were executed")
	}

	return nil, lastErr
}

func (t *Tesseract) DigitText(ctx context.Context, imagePath string, options Options) (string, error) {
	psmValues := t.psmValues(options.PSM)

	var lastErr error
	for _, psm := range psmValues {
		stdout, err := t.runTesseractText(ctx, imagePath, options.Lang, psm, []string{
			"tessedit_char_whitelist=0123456789",
			"load_system_dawg=0",
			"load_freq_dawg=0",
			"classify_bln_numeric_mode=1",
		})
		if err != nil {
			lastErr = fmt.Errorf("tesseract digit OCR failed with PSM=%d: %w", psm, err)
			continue
		}

		return string(stdout), nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no OCR attempts were executed")
	}

	return "", lastErr
}

func (t *Tesseract) runTesseractText(ctx context.Context, screenshot string, lang string, psm int, configs []string) ([]byte, error) {
	args := []string{screenshot, "stdout"}
	if lang != "" {
		args = append(args, "-l", lang)
	}
	args = append(args, "--psm", strconv.Itoa(psm))
	for _, config := range configs {
		args = append(args, "-c", config)
	}

	cmd := exec.CommandContext(ctx, t.Path, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("tesseract command failed: %w\nstderr: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func (t *Tesseract) runTesseractTSV(ctx context.Context, screenshot string, lang string, psm int) ([]byte, error) {
	args := []string{screenshot, "stdout", "--psm", strconv.Itoa(psm), "tsv"}
	if lang != "" {
		args = []string{screenshot, "stdout", "-l", lang, "--psm", strconv.Itoa(psm), "tsv"}
	}

	cmd := exec.CommandContext(ctx, t.Path, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("tesseract command failed: %w\nstderr: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func (t *Tesseract) FindText(ctx context.Context, imagePath string, target string, options Options) (*Bounds, error) {
	psmValues := t.psmValues(options.PSM)

	var lastErr error
	for _, psm := range psmValues {
		stdout, err := t.runTesseractTSV(ctx, imagePath, options.Lang, psm)
		if err != nil {
			lastErr = fmt.Errorf("tesseract OCR failed with PSM=%d: %w", psm, err)
			continue
		}

		bounds, err := t.findTextBoundsFromTSV(bytes.NewReader(stdout), target)
		if err == nil {
			return bounds, nil
		}

		lastErr = fmt.Errorf("text %q not found with PSM=%d: %w", target, psm, err)
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no OCR attempts were executed")
	}

	return nil, lastErr
}

func (t *Tesseract) psmValues(psm int) []int {
	if psm > 0 {
		return []int{psm}
	}

	defaultPSM := t.DefultPSM
	if defaultPSM <= 0 {
		defaultPSM = defaultOCRPSM
	}
	fallbackPSM := t.FallbackPSM
	if fallbackPSM <= 0 {
		fallbackPSM = fallbackOCRPSM
	}

	values := []int{defaultPSM}
	if fallbackPSM != defaultPSM {
		values = append(values, fallbackPSM)
	}
	return values
}

func (t *Tesseract) findTextBoundsFromTSV(input io.Reader, target string) (*Bounds, error) {
	rawTarget := strings.TrimSpace(target)
	target = normalizeText(target)
	if target == "" {
		return nil, fmt.Errorf("target text is empty after normalization")
	}

	reader := csv.NewReader(input)
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("failed to read TSV header: %w", err)
	}
	columns := indexColumns(header)
	lines := make([]ocrLine, 0)
	lineIndex := make(map[string]int)

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read TSV record: %w", err)
		}
		if value(record, columns, "level") != "5" {
			continue
		}

		wordText := strings.TrimSpace(value(record, columns, "text"))
		if wordText == "" {
			continue
		}

		word, err := paraseOCRWord(record, columns, wordText)
		if err != nil {
			return nil, fmt.Errorf("failed to parse OCR word: %w", err)
		}

		key := strings.Join([]string{
			value(record, columns, "page_num"),
			value(record, columns, "block_num"),
			value(record, columns, "par_num"),
			value(record, columns, "line_num"),
		}, "/")

		index, ok := lineIndex[key]
		if !ok {
			index = len(lines)
			lines = append(lines, ocrLine{key: key})
			lineIndex[key] = index
		}
		lines[index].words = append(lines[index].words, word)
	}

	visibleLines := make([]string, 0, len(lines))
	for _, line := range lines {
		text := line.text()
		if text == "" {
			continue
		}
		visibleLines = append(visibleLines, text)
	}

	for _, line := range lines {
		if bounds := line.findBounds(rawTarget, target, true); bounds != nil {
			return bounds, nil
		}
	}

	for _, line := range lines {
		if bounds := line.findBounds(rawTarget, target, false); bounds != nil {
			return bounds, nil
		}
	}

	return nil, fmt.Errorf("target text not found in OCR results: %s\nVisible lines: %v", target, visibleLines)
}

func normalizeText(text string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return unicode.IsSpace(r)
	}), " ")
}

func wordsFromTSV(input io.Reader) ([]Word, error) {
	reader := csv.NewReader(input)
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("failed to read TSV header: %w", err)
	}
	columns := indexColumns(header)

	words := make([]Word, 0)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read TSV record: %w", err)
		}
		if value(record, columns, "level") != "5" {
			continue
		}

		wordText := strings.TrimSpace(value(record, columns, "text"))
		if wordText == "" {
			continue
		}

		word, err := paraseOCRWord(record, columns, wordText)
		if err != nil {
			return nil, fmt.Errorf("failed to parse OCR word: %w", err)
		}

		words = append(words, Word{
			Text: word.Text,
			Bounds: Bounds{
				Left:   word.Left,
				Top:    word.Top,
				Right:  word.Left + word.Width,
				Bottom: word.Top + word.Height,
			},
		})
	}

	return words, nil
}

func indexColumns(header []string) map[string]int {
	colums := make(map[string]int, len(header))
	for i, col := range header {
		colums[col] = i
	}
	return colums
}

func value(record []string, columns map[string]int, column string) string {
	index, ok := columns[column]
	if !ok || index >= len(record) {
		return ""
	}
	return record[index]
}

func paraseOCRWord(record []string, columns map[string]int, text string) (ocrWord, error) {
	left, err := atoiColumn(record, columns, "left")
	if err != nil {
		return ocrWord{}, fmt.Errorf("failed to parse left column: %w", err)
	}
	top, err := atoiColumn(record, columns, "top")
	if err != nil {
		return ocrWord{}, fmt.Errorf("failed to parse top column: %w", err)
	}
	width, err := atoiColumn(record, columns, "width")
	if err != nil {
		return ocrWord{}, fmt.Errorf("failed to parse width column: %w", err)
	}
	height, err := atoiColumn(record, columns, "height")
	if err != nil {
		return ocrWord{}, fmt.Errorf("failed to parse height column: %w", err)
	}
	return ocrWord{
		Text:   text,
		Left:   left,
		Top:    top,
		Width:  width,
		Height: height,
	}, nil
}

func atoiColumn(record []string, columns map[string]int, column string) (int, error) {
	raw := value(record, columns, column)
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("failed to parse column %s value %s as int: %w", column, raw, err)
	}
	return value, nil
}

func (l ocrLine) text() string {
	words := make([]string, 0, len(l.words))
	for _, word := range l.words {
		words = append(words, word.Text)
	}
	return strings.Join(words, " ")
}

func (l ocrLine) bounds() *Bounds {
	return wordsBounds(l.words)
}

func (l ocrLine) findBounds(rawTarget, target string, exact bool) *Bounds {
	for length := 1; length <= len(l.words); length++ {
		for start := 0; start+length <= len(l.words); start++ {
			words := l.words[start : start+length]
			parts := make([]string, 0, len(words))
			for _, word := range words {
				parts = append(parts, word.Text)
			}
			text := strings.Join(parts, " ")
			if exact && text == rawTarget {
				return wordsBounds(words)
			}
			if exact {
				continue
			}

			if strings.Contains(normalizeText(text), target) {
				return wordsBounds(words)
			}
		}
	}

	return nil
}

func wordsBounds(words []ocrWord) *Bounds {
	if len(words) == 0 {
		return nil
	}

	bounds := &Bounds{
		Left:   words[0].Left,
		Top:    words[0].Top,
		Right:  words[0].Left + words[0].Width,
		Bottom: words[0].Top + words[0].Height,
	}
	for _, word := range words[1:] {
		bounds.Left = min(bounds.Left, word.Left)
		bounds.Top = min(bounds.Top, word.Top)
		bounds.Right = max(bounds.Right, word.Left+word.Width)
		bounds.Bottom = max(bounds.Bottom, word.Top+word.Height)
	}
	return bounds
}

// Center returns the middle point of the OCR rectangle.
func (b Bounds) Center() (int, int) {
	return (b.Left + b.Right) / 2, (b.Top + b.Bottom) / 2
}
