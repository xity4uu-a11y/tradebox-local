package power

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ReadinessCheck captures one 24/7 hosting check item.
type ReadinessCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | warn
	Detail string `json:"detail"`
}

// PowerGuide provides safe, non-destructive keep-awake commands for running 24/7 on a laptop or desktop.
type PowerGuide struct {
	Platform        string   `json:"platform"`
	Title           string   `json:"title"`
	Notes           string   `json:"notes"`
	KeepAwakeCmds   []string `json:"keep_awake_commands"`
	RestoreDefaults []string `json:"restore_commands"`
}

// HostReport aggregates hardware, environment, readiness checks, and OS-specific keep-awake helpers.
type HostReport struct {
	Hostname      string           `json:"hostname"`
	OS            string           `json:"os"`
	Arch          string           `json:"arch"`
	GoVersion     string           `json:"go_version"`
	CPUCores      int              `json:"cpu_cores"`
	TotalRAMMB    uint64           `json:"total_ram_mb"`
	InContainer   bool             `json:"in_container"`
	WineAvailable bool             `json:"wine_available"`
	Ready         bool             `json:"ready"`
	Checks        []ReadinessCheck `json:"checks"`
	PowerGuide    PowerGuide       `json:"power_guide"`
	AllGuides     []PowerGuide     `json:"all_guides"`
}

// InspectHost collects local machine capabilities and generates non-destructive 24/7 host guidance.
func InspectHost() HostReport {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "local-host"
	}

	cores := runtime.NumCPU()
	ramMB := detectTotalRAMMB()
	inContainer := fileExists("/.dockerenv")
	_, wineErr := exec.LookPath("wine")
	wineAvailable := wineErr == nil

	var checks []ReadinessCheck
	ready := true

	if cores >= 2 {
		checks = append(checks, ReadinessCheck{
			Name:   "CPU Cores",
			Status: "pass",
			Detail: fmt.Sprintf("%d logical cores available (2+ recommended for MT5 + Expert Advisors)", cores),
		})
	} else {
		ready = false
		checks = append(checks, ReadinessCheck{
			Name:   "CPU Cores",
			Status: "warn",
			Detail: fmt.Sprintf("%d logical core detected; 2+ cores recommended to avoid chart tick lag", cores),
		})
	}

	switch {
	case ramMB >= 4096:
		checks = append(checks, ReadinessCheck{
			Name:   "System Memory",
			Status: "pass",
			Detail: fmt.Sprintf("%d MB RAM detected (4096+ MB recommended for 24/7 MT5 host)", ramMB),
		})
	case ramMB > 0:
		checks = append(checks, ReadinessCheck{
			Name:   "System Memory",
			Status: "warn",
			Detail: fmt.Sprintf("%d MB RAM detected; 2048-4096 MB works for 1 MT5 instance with few charts, 4096+ MB preferred", ramMB),
		})
	default:
		checks = append(checks, ReadinessCheck{
			Name:   "System Memory",
			Status: "warn",
			Detail: "Unable to probe total RAM automatically; verify at least 2 GB free memory",
		})
	}

	if runtime.GOOS == "windows" {
		checks = append(checks, ReadinessCheck{
			Name:   "MT5 Runtime",
			Status: "pass",
			Detail: "Native Windows host: run terminal64.exe /portable directly with zero virtualization overhead",
		})
	} else if wineAvailable || inContainer {
		checks = append(checks, ReadinessCheck{
			Name:   "MT5 Runtime",
			Status: "pass",
			Detail: fmt.Sprintf("Unix/Linux host (%s): Wine/container runtime available for terminal64.exe", runtime.GOOS),
		})
	} else {
		checks = append(checks, ReadinessCheck{
			Name:   "MT5 Runtime",
			Status: "warn",
			Detail: "Wine not found in PATH; install Wine64 or run the bundled docker-compose Wine+noVNC stack",
		})
	}

	checks = append(checks, ReadinessCheck{
		Name:   "Remote Access Hygiene",
		Status: "pass",
		Detail: "Bind dashboard (127.0.0.1:8092) and noVNC (127.0.0.1:6080) to loopback and access remotely via Tailscale or Cloudflare Tunnel",
	})

	guides := []PowerGuide{
		windowsPowerGuide(),
		linuxPowerGuide(),
		macOSPowerGuide(),
	}

	activeGuide := guides[0]
	switch runtime.GOOS {
	case "linux":
		activeGuide = guides[1]
	case "darwin":
		activeGuide = guides[2]
	default:
		activeGuide = guides[0]
	}

	return HostReport{
		Hostname:      hostname,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		GoVersion:     runtime.Version(),
		CPUCores:      cores,
		TotalRAMMB:    ramMB,
		InContainer:   inContainer,
		WineAvailable: wineAvailable,
		Ready:         ready,
		Checks:        checks,
		PowerGuide:    activeGuide,
		AllGuides:     guides,
	}
}

