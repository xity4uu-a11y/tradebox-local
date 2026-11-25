package journal

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestParseFileUTF16LEWithBOMAndClassifications(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "MQL5", "Logs", "20261125.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	rawLines := []string{
		"CS\t0\t08:00:01.100\tNetwork\t'501234': authorized on Broker-Live (ping: 12.4 ms)",
		"CS\t2\t08:01:02.200\tNetwork\t'501234': No connection to trade server",
		"EO\t2\t08:02:03.300\tGridEA (EURUSD,H1)\tCommon error during symbol info lookup",
		"CJ\t2\t08:03:04.400\tTrades\t'501234': failed instant buy 0.10 EURUSD [AutoTrading disabled by client]",
		"EO\t1\t08:04:05.500\tGridEA (EURUSD,H1)\tTrade context is busy",
		"CJ\t1\t08:05:06.600\tTrades\tRequote 1.0841 / 1.0843",
		"CJ\t2\t08:06:07.700\tTrades\tInvalid stops on order #9910",
		"CJ\t2\t08:07:08.800\tTrades\tNot enough money for buy 1.00 XAUUSD",
		"EO\t2\t08:08:09.900\tGridEA (EURUSD,H1)\tOrderSend failed: Error 4756",
		"EO\t2\t08:09:10.100\tGridEA (EURUSD,H1)\tOrderSend failed due to broker reject",
		"CJ\t0\t08:10:11.200\tTrades\t'501234': instant buy 0.10 EURUSD at 1.08500 filled (deal #55210)",
	}
	encoded := EncodeUTF16LE(strings.Join(rawLines, "\r\n")+"\r\n", true)
	if err := os.WriteFile(logPath, encoded, 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	entries, enc, err := ParseFile(logPath)
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}
	if enc != EncodingUTF16LE {
		t.Fatalf("expected UTF-16LE, got %s", enc)
	}
	if len(entries) != len(rawLines) {
		t.Fatalf("expected %d entries, got %d", len(rawLines), len(entries))
	}

	want := []struct {
		ruleID   string
		severity string
		category string
	}{
		{"mt5_connection_ok", "info", "connection"},
		{"mt5_no_connection", "error", "connection"},
		{"mt5_common_error", "error", "ea_error"},
		{"mt5_autotrading_disabled", "error", "autotrading"},
		{"mt5_trade_context_busy", "warn", "order"},
		{"mt5_requote", "warn", "order"},
		{"mt5_invalid_stops", "error", "order"},
		{"mt5_not_enough_money", "error", "order"},
		{"mt5_error_4756", "error", "order"},
		{"mt5_ordersend_failed", "error", "order"},
		{"mt5_trade_filled", "trade", "order"},
	}

	for i, w := range want {
		e := entries[i]
		if e.RuleID != w.ruleID || e.Severity != w.severity || e.Category != w.category {
			t.Errorf("line %d (%q): got (%s, %s, %s), want (%s, %s, %s)",
				i, e.Message, e.RuleID, e.Severity, e.Category, w.ruleID, w.severity, w.category)
		}
	}
}

