package journal

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// Encoding identifies the detected byte encoding of an MT5 log file.
type Encoding string

const (
	EncodingUTF8    Encoding = "UTF-8"
	EncodingUTF16LE Encoding = "UTF-16LE"
	EncodingUTF16BE Encoding = "UTF-16BE"
)

// Entry represents a single decoded and classified MetaTrader 5 journal or Experts log event.
type Entry struct {
	ID         int64     `json:"id"`
	Timestamp  string    `json:"timestamp"`
	ObservedAt time.Time `json:"observed_at"`
	Severity   string    `json:"severity"` // info | warn | error | trade
	Category   string    `json:"category"` // connection | autotrading | order | ea_error | journal
	RuleID     string    `json:"rule_id,omitempty"`
	Source     string    `json:"source"`
	LogKind    string    `json:"log_kind"` // experts | terminal
	File       string    `json:"file"`
	Encoding   Encoding  `json:"encoding"`
	Message    string    `json:"message"`
	Raw        string    `json:"raw,omitempty"`
}

// FileState reports tracked tailing metadata for a discovered MT5 log file.
type FileState struct {
	Path      string    `json:"path"`
	LogKind   string    `json:"log_kind"`
	Encoding  Encoding  `json:"encoding"`
	Offset    int64     `json:"offset"`
	Size      int64     `json:"size"`
	LineCount int       `json:"line_count"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Summary aggregates event counts across severity and category dimensions.
type Summary struct {
	Total       int `json:"total"`
	Errors      int `json:"errors"`
	Warnings    int `json:"warnings"`
	Trades      int `json:"trades"`
	Connections int `json:"connections"`
	TrackedFiles int `json:"tracked_files"`
}

// DetectEncoding inspects a byte prefix and determines whether a log file is UTF-16LE (with or without BOM),
// UTF-16BE, or UTF-8. MetaTrader 5 natively writes Windows/Wine logs in UTF-16LE.
func DetectEncoding(sample []byte) (Encoding, int) {
	if len(sample) >= 2 {
		if sample[0] == 0xFF && sample[1] == 0xFE {
			return EncodingUTF16LE, 2
		}
		if sample[0] == 0xFE && sample[1] == 0xFF {
			return EncodingUTF16BE, 2
		}
	}
	if len(sample) >= 3 && sample[0] == 0xEF && sample[1] == 0xBB && sample[2] == 0xBF {
		return EncodingUTF8, 3
	}

	// Heuristic for BOM-less UTF-16LE (common when inspecting mid-file chunks or tool-exported MT5 logs).
	if len(sample) >= 4 {
		limit := len(sample)
		if limit > 512 {
			limit = 512
		}
		limit -= limit % 2
		pairs := limit / 2
		oddNulls := 0
		evenNulls := 0
		for i := 0; i < limit; i += 2 {
			if sample[i] == 0x00 {
				evenNulls++
			}
			if sample[i+1] == 0x00 {
				oddNulls++
			}
		}
		if pairs > 0 {
			oddRatio := float64(oddNulls) / float64(pairs)
			evenRatio := float64(evenNulls) / float64(pairs)
			if oddRatio >= 0.30 && evenRatio < 0.05 {
				return EncodingUTF16LE, 0
			}
			if evenRatio >= 0.30 && oddRatio < 0.05 {
				return EncodingUTF16BE, 0
			}
		}
	}

	return EncodingUTF8, 0
}

// DecodeChunk converts raw bytes in the specified encoding into UTF-8 text and returns
// both the decoded text and the exact number of bytes consumed (preserving alignment for UTF-16).
func DecodeChunk(data []byte, enc Encoding) (string, int) {
	switch enc {
	case EncodingUTF16LE:
		usable := len(data) - (len(data) % 2)
		if usable == 0 {
			return "", 0
		}
		u16 := make([]uint16, usable/2)
		for i := 0; i < usable; i += 2 {
			u16[i/2] = binary.LittleEndian.Uint16(data[i : i+2])
		}
		return string(utf16.Decode(u16)), usable

	case EncodingUTF16BE:
		usable := len(data) - (len(data) % 2)
		if usable == 0 {
			return "", 0
		}
		u16 := make([]uint16, usable/2)
		for i := 0; i < usable; i += 2 {
			u16[i/2] = binary.BigEndian.Uint16(data[i : i+2])
		}
		return string(utf16.Decode(u16)), usable

	default:
		return string(data), len(data)
	}
}

// EncodeUTF16LE encodes a UTF-8 Go string into UTF-16LE bytes (optionally prefixed with the 0xFF 0xFE BOM).
func EncodeUTF16LE(text string, includeBOM bool) []byte {
	runes := utf16.Encode([]rune(text))
	extra := 0
	if includeBOM {
		extra = 2
	}
	buf := make([]byte, extra+len(runes)*2)
	if includeBOM {
		buf[0] = 0xFF
		buf[1] = 0xFE
	}
	for i, r := range runes {
		binary.LittleEndian.PutUint16(buf[extra+i*2:], r)
	}
	return buf
}

type classificationRule struct {
	ruleID   string
	severity string
	category string
	patterns []string
}

var mt5Rules = []classificationRule{
	{
		ruleID:   "mt5_autotrading_disabled",
		severity: "error",
		category: "autotrading",
		patterns: []string{"autotrading disabled", "automated trading is disabled", "algo trading disabled", "algotrading disabled"},
	},
	{
		ruleID:   "mt5_error_4756",
		severity: "error",
		category: "order",
		patterns: []string{"error 4756", "err 4756", "code 4756", "(4756)", "retcode 10006", "retcode 10018"},
	},
	{
		ruleID:   "mt5_no_connection",
		severity: "error",
		category: "connection",
		patterns: []string{"no connection", "connection lost", "connection to", "disconnected", "network is unreachable"},
	},
	{
		ruleID:   "mt5_common_error",
		severity: "error",
		category: "ea_error",
		patterns: []string{"common error", "critical runtime error", "array out of range", "zero divide", "invalid pointer"},
	},
	{
		ruleID:   "mt5_not_enough_money",
		severity: "error",
		category: "order",
		patterns: []string{"not enough money", "no money", "insufficient margin"},
	},
	{
		ruleID:   "mt5_invalid_stops",
		severity: "error",
		category: "order",
		patterns: []string{"invalid stops", "invalid sl/tp", "invalid stop loss"},
	},
	{
		ruleID:   "mt5_trade_context_busy",
		severity: "warn",
		category: "order",
		patterns: []string{"trade context is busy", "trade context busy"},
	},
	{
		ruleID:   "mt5_requote",
		severity: "warn",
		category: "order",
		patterns: []string{"requote", "off quotes", "price changed"},
	},
	{
		ruleID:   "mt5_ordersend_failed",
		severity: "error",
		category: "order",
		patterns: []string{"ordersend failed", "order send failed", "failed instant", "failed market", "failed buy", "failed sell"},
	},
	{
		ruleID:   "mt5_trade_filled",
		severity: "trade",
		category: "order",
		patterns: []string{"filled", "deal #", "position opened", "position closed", "order #", "take profit triggered", "stop loss triggered"},
	},
	{
		ruleID:   "mt5_connection_ok",
		severity: "info",
		category: "connection",
		patterns: []string{"authorized on", "connected to", "ping:", "synchronized", "access server"},
	},
}

// ClassifyLine parses an MT5 tab-delimited or plain timestamped log line into a structured Entry.
func ClassifyLine(line, fileName, logKind string, enc Encoding) Entry {
	clean := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
	entry := Entry{
		ObservedAt: time.Now(),
		Severity:   "info",
		Category:   "journal",
		Source:     "MetaTrader 5",
		LogKind:    logKind,
		File:       fileName,
		Encoding:   enc,
		Message:    clean,
		Raw:        clean,
	}

	// Standard MT5 journal format:
	// <code>\t<level>\t<HH:MM:SS.mmm>\t<Source>\t<Message>
	parts := strings.Split(clean, "\t")
	mt5Level := ""
	if len(parts) >= 5 {
		mt5Level = strings.TrimSpace(parts[1])
		entry.Timestamp = strings.TrimSpace(parts[2])
		entry.Source = strings.TrimSpace(parts[3])
		entry.Message = strings.TrimSpace(strings.Join(parts[4:], " "))
	} else if len(parts) >= 3 {
		entry.Timestamp = strings.TrimSpace(parts[0])
		entry.Source = strings.TrimSpace(parts[1])
		entry.Message = strings.TrimSpace(strings.Join(parts[2:], " "))
	} else {
		// Fallback for space-delimited timestamps like "2026.11.25 09:15:01.003 ..."
		fields := strings.Fields(clean)
		if len(fields) >= 3 && (strings.Contains(fields[0], ".") || strings.Contains(fields[0], "-")) && strings.Contains(fields[1], ":") {
			entry.Timestamp = fields[0] + " " + fields[1]
			entry.Message = strings.TrimSpace(strings.Join(fields[2:], " "))
		}
	}

	lower := strings.ToLower(entry.Source + " " + entry.Message)

	// Guard against matching "connection to X established/authorized" as disconnect.
	for _, rule := range mt5Rules {
		if rule.ruleID == "mt5_no_connection" {
			if strings.Contains(lower, "no connection") || strings.Contains(lower, "connection lost") || strings.Contains(lower, "disconnected") || strings.Contains(lower, "lost -") {
				entry.Severity = rule.severity
				entry.Category = rule.category
				entry.RuleID = rule.ruleID
				return entry
			}
			continue
		}
		if rule.ruleID == "mt5_trade_filled" {
			// Never classify a failed order line as a filled trade.
			if strings.Contains(lower, "failed") || strings.Contains(lower, "rejected") || strings.Contains(lower, "error") {
				continue
			}
		}
		for _, p := range rule.patterns {
			if strings.Contains(lower, p) {
				entry.Severity = rule.severity
				entry.Category = rule.category
				entry.RuleID = rule.ruleID
				return entry
			}
		}
	}

	// Fallback classification based on MT5 column source / severity level.
	srcLower := strings.ToLower(entry.Source)
	switch {
	case strings.Contains(srcLower, "network"):
		entry.Category = "connection"
	case strings.Contains(srcLower, "trade"):
		entry.Category = "order"
	case strings.Contains(srcLower, "expert") || strings.Contains(logKind, "experts"):
		if mt5Level == "2" || strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
			entry.Category = "ea_error"
		}
	}

	if mt5Level == "2" || strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
		entry.Severity = "error"
	} else if mt5Level == "1" || strings.Contains(lower, "warn") {
		entry.Severity = "warn"
	}

	return entry
}

// ParseFile decodes and parses an entire MT5 log file in UTF-16LE (BOM or BOM-less) or UTF-8.
func ParseFile(path string) ([]Entry, Encoding, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read log file %q: %w", path, err)
	}
	enc, bomLen := DetectEncoding(raw)
	text, _ := DecodeChunk(raw[bomLen:], enc)

	logKind := inferLogKind(path)
	base := filepath.Base(path)
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	entries := make([]Entry, 0, len(lines))
	var nextID int64 = 1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		e := ClassifyLine(line, base, logKind, enc)
		e.ID = nextID
		nextID++
		entries = append(entries, e)
	}
	return entries, enc, nil
}

func inferLogKind(path string) string {
	norm := strings.ToLower(filepath.ToSlash(path))
	if strings.Contains(norm, "/mql5/logs/") || strings.Contains(norm, "experts") {
		return "experts"
	}
	return "terminal"
}

type trackedFile struct {
	path        string
	logKind     string
	encoding    Encoding
	encodingSet bool
	offset      int64
	size        int64
	lineCount   int
	partialLine string
	updatedAt   time.Time
}

// Tailer incrementally tails MetaTrader 5 terminal and MQL5/Logs files with constant memory
// and per-file byte offsets so alerts are never duplicated.
type Tailer struct {
	rootDir      string
	maxEntries   int
	onAlertEntry func(Entry)

	mu      sync.RWMutex
	files   map[string]*trackedFile
	entries []Entry
	nextID  int64
}

// NewTailer creates a new incremental MT5 log tailer rooted at logsDir.
func NewTailer(logsDir string, maxEntries int, onAlertEntry func(Entry)) *Tailer {
	if maxEntries <= 0 {
		maxEntries = 500
	}
	return &Tailer{
		rootDir:      logsDir,
		maxEntries:   maxEntries,
		onAlertEntry: onAlertEntry,
		files:        make(map[string]*trackedFile),
		entries:      make([]Entry, 0, maxEntries),
		nextID:       1,
	}
}

// Poll scans configured MT5 log directories and incrementally decodes newly written bytes.
// When emitAlerts is false (e.g. initial historical hydration), existing entries are loaded without firing alerts.
func (t *Tailer) Poll(emitAlerts bool) ([]Entry, error) {
	if strings.TrimSpace(t.rootDir) == "" {
		return nil, nil
	}

	paths := discoverLogFiles(t.rootDir)
	var newlyParsed []Entry

	for _, p := range paths {
		parsed, err := t.tailFile(p)
		if err != nil {
			continue
		}
		if len(parsed) > 0 {
			newlyParsed = append(newlyParsed, parsed...)
		}
	}

	if emitAlerts && t.onAlertEntry != nil {
		for _, e := range newlyParsed {
			if e.Severity == "error" || e.Severity == "warn" {
				t.onAlertEntry(e)
			}
		}
	}
	return newlyParsed, nil
}

// Run performs an initial non-alerting hydration scan and then polls on every tick until ctx is cancelled.
func (t *Tailer) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	_, _ = t.Poll(false)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = t.Poll(true)
		}
	}
}

func (t *Tailer) tailFile(path string) ([]Entry, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil, err
	}

	t.mu.Lock()
	tf, exists := t.files[path]
	if !exists {
		tf = &trackedFile{
			path:    path,
			logKind: inferLogKind(path),
		}
		t.files[path] = tf
	}
	// Reset state cleanly on file truncation or daily rotation.
	if info.Size() < tf.offset {
		tf.offset = 0
		tf.encodingSet = false
		tf.partialLine = ""
	}
	startOffset := tf.offset
	enc := tf.encoding
	encSet := tf.encodingSet
	partial := tf.partialLine
	t.mu.Unlock()

	if info.Size() == startOffset {
		return nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	if startOffset > 0 {
		if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
			return nil, err
		}
	}

	// Read bounded chunk (up to 2 MiB per poll) to keep memory usage strictly constant even on huge logs.
	const maxChunk = 2 * 1024 * 1024
	raw, err := io.ReadAll(io.LimitReader(f, maxChunk))
	if err != nil || len(raw) == 0 {
		return nil, err
	}

	bomLen := 0
	if !encSet {
		enc, bomLen = DetectEncoding(raw)
		if len(raw) < 2 && info.Size() < 2 {
			return nil, nil
		}
		encSet = true
	}

	payload := raw[bomLen:]
	text, consumed := DecodeChunk(payload, enc)
	newOffset := startOffset + int64(bomLen+consumed)

	combined := partial + strings.ReplaceAll(text, "\r\n", "\n")
	// Only process lines terminated by '\n' so mid-write flushes by MT5 are held until complete.
	var completeLines []string
	var nextPartial string
	if idx := strings.LastIndex(combined, "\n"); idx >= 0 {
		completePart := combined[:idx]
		nextPartial = combined[idx+1:]
		completeLines = strings.Split(completePart, "\n")
	} else {
		nextPartial = combined
	}

	base := filepath.Base(path)
	kind := inferLogKind(path)
	var parsed []Entry

	t.mu.Lock()
	tf.encoding = enc
	tf.encodingSet = encSet
	tf.offset = newOffset
	tf.size = info.Size()
	tf.partialLine = nextPartial
	tf.updatedAt = info.ModTime()

	for _, line := range completeLines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		e := ClassifyLine(line, base, kind, enc)
		e.ID = t.nextID
		t.nextID++
		tf.lineCount++
		parsed = append(parsed, e)

		if len(t.entries) >= t.maxEntries {
			copy(t.entries, t.entries[1:])
			t.entries[len(t.entries)-1] = e
		} else {
			t.entries = append(t.entries, e)
		}
	}
	t.mu.Unlock()

	return parsed, nil
}

// Entries returns recent decoded MT5 journal entries filtered by "all", "errors", "trades", or "connections".
func (t *Tailer) Entries(filter string, limit int) []Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if limit <= 0 || limit > t.maxEntries {
		limit = 100
	}
	f := strings.ToLower(strings.TrimSpace(filter))
	result := make([]Entry, 0, limit)

	// Return newest first for dashboard stream ergonomics.
	for i := len(t.entries) - 1; i >= 0 && len(result) < limit; i-- {
		e := t.entries[i]
		if !MatchesFilter(e, f) {
			continue
		}
		result = append(result, e)
	}
	return result
}

// MatchesFilter evaluates whether an entry belongs to the requested dashboard/CLI filter tab.
func MatchesFilter(e Entry, filter string) bool {
	switch strings.ToLower(strings.TrimSpace(filter)) {
	case "", "all":
		return true
	case "errors", "error":
		return e.Severity == "error" || e.Severity == "warn"
	case "trades", "trade":
		return e.Severity == "trade" || e.Category == "order"
	case "connections", "connection":
		return e.Category == "connection"
	default:
		return true
	}
}

// Files returns metadata about all tracked MT5 log files.
func (t *Tailer) Files() []FileState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make([]FileState, 0, len(t.files))
	for _, tf := range t.files {
		out = append(out, FileState{
			Path:      tf.path,
			LogKind:   tf.logKind,
			Encoding:  tf.encoding,
			Offset:    tf.offset,
			Size:      tf.size,
			LineCount: tf.lineCount,
			UpdatedAt: tf.updatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Summary returns aggregate event counts across buffered entries.
func (t *Tailer) Summary() Summary {
	t.mu.RLock()
	defer t.mu.RUnlock()

	s := Summary{
		Total:        len(t.entries),
		TrackedFiles: len(t.files),
	}
	for _, e := range t.entries {
		switch e.Severity {
		case "error":
			s.Errors++
		case "warn":
			s.Warnings++
		case "trade":
			s.Trades++
		}
		if e.Category == "connection" {
			s.Connections++
		}
	}
	return s
}

func discoverLogFiles(root string) []string {
	candidates := []string{
		root,
		filepath.Join(root, "Logs"),
		filepath.Join(root, "MQL5", "Logs"),
	}
	seen := make(map[string]struct{})
	var found []string

	for _, dir := range candidates {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, de := range entries {
			if de.IsDir() {
				continue
			}
			if !strings.EqualFold(filepath.Ext(de.Name()), ".log") {
				continue
			}
			full := filepath.Join(dir, de.Name())
			abs, err := filepath.Abs(full)
			if err == nil {
				full = abs
			}
			if _, ok := seen[full]; !ok {
				seen[full] = struct{}{}
				found = append(found, full)
			}
		}
	}
	sort.Strings(found)
	return found
}

// GenerateSampleUTF16LELog writes a realistic UTF-16LE (with BOM) MetaTrader 5 log fixture
// used by `tradebox init` so users can test `parse-log` and inspect the dashboard immediately.
func GenerateSampleUTF16LELog(path string) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create sample log directory: %w", err)
		}
	}
	lines := []string{
		"CS\t0\t08:00:01.104\tNetwork\t'50192831': authorized on MetaQuotes-Demo through Access Server EU (ping: 14.20 ms)",
		"EO\t0\t08:00:02.415\tTrendBreakoutEA (EURUSD,M15)\tExpert TrendBreakoutEA EURUSD,M15: loaded successfully",
		"CJ\t0\t08:15:00.221\tTrades\t'50192831': instant buy 0.10 EURUSD at 1.08420 filled (deal #44182011)",
		"EO\t1\t08:32:14.018\tTrendBreakoutEA (EURUSD,M15)\tTrade context is busy, retrying order modification",
		"CJ\t1\t08:45:02.774\tTrades\t'50192831': Requote 1.08455 / 1.08468 on XAUUSD",
		"EO\t2\t09:05:11.903\tTrendBreakoutEA (EURUSD,M15)\tOrderSend failed: Error 4756 (Trade request sending failed)",
		"CJ\t2\t09:05:12.104\tTrades\t'50192831': failed instant sell 0.10 EURUSD [AutoTrading disabled by client]",
		"CS\t2\t09:12:48.550\tNetwork\t'50192831': connection to MetaQuotes-Demo lost - No connection",
		"CS\t0\t09:12:54.312\tNetwork\t'50192831': authorized on MetaQuotes-Demo through Access Server EU (ping: 15.10 ms)",
		"CJ\t0\t09:30:00.410\tTrades\t'50192831': position closed deal #44182099 profit +24.80 USD",
	}
	content := strings.Join(lines, "\r\n") + "\r\n"
	encoded := EncodeUTF16LE(content, true)
	return os.WriteFile(path, encoded, 0o644)
}