func windowsPowerGuide() PowerGuide {
	return PowerGuide{
		Platform: "windows",
		Title:    "Windows Laptop 24/7 Lid-Closed AC Profile (powercfg)",
		Notes:    "Run in an elevated PowerShell window to prevent sleep and keep MT5 active with the laptop lid closed while plugged into AC power. Battery behavior remains unchanged.",
		KeepAwakeCmds: []string{
			"powercfg /change standby-timeout-ac 0",
			"powercfg /change hibernate-timeout-ac 0",
			"powercfg /setacvalueindex SCHEME_CURRENT SUB_BUTTONS LIDACTION 0",
			"powercfg /setactive SCHEME_CURRENT",
		},
		RestoreDefaults: []string{
			"powercfg /change standby-timeout-ac 30",
			"powercfg /setacvalueindex SCHEME_CURRENT SUB_BUTTONS LIDACTION 1",
			"powercfg /setactive SCHEME_CURRENT",
		},
	}
}

func linuxPowerGuide() PowerGuide {
	return PowerGuide{
		Platform: "linux",
		Title:    "Linux Laptop / Mini-PC 24/7 Session Guard (systemd-inhibit)",
		Notes:    "Wraps tradebox in a scoped systemd inhibitor lock so the host ignores idle sleep and lid-close events only while tradebox is running.",
		KeepAwakeCmds: []string{
			`systemd-inhibit --what=idle:sleep:handle-lid-switch --who=tradebox --why="24/7 MT5 Watchdog" ./tradebox serve --config tradebox.json`,
		},
		RestoreDefaults: []string{
			"# Stop tradebox (Ctrl+C or systemctl stop tradebox) to release the inhibitor lock automatically.",
		},
	}
}

func macOSPowerGuide() PowerGuide {
	return PowerGuide{
		Platform: "darwin",
		Title:    "macOS 24/7 Awake Wrapper (caffeinate)",
		Notes:    "Keeps the Mac awake while plugged into AC power for the lifetime of the tradebox process without permanently altering System Settings.",
		KeepAwakeCmds: []string{
			"caffeinate -dims -- ./tradebox serve --config tradebox.json",
		},
		RestoreDefaults: []string{
			"# Stop tradebox (Ctrl+C) to restore normal macOS sleep behavior immediately.",
		},
	}
}

// FormatReport renders a clean CLI summary of the host inspection and keep-awake commands.
func FormatReport(r HostReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "TradeBox Local - 24/7 Host Readiness Check\n")
	fmt.Fprintf(&b, "Host: %s | OS: %s/%s | CPU: %d cores | RAM: %d MB | Container: %t\n\n",
		r.Hostname, r.OS, r.Arch, r.CPUCores, r.TotalRAMMB, r.InContainer)

	for _, c := range r.Checks {
		fmt.Fprintf(&b, "[%s] %s: %s\n", strings.ToUpper(c.Status), c.Name, c.Detail)
	}

	fmt.Fprintf(&b, "\nRecommended Keep-Awake Commands (%s):\n", r.PowerGuide.Title)
	fmt.Fprintf(&b, "Note: %s\n", r.PowerGuide.Notes)
	for _, cmd := range r.PowerGuide.KeepAwakeCmds {
		fmt.Fprintf(&b, "  %s\n", cmd)
	}
	fmt.Fprintf(&b, "\nRevert / Restore Commands:\n")
	for _, cmd := range r.PowerGuide.RestoreDefaults {
		fmt.Fprintf(&b, "  %s\n", cmd)
	}
	return b.String()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
