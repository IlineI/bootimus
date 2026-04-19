// Package webhook provides fire-and-forget outbound HTTP POST notifications
// for bootimus lifecycle events. The config (URL, enabled, per-event toggles)
// lives in the WebhookConfig singleton; this package reads it on every fire
// so config changes take effect without restart.
//
// Failures log but don't affect the calling path — webhooks are advisory.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"bootimus/internal/models"
	"bootimus/internal/storage"
)

const (
	EventBootStarted       = "boot.started"
	EventClientDiscovered  = "client.discovered"
	EventInventoryUpdated  = "client.inventory_updated"
)

// Event is the JSON shape POSTed to the webhook URL.
type Event struct {
	Event      string            `json:"event"`
	Timestamp  time.Time         `json:"timestamp"`
	MAC        string            `json:"mac,omitempty"`
	ClientName string            `json:"client_name,omitempty"`
	Image      string            `json:"image,omitempty"`
	IP         string            `json:"ip,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Notifier is the tiny surface bootimus uses to raise events. Obtained from
// New(store). All Fire calls are non-blocking.
type Notifier struct {
	store  storage.Storage
	client *http.Client
}

func New(store storage.Storage) *Notifier {
	return &Notifier{
		store:  store,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Fire sends the event to the configured webhook if enabled and the specific
// event type is turned on. Runs on a goroutine so callers aren't blocked.
func (n *Notifier) Fire(ev Event) {
	if n == nil || n.store == nil {
		return
	}
	cfg, err := n.store.GetWebhookConfig()
	if err != nil || cfg == nil || !cfg.Enabled || cfg.URL == "" {
		return
	}
	if !eventEnabled(cfg, ev.Event) {
		return
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	go n.deliver(cfg.URL, ev)
}

func eventEnabled(cfg *models.WebhookConfig, event string) bool {
	switch event {
	case EventBootStarted:
		return cfg.OnBootStarted
	case EventClientDiscovered:
		return cfg.OnClientDiscovered
	case EventInventoryUpdated:
		return cfg.OnInventoryUpdated
	}
	return false
}

func (n *Notifier) deliver(url string, ev Event) {
	body, err := json.Marshal(ev)
	if err != nil {
		log.Printf("webhook: marshal failed: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		log.Printf("webhook: build request failed: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "bootimus-webhook/1")
	resp, err := n.client.Do(req)
	if err != nil {
		log.Printf("webhook: POST %s failed: %v", url, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("webhook: POST %s returned HTTP %d", url, resp.StatusCode)
		return
	}
	log.Printf("webhook: delivered %s to %s", ev.Event, url)
}
