package power

import (
	"strings"
	"testing"
)

func TestInspectHostReturnsValidDiagnosticsAndPowerGuides(t *testing.T) {
	report := InspectHost()
	if report.CPUCores < 1 {
		t.Fatalf("expected >= 1 CPU core, got %d", report.CPUCores)
	}
	if len(report.Checks) < 3 {
		t.Fatalf("expected at least 3 readiness checks, got %d", len(report.Checks))
	}
	if len(report.PowerGuide.KeepAwakeCmds) == 0 {
		t.Fatal("expected non-empty OS keep-awake commands")
	}
	if len(report.AllGuides) != 3 {
		t.Fatalf("expected 3 OS guides (windows, linux, darwin), got %d", len(report.AllGuides))
	}

	text := FormatReport(report)
	if !strings.Contains(text, "24/7 Host Readiness Check") {
		t.Errorf("unexpected FormatReport output: %s", text)
	}
}
