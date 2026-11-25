package alerts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xity4uu-a11y/tradebox-local/internal/config"
)

func TestDispatcherAllChannelsHMACAndCooldown(t *testing.T) {
	const webhookSecret = "unit-test-hmac-secret-value"

	var mu sync.Mutex
	hits := map[string]int{}
	var tgText string
	var discordEmbeds int
	var slackBlocks int
	var sigValid bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		defer mu.Unlock()

		switch {
		case strings.HasPrefix(r.URL.Path, "/bot") && strings.HasSuffix(r.URL.Path, "/sendMessage"):
			hits["telegram"]++
			var p map[string]any
			_ = json.Unmarshal(body, &p)
			if txt, ok := p["text"].(string); ok {
				tgText = txt
			}
		case r.URL.Path == "/discord":
			hits["discord"]++
			var p map[string]any
			_ = json.Unmarshal(body, &p)
			if embeds, ok := p["embeds"].([]any); ok {
				discordEmbeds = len(embeds)
			}
		case r.URL.Path == "/slack":
			hits["slack"]++
			var p map[string]any
			_ = json.Unmarshal(body, &p)
			if blocks, ok := p["blocks"].([]any); ok {
				slackBlocks = len(blocks)
			}
		case r.URL.Path == "/webhook":
			hits["webhook"]++
			sig := r.Header.Get("X-TradeBox-Signature")
			sigValid = VerifySignature(webhookSecret, body, sig)
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	cfg := config.AlertsConfig{
		CooldownSec: 60,
		Telegram: config.TelegramConfig{
			Enabled:    true,
			BotToken:   "mock-telegram-bot-token",
			ChatID:     "-100999888",
			APIBaseURL: srv.URL,
		},
		Discord: config.DiscordConfig{
			Enabled:    true,
			WebhookURL: srv.URL + "/discord",
		},
		Slack: config.SlackConfig{
			Enabled:    true,
			WebhookURL: srv.URL + "/slack",
		},
		Webhook: config.WebhookConfig{
			Enabled: true,
			URL:     srv.URL + "/webhook",
			Secret:  webhookSecret,
		},
	}

	d := NewDispatcher(cfg, srv.Client())
	ctx := context.Background()

	alrt := Alert{
		RuleKey:  "journal:mt5_error_4756",
		Severity: "critical",
		Category: "journal",
		Title:    "MT5 OrderSend Error 4756",
		Message:  "TrendBreakoutEA failed to dispatch EURUSD buy order",
		Source:   "MQL5/Logs/20261125.log",
		Metadata: map[string]string{"symbol": "EURUSD"},
	}

	rec1 := d.Dispatch(ctx, alrt)
	if rec1.Status != "sent" {
		t.Fatalf("expected status sent, got %s (%+v)", rec1.Status, rec1.Channels)
	}
	if len(rec1.Channels) != 4 {
		t.Fatalf("expected 4 channel results, got %d", len(rec1.Channels))
	}

	// Second identical RuleKey within cooldown must be throttled without sending HTTP requests.
	rec2 := d.Dispatch(ctx, alrt)
	if rec2.Status != "throttled" {
		t.Fatalf("expected second alert within cooldown to be throttled, got %s", rec2.Status)
	}

	// Manual test alert with BypassCooldown must still dispatch.
	alrtBypass := alrt
	alrtBypass.BypassCooldown = true
	rec3 := d.Dispatch(ctx, alrtBypass)
	if rec3.Status != "sent" {
		t.Fatalf("expected BypassCooldown alert to send, got %s", rec3.Status)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, ch := range []string{"telegram", "discord", "slack", "webhook"} {
		if hits[ch] != 2 {
			t.Errorf("expected exactly 2 HTTP deliveries for %s (1 initial + 1 bypass, 1 throttled), got %d", ch, hits[ch])
		}
	}
	if !strings.Contains(tgText, "[CRITICAL] MT5 OrderSend Error 4756") {
		t.Errorf("unexpected Telegram text: %q", tgText)
	}
	if discordEmbeds != 1 {
		t.Errorf("expected 1 Discord embed, got %d", discordEmbeds)
	}
	if slackBlocks < 2 {
		t.Errorf("expected Slack Block Kit blocks, got %d", slackBlocks)
	}
	if !sigValid {
		t.Error("expected valid HMAC-SHA256 X-TradeBox-Signature header")
	}

	hist := d.History(10)
	if len(hist) != 3 {
		t.Errorf("expected 3 history records, got %d", len(hist))
	}
}
