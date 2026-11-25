package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultConfigSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tradebox.json")

	def := DefaultConfig()
	if err := Save(path, def); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.HTTPAddr != def.HTTPAddr {
		t.Errorf("expected HTTPAddr %q, got %q", def.HTTPAddr, loaded.HTTPAddr)
	}
	if len(loaded.Brokers) != len(def.Brokers) {
		t.Errorf("expected %d brokers, got %d", len(def.Brokers), len(loaded.Brokers))
	}
	if loaded.RestartPolicy.MaxRestartsPerHour != 12 {
		t.Errorf("expected max_restarts_per_hour 12, got %d", loaded.RestartPolicy.MaxRestartsPerHour)
	}
}

func TestConfigEnvExpansionAndValidation(t *testing.T) {
	t.Setenv("TEST_TB_TG_TOKEN", "bot-token-value")
	t.Setenv("TEST_TB_TG_CHAT", "987654321")

	dir := t.TempDir()
	path := filepath.Join(dir, "tradebox.json")

	cfg := DefaultConfig()
	cfg.Alerts.Telegram.Enabled = true
	cfg.Alerts.Telegram.BotToken = "${TEST_TB_TG_TOKEN}"
	cfg.Alerts.Telegram.ChatID = "${TEST_TB_TG_CHAT}"

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Alerts.Telegram.BotToken != "bot-token-value" {
		t.Errorf("expected expanded bot token, got %q", loaded.Alerts.Telegram.BotToken)
	}
	if loaded.Alerts.Telegram.ChatID != "987654321" {
		t.Errorf("expected expanded chat id, got %q", loaded.Alerts.Telegram.ChatID)
	}
}

func TestConfigRejectsInvalidBrokerPort(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Brokers = []BrokerTarget{
		{Name: "BadBroker", Host: "127.0.0.1", Port: 70000, MaxLatencyMs: 50},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for out-of-range port, got nil")
	}
}
