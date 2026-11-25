package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Config holds the full runtime configuration for tradebox-local.
type Config struct {
	TerminalCommand string         `json:"terminal_command"`
	TerminalArgs    []string       `json:"terminal_args"`
	WorkingDir      string         `json:"working_dir"`
	AutoStart       bool           `json:"auto_start"`
	MT5LogsDir      string         `json:"mt5_logs_dir"`
	Brokers         []BrokerTarget `json:"brokers"`
	RestartPolicy   RestartPolicy  `json:"restart_policy"`
	LatencyCheckSec int            `json:"latency_check_sec"`
	JournalPollSec  int            `json:"journal_poll_sec"`
	Alerts          AlertsConfig   `json:"alerts"`
	NoVNCURL        string         `json:"novnc_url"`
	HTTPAddr        string         `json:"http_addr"`
	PluginsDir      string         `json:"plugins_dir"`
}

// BrokerTarget defines a broker TCP endpoint monitored for execution latency.
type BrokerTarget struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	MaxLatencyMs int    `json:"max_latency_ms"`
	TimeoutMs    int    `json:"timeout_ms,omitempty"`
}

// RestartPolicy controls crash recovery, exponential backoff, and circuit breaking.
type RestartPolicy struct {
	MaxRestartsPerHour int `json:"max_restarts_per_hour"`
	BackoffBaseSec     int `json:"backoff_base_sec"`
	BackoffMaxSec      int `json:"backoff_max_sec,omitempty"`
	CheckIntervalSec   int `json:"check_interval_sec"`
	StableResetSec     int `json:"stable_reset_sec,omitempty"`
}

// AlertsConfig defines notification destinations and cooldown throttling.
type AlertsConfig struct {
	CooldownSec int            `json:"cooldown_sec"`
	Telegram    TelegramConfig `json:"telegram"`
	Discord     DiscordConfig  `json:"discord"`
	Slack       SlackConfig    `json:"slack"`
	Webhook     WebhookConfig  `json:"webhook"`
}

// TelegramConfig holds Telegram Bot API settings.
type TelegramConfig struct {
	Enabled    bool   `json:"enabled"`
	BotToken   string `json:"bot_token"`
	ChatID     string `json:"chat_id"`
	APIBaseURL string `json:"api_base_url,omitempty"`
}

// DiscordConfig holds Discord incoming webhook settings.
type DiscordConfig struct {
	Enabled    bool   `json:"enabled"`
	WebhookURL string `json:"webhook_url"`
}

// SlackConfig holds Slack incoming webhook settings.
type SlackConfig struct {
	Enabled    bool   `json:"enabled"`
	WebhookURL string `json:"webhook_url"`
}

// WebhookConfig holds generic HMAC-SHA256 signed webhook settings.
type WebhookConfig struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
	Secret  string `json:"secret"`
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// DefaultConfig returns a ready-to-use configuration tuned for the current OS.
func DefaultConfig() *Config {
	termCmd := "terminal64.exe"
	termArgs := []string{"/portable"}
	workDir := "."
	logsDir := "./mt5-logs"

	if runtime.GOOS != "windows" {
		termCmd = "wine"
		termArgs = []string{"/mt5-data/terminal/terminal64.exe", "/portable"}
		workDir = "."
		logsDir = "./mt5-logs"
	}

	return &Config{
		TerminalCommand: termCmd,
		TerminalArgs:    termArgs,
		WorkingDir:      workDir,
		AutoStart:       false,
		MT5LogsDir:      logsDir,
		Brokers: []BrokerTarget{
			{
				Name:         "MetaQuotes-Demo-EU",
				Host:         "access.metatrader5.com",
				Port:         443,
				MaxLatencyMs: 120,
				TimeoutMs:    3000,
			},
			{
				Name:         "Network-Baseline-CF",
				Host:         "1.1.1.1",
				Port:         443,
				MaxLatencyMs: 80,
				TimeoutMs:    3000,
			},
		},
		RestartPolicy: RestartPolicy{
			MaxRestartsPerHour: 12,
			BackoffBaseSec:     2,
			BackoffMaxSec:      60,
			CheckIntervalSec:   5,
			StableResetSec:     60,
		},
		LatencyCheckSec: 15,
		JournalPollSec:  2,
		Alerts: AlertsConfig{
			CooldownSec: 300,
			Telegram: TelegramConfig{
				Enabled:    false,
				BotToken:   "${TRADEBOX_TELEGRAM_BOT_TOKEN}",
				ChatID:     "${TRADEBOX_TELEGRAM_CHAT_ID}",
				APIBaseURL: "https://api.telegram.org",
			},
			Discord: DiscordConfig{
				Enabled:    false,
				WebhookURL: "${TRADEBOX_DISCORD_WEBHOOK_URL}",
			},
			Slack: SlackConfig{
				Enabled:    false,
				WebhookURL: "${TRADEBOX_SLACK_WEBHOOK_URL}",
			},
			Webhook: WebhookConfig{
				Enabled: false,
				URL:     "${TRADEBOX_WEBHOOK_URL}",
				Secret:  "${TRADEBOX_WEBHOOK_SECRET}",
			},
		},
		NoVNCURL:   "http://127.0.0.1:6080/vnc.html?autoconnect=1&resize=scale",
		HTTPAddr:   "127.0.0.1:8092",
		PluginsDir: "./plugins",
	}
}