func TestParseFileUTF16LEWithoutBOMAndUTF8(t *testing.T) {
	dir := t.TempDir()

	// BOM-less UTF-16LE
	noBOMPath := filepath.Join(dir, "nobom.log")
	content := "EO\t2\t10:00:00.001\tScalperEA\tError 4756 while sending order\r\n"
	if err := os.WriteFile(noBOMPath, EncodeUTF16LE(content, false), 0o644); err != nil {
		t.Fatalf("write nobom failed: %v", err)
	}
	entries, enc, err := ParseFile(noBOMPath)
	if err != nil {
		t.Fatalf("ParseFile nobom failed: %v", err)
	}
	if enc != EncodingUTF16LE {
		t.Errorf("expected BOM-less UTF-16LE detection, got %s", enc)
	}
	if len(entries) != 1 || entries[0].RuleID != "mt5_error_4756" {
		t.Errorf("unexpected BOM-less parsed entries: %+v", entries)
	}

	// Plain UTF-8
	utf8Path := filepath.Join(dir, "utf8.log")
	utf8Text := "2026.11.25 10:01:00.123 Network: No connection to broker\n"
	if err := os.WriteFile(utf8Path, []byte(utf8Text), 0o644); err != nil {
		t.Fatalf("write utf8 failed: %v", err)
	}
	entries2, enc2, err := ParseFile(utf8Path)
	if err != nil {
		t.Fatalf("ParseFile utf8 failed: %v", err)
	}
	if enc2 != EncodingUTF8 {
		t.Errorf("expected UTF-8 detection, got %s", enc2)
	}
	if len(entries2) != 1 || entries2[0].RuleID != "mt5_no_connection" {
		t.Errorf("unexpected UTF-8 parsed entries: %+v", entries2)
	}
}

func TestTailerIncrementalOffsetsNoDuplicateAlertsAndSplitUTF16Write(t *testing.T) {
	dir := t.TempDir()
	logsDir := filepath.Join(dir, "MQL5", "Logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	logFile := filepath.Join(logsDir, "20261125.log")

	// Initial write with BOM + 1 normal line.
	chunk1 := EncodeUTF16LE("CS\t0\t09:00:00.000\tNetwork\tauthorized on Demo\r\n", true)
	if err := os.WriteFile(logFile, chunk1, 0o644); err != nil {
		t.Fatalf("write chunk1 failed: %v", err)
	}

	var mu sync.Mutex
	var alerted []Entry

	tailer := NewTailer(dir, 100, func(e Entry) {
		mu.Lock()
		alerted = append(alerted, e)
		mu.Unlock()
	})

	// Initial hydration should not fire alerts.
	initial, err := tailer.Poll(false)
	if err != nil {
		t.Fatalf("initial Poll failed: %v", err)
	}
	if len(initial) != 1 {
		t.Fatalf("expected 1 initial entry, got %d", len(initial))
	}

	// Second write: simulate an odd-byte partial UTF-16LE flush in the middle of an error line!
	errLine := EncodeUTF16LE("EO\t2\t09:05:00.000\tBreakoutEA\tOrderSend failed: Error 4756\r\n", false)
	splitPoint := 17 // deliberately odd byte boundary
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open append failed: %v", err)
	}
	if _, err := f.Write(errLine[:splitPoint]); err != nil {
		t.Fatalf("write part1 failed: %v", err)
	}
	_ = f.Close()

	midParsed, err := tailer.Poll(true)
	if err != nil {
		t.Fatalf("mid Poll failed: %v", err)
	}
	if len(midParsed) != 0 {
		t.Fatalf("expected 0 complete lines on partial write, got %d", len(midParsed))
	}

	// Finish flushing the remainder of the UTF-16LE error line.
	f, err = os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open append2 failed: %v", err)
	}
	if _, err := f.Write(errLine[splitPoint:]); err != nil {
		t.Fatalf("write part2 failed: %v", err)
	}
	_ = f.Close()

	afterParsed, err := tailer.Poll(true)
	if err != nil {
		t.Fatalf("after Poll failed: %v", err)
	}
	if len(afterParsed) != 1 || afterParsed[0].RuleID != "mt5_error_4756" {
		t.Fatalf("expected 1 decoded Error 4756 entry, got %+v", afterParsed)
	}

	// Poll again with no new data -> must produce 0 entries and 0 duplicate alerts.
	noop, err := tailer.Poll(true)
	if err != nil {
		t.Fatalf("noop Poll failed: %v", err)
	}
	if len(noop) != 0 {
		t.Fatalf("expected 0 entries on noop poll, got %d", len(noop))
	}

	mu.Lock()
	alertCount := len(alerted)
	mu.Unlock()
	if alertCount != 1 {
		t.Errorf("expected exactly 1 alert emitted without duplicates, got %d", alertCount)
	}
}