// Load reads, normalizes, expands environment variables in, and validates a JSON config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	cfg := DefaultConfig()
	// Clear default brokers before unmarshaling so explicit empty/custom lists replace defaults cleanly.
	cfg.Brokers = nil

	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	cfg.applyDefaults()
	cfg.expandSecrets()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %q: %w", path, err)
	}

	return cfg, nil
}

// Save writes a formatted JSON configuration file to disk, creating parent directories if needed.
func Save(path string, cfg *Config) error {
	if cfg == nil {
		return errors.New("config cannot be nil")
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	return nil
}

func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.HTTPAddr) == "" {
		c.HTTPAddr = "127.0.0.1:8092"
	}
	if strings.TrimSpace(c.WorkingDir) == "" {
		c.WorkingDir = "."
	}
	if strings.TrimSpace(c.PluginsDir) == "" {
		c.PluginsDir = "./plugins"
	}
	if c.RestartPolicy.MaxRestartsPerHour <= 0 {
		c.RestartPolicy.MaxRestartsPerHour = 12
	}
	if c.RestartPolicy.BackoffBaseSec <= 0 {
		c.RestartPolicy.BackoffBaseSec = 1
	}
	if c.RestartPolicy.BackoffMaxSec <= 0 {
		c.RestartPolicy.BackoffMaxSec = 60
	}
	if c.RestartPolicy.CheckIntervalSec <= 0 {
		c.RestartPolicy.CheckIntervalSec = 5
	}
	if c.RestartPolicy.StableResetSec <= 0 {
		c.RestartPolicy.StableResetSec = 60
	}
	if c.LatencyCheckSec <= 0 {
		c.LatencyCheckSec = 15
	}
	if c.JournalPollSec <= 0 {
		c.JournalPollSec = 2
	}
	if c.Alerts.CooldownSec < 0 {
		c.Alerts.CooldownSec = 0
	}
	if strings.TrimSpace(c.Alerts.Telegram.APIBaseURL) == "" {
		c.Alerts.Telegram.APIBaseURL = "https://api.telegram.org"
	}
	for i := range c.Brokers {
		if c.Brokers[i].TimeoutMs <= 0 {
			c.Brokers[i].TimeoutMs = 3000
		}
		if c.Brokers[i].MaxLatencyMs <= 0 {
			c.Brokers[i].MaxLatencyMs = 150
		}
	}
}

func (c *Config) expandSecrets() {
	c.Alerts.Telegram.BotToken = expandEnv(c.Alerts.Telegram.BotToken)
	c.Alerts.Telegram.ChatID = expandEnv(c.Alerts.Telegram.ChatID)
	c.Alerts.Discord.WebhookURL = expandEnv(c.Alerts.Discord.WebhookURL)
	c.Alerts.Slack.WebhookURL = expandEnv(c.Alerts.Slack.WebhookURL)
	c.Alerts.Webhook.URL = expandEnv(c.Alerts.Webhook.URL)
	c.Alerts.Webhook.Secret = expandEnv(c.Alerts.Webhook.Secret)
}

func expandEnv(val string) string {
	return envPattern.ReplaceAllStringFunc(val, func(match string) string {
		sub := envPattern.FindStringSubmatch(match)
		if len(sub) != 2 {
			return match
		}
		if resolved, ok := os.LookupEnv(sub[1]); ok {
			return resolved
		}
		return ""
	})
}

// Validate checks semantic invariants for the configuration.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.TerminalCommand) == "" {
		return errors.New("terminal_command must not be empty")
	}
	if c.RestartPolicy.MaxRestartsPerHour < 1 {
		return errors.New("restart_policy.max_restarts_per_hour must be >= 1")
	}
	if c.RestartPolicy.BackoffBaseSec < 1 {
		return errors.New("restart_policy.backoff_base_sec must be >= 1")
	}
	if c.RestartPolicy.BackoffMaxSec < c.RestartPolicy.BackoffBaseSec {
		return errors.New("restart_policy.backoff_max_sec must be >= backoff_base_sec")
	}
	if c.RestartPolicy.CheckIntervalSec < 1 {
		return errors.New("restart_policy.check_interval_sec must be >= 1")
	}
	for i, b := range c.Brokers {
		if strings.TrimSpace(b.Name) == "" {
			return fmt.Errorf("brokers[%d].name must not be empty", i)
		}
		if strings.TrimSpace(b.Host) == "" {
			return fmt.Errorf("brokers[%d].host must not be empty", i)
		}
		if b.Port < 1 || b.Port > 65535 {
			return fmt.Errorf("brokers[%d].port must be between 1 and 65535", i)
		}
		if b.MaxLatencyMs < 1 {
			return fmt.Errorf("brokers[%d].max_latency_ms must be >= 1", i)
		}
	}
	if c.Alerts.Telegram.Enabled {
		if strings.TrimSpace(c.Alerts.Telegram.BotToken) == "" || strings.TrimSpace(c.Alerts.Telegram.ChatID) == "" {
			return errors.New("alerts.telegram enabled requires non-empty bot_token and chat_id")
		}
	}
	if c.Alerts.Discord.Enabled && strings.TrimSpace(c.Alerts.Discord.WebhookURL) == "" {
		return errors.New("alerts.discord enabled requires non-empty webhook_url")
	}
	if c.Alerts.Slack.Enabled && strings.TrimSpace(c.Alerts.Slack.WebhookURL) == "" {
		return errors.New("alerts.slack enabled requires non-empty webhook_url")
	}
	if c.Alerts.Webhook.Enabled && strings.TrimSpace(c.Alerts.Webhook.URL) == "" {
		return errors.New("alerts.webhook enabled requires non-empty url")
	}
	return nil
}
