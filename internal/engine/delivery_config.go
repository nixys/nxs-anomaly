package engine

import (
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nixys/nxs-anomaly/internal/store"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// blockPrivateWebhooks reports whether outbound delivery must refuse private,
// loopback and link-local hosts (SSRF guard). On by default in the production
// profile; an explicit env value overrides.
func blockPrivateWebhooks(prod bool) bool {
	if v := os.Getenv("NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS"); v != "" {
		return v == "true"
	}
	return prod
}

// SMTPConfig holds outbound SMTP settings.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	Sender   string
	From     string
	UseTLS   bool
	// Dial, when set, opens the SMTP connection through the configured SOCKS5
	// proxy instead of dialling the server directly. See delivery_proxy.go.
	Dial func(network, addr string) (net.Conn, error)
}

// DeliveryConfig holds all adapter credentials loaded once at startup.
type DeliveryConfig struct {
	TelegramToken string
	// MobilePushURL is the operator-run relay that turns a notification into an
	// actual push. Empty means this deployment has no mobile transport, and
	// mobile notifications are skipped rather than reported as delivered.
	MobilePushURL         string
	MobilePushToken       string // optional bearer token for the relay
	SMTP                  SMTPConfig
	AsteriskInstances     []asteriskInstance
	MaxRetries            int
	RetryDelays           []int
	WebhookTimeoutSeconds int
	// DeliveryConcurrency bounds how many notification provider calls run in
	// parallel per delivery/retry cycle. Default 8; <1 means sequential.
	DeliveryConcurrency int
	// WorkerCycleTimeout bounds a single RunWorkerCycle. 0 (default) disables it,
	// keeping the previous behavior of letting a cycle run to completion — a large
	// legitimate delivery backlog can exceed any fixed bound, so this is opt-in.
	WorkerCycleTimeout time.Duration
	HTTPClient         *http.Client
	// NotifyOnResolve tells the people a group woke that it is over. Off by
	// default: it is a new class of message, and doubling what a responder
	// receives at night is not something an upgrade should decide for them.
	NotifyOnResolve bool
	// PublicURL is where this deployment answers from the outside. Mattermost
	// buttons post back to an absolute URL, so without it they cannot be
	// rendered — a button pointing nowhere is worse than none.
	PublicURL string
	// MattermostActionSecret authenticates those callbacks. Mattermost does not
	// sign them, so the secret travelling in the button's context is the only
	// credential the request carries.
	MattermostActionSecret string
	DeadLetterWebhookURL   string
	// BlockPrivateWebhooks, when true, rejects outbound webhook/issue requests
	// whose host resolves to a private, loopback, or link-local address (SSRF guard).
	BlockPrivateWebhooks bool
	// CircuitBreakerThreshold is the number of consecutive failures per
	// channel+target before delivery to it is short-circuited. 0 (default) disables
	// the breaker. CircuitBreakerCooldown is how long it stays open before a trial.
	CircuitBreakerThreshold int
	CircuitBreakerCooldown  time.Duration
	// ClaimTimeout is how long a notification may sit in a transient claim status
	// ('delivering'/'retrying') before the reaper assumes the owning worker died
	// and returns it to its pending status. Must exceed the worst-case cycle time.
	ClaimTimeout time.Duration
	// Retention bounds how long each category of stored data lives, and Channels
	// says which outbound channels this installation permits and where they may
	// send. Both are installation-wide compliance decisions rather than delivery
	// tuning; they live here because this is the struct loaded once at startup.
	// See data_policy.go and docs/DATA_INVENTORY.md.
	Retention RetentionPolicy
	Channels  ChannelPolicy
	// proxies is the outbound proxy configuration, and httpClients the per-channel
	// clients built from it. A channel with no entry falls back to HTTPClient, so a
	// deployment that configures no proxy behaves exactly as before.
	// See delivery_proxy.go.
	proxies     proxySettings
	httpClients map[string]*http.Client
}

// clientFor returns the HTTP client a channel's provider calls must go through:
// the proxied one where the operator configured a proxy, the shared direct
// client everywhere else.
func (cfg DeliveryConfig) clientFor(channel string) *http.Client {
	if c := cfg.httpClients[channel]; c != nil {
		return c
	}
	return cfg.HTTPClient
}

// chatopsProxyChannel picks which channel's proxy a ChatOps message goes
// through.
//
// A ChatOps channel posts to its platform's own host — api.telegram.org for a
// Telegram channel — so an operator who proxies Telegram because it is blocked
// means that message too, and they will not think to also set the ChatOps
// variable. The platform's proxy therefore wins, unless the operator named the
// ChatOps channel explicitly: an explicit setting (including "direct") is an
// answer to this exact question and outranks the inference.
func (cfg DeliveryConfig) chatopsProxyChannel(platform string) string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if !cfg.proxies.explicit["chatops"] && cfg.proxied(platform) {
		return platform
	}
	return "chatops"
}

// proxied reports whether this channel's transport goes through a proxy.
func (cfg DeliveryConfig) proxied(channel string) bool {
	_, ok := cfg.proxies.byChannel[channel]
	return ok
}

// blockPrivateFor decides whether the pre-flight SSRF check applies to a
// channel's destination URL.
//
// It is off for a proxied channel, and that is not a weakening of the guard so
// much as an acknowledgement of who resolves the name. The pre-flight resolves
// the destination locally and refuses private answers; through a proxy this
// process never resolves or dials the destination at all, so the local answer
// says nothing about where the request goes — and in the networks people deploy
// a proxy for, the local answer is commonly no answer, which would turn the
// guard into "every notification fails". The destination is still judged by the
// egress allowlist, on the configured URL and on every redirect hop. See
// newProxiedDeliveryClient.
func (cfg DeliveryConfig) blockPrivateFor(channel string) bool {
	return cfg.BlockPrivateWebhooks && !cfg.proxied(channel)
}

// ChannelPolicy exposes the outbound-channel policy to callers outside the
// engine (the API layer refuses to configure a blocked channel, the readiness
// report lists what still points at one).
func (e *Engine) ChannelPolicy() ChannelPolicy { return e.deliveryCfg.Channels }

// PublicURL is where this deployment answers from the outside, for callers that
// build a link or a callback out of it. Empty when it was never configured, and
// every caller treats that as "offer no link" rather than guessing one.
func (e *Engine) PublicURL() string { return e.deliveryCfg.PublicURL }

// RetentionPolicy exposes the configured retention horizons.
func (e *Engine) RetentionPolicy() RetentionPolicy { return e.deliveryCfg.Retention }

// MobilePushConfigured reports whether this deployment has a push relay, so
// callers outside the engine can answer "is mobile a real channel here" without
// reaching into the delivery config.
func (e *Engine) MobilePushConfigured() bool { return e.deliveryCfg.MobilePushURL != "" }

// DeliveryConfigFromEnv reads delivery adapter settings from environment variables.
func DeliveryConfigFromEnv() DeliveryConfig {
	smtpPort := 465
	if v := os.Getenv("NXS_ANOMALY_SMTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			smtpPort = n
		}
	}
	smtpSender := os.Getenv("NXS_ANOMALY_SMTP_SENDER")
	if smtpSender == "" {
		smtpSender = os.Getenv("NXS_ANOMALY_SMTP_USERNAME")
	}
	smtpFrom := os.Getenv("NXS_ANOMALY_SMTP_FROM")
	if smtpFrom == "" {
		smtpFrom = "Nixys Alerter"
	}

	delays := []int{1, 5}
	if raw := strings.TrimSpace(os.Getenv("NXS_ANOMALY_NOTIFICATION_RETRY_DELAYS")); raw != "" {
		var parsed []int
		for _, part := range strings.Split(raw, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				parsed = append(parsed, n)
			}
		}
		if len(parsed) > 0 {
			delays = parsed
		}
	}
	maxRetries := len(delays) + 1
	if v := os.Getenv("NXS_ANOMALY_NOTIFICATION_MAX_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxRetries = n
		}
	}

	timeoutSec := 5
	if v := os.Getenv("NXS_ANOMALY_WEBHOOK_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeoutSec = n
		}
	}
	deliveryConcurrency := 8
	if v := os.Getenv("NXS_ANOMALY_NOTIFICATION_DELIVERY_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			deliveryConcurrency = n
		}
	}

	var cycleTimeout time.Duration
	if v := os.Getenv("NXS_ANOMALY_WORKER_CYCLE_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cycleTimeout = time.Duration(n) * time.Second
		}
	}

	// The production profile turns the delivery circuit breaker and the SSRF
	// guard on by default; an explicit env value still overrides either.
	prod := utils.ProductionProfile()
	// Resolved before the client is built: the guard is enforced by the client's
	// own transport, so it has to be known at construction time rather than only
	// at call time. See newDeliveryHTTPClient.
	blockPrivate := blockPrivateWebhooks(prod)
	breakerThreshold := 0
	if prod {
		breakerThreshold = 5
	}
	if v := os.Getenv("NXS_ANOMALY_CIRCUIT_BREAKER_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			breakerThreshold = n
		}
	}
	breakerCooldown := 30 * time.Second
	if v := os.Getenv("NXS_ANOMALY_CIRCUIT_BREAKER_COOLDOWN_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			breakerCooldown = time.Duration(n) * time.Second
		}
	}

	// Default ClaimTimeout is derived from the worst-case delivery-stage duration
	// so the reaper never reclaims work a live worker is still doing (which would
	// double-deliver). An explicit env value still wins.
	channels := channelPolicyFromEnv()

	claimTimeout := defaultClaimTimeout(timeoutSec, deliveryConcurrency)
	if v := os.Getenv("NXS_ANOMALY_NOTIFICATION_CLAIM_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			claimTimeout = time.Duration(n) * time.Second
		}
	}

	// Outbound proxying. Resolved before the clients are built: which proxy a
	// channel goes through is a property of its transport, not of the call.
	proxies := proxySettingsFromEnv()
	timeout := time.Duration(timeoutSec) * time.Second

	return DeliveryConfig{
		TelegramToken:   os.Getenv("NXS_ANOMALY_TELEGRAM_BOT_TOKEN"),
		MobilePushURL:   strings.TrimSpace(os.Getenv("NXS_ANOMALY_MOBILE_PUSH_URL")),
		MobilePushToken: os.Getenv("NXS_ANOMALY_MOBILE_PUSH_TOKEN"),
		SMTP: SMTPConfig{
			Host:     os.Getenv("NXS_ANOMALY_SMTP_HOST"),
			Port:     smtpPort,
			Username: os.Getenv("NXS_ANOMALY_SMTP_USERNAME"),
			Password: os.Getenv("NXS_ANOMALY_SMTP_PASSWORD"),
			Sender:   smtpSender,
			From:     smtpFrom,
			UseTLS:   os.Getenv("NXS_ANOMALY_SMTP_USE_TLS") != "false",
			Dial:     proxies.dialFunc("email", timeout),
		},
		AsteriskInstances:       loadAsteriskInstances(proxies.dialFunc("call", timeout)),
		MaxRetries:              maxRetries,
		RetryDelays:             delays,
		WebhookTimeoutSeconds:   timeoutSec,
		DeliveryConcurrency:     deliveryConcurrency,
		WorkerCycleTimeout:      cycleTimeout,
		HTTPClient:              newDeliveryHTTPClient(timeout, blockPrivate, channels),
		NotifyOnResolve:         os.Getenv("NXS_ANOMALY_NOTIFY_ON_RESOLVE") == "true",
		PublicURL:               strings.TrimSpace(os.Getenv("NXS_ANOMALY_PUBLIC_URL")),
		MattermostActionSecret:  strings.TrimSpace(os.Getenv("NXS_ANOMALY_MATTERMOST_ACTION_SECRET")),
		DeadLetterWebhookURL:    os.Getenv("NXS_ANOMALY_DEAD_LETTER_WEBHOOK_URL"),
		BlockPrivateWebhooks:    blockPrivate,
		CircuitBreakerThreshold: breakerThreshold,
		CircuitBreakerCooldown:  breakerCooldown,
		ClaimTimeout:            claimTimeout,
		Retention:               retentionPolicyFromEnv(),
		Channels:                channels,
		proxies:                 proxies,
		httpClients:             newDeliveryClients(timeout, blockedIPPolicy(blockPrivate), channels, proxies),
	}
}

// blockedIPPolicy turns the SSRF flag into the dial-time blocklist, or nil when
// the guard is off.
func blockedIPPolicy(block bool) func(net.IP) bool {
	if !block {
		return nil
	}
	return isBlockedIP
}

// worstCaseDeliveryStageSeconds estimates the longest a delivery/retry stage can
// run before its save commits: a full claim batch (store.WorkerBatchLimit), each
// provider call up to WebhookTimeout, bounded by DeliveryConcurrency. A
// notification stays in its transient claim ('delivering'/'retrying') for roughly
// this long, so ClaimTimeout must exceed it — otherwise another worker's reaper
// reclaims work still in progress and double-delivers it.
func worstCaseDeliveryStageSeconds(webhookTimeout, concurrency int) float64 {
	if webhookTimeout < 1 {
		webhookTimeout = 1
	}
	if concurrency < 1 {
		concurrency = 1
	}
	return float64(store.WorkerBatchLimit) * float64(webhookTimeout) / float64(concurrency)
}

// defaultClaimTimeout derives a safe ClaimTimeout: twice the worst-case stage
// duration (margin for scheduling jitter), floored at 5 minutes.
func defaultClaimTimeout(webhookTimeout, concurrency int) time.Duration {
	secs := int(math.Ceil(worstCaseDeliveryStageSeconds(webhookTimeout, concurrency) * 2))
	if d := time.Duration(secs) * time.Second; d > 5*time.Minute {
		return d
	}
	return 5 * time.Minute
}

// warnClaimTimeoutRisk logs a startup warning when ClaimTimeout is too small to
// be safe. A notification is held in a transient claim status
// ('delivering'/'retrying') for as long as its stage runs (claimed at the start
// of the delivery stage, finalized when the save commits). If ClaimTimeout is
// shorter, another worker's reaper reclaims a notification a live worker is still
// delivering → duplicate to the recipient. Two independent checks:
//   - against the worst-case stage duration (a real risk even on default config
//     under a full delivery backlog);
//   - against WorkerCycleTimeout, when that explicit bound is set.
func (cfg DeliveryConfig) warnClaimTimeoutRisk() {
	if cfg.ClaimTimeout <= 0 {
		return // reaper disabled — nothing to reclaim
	}
	worst := time.Duration(math.Ceil(worstCaseDeliveryStageSeconds(cfg.WebhookTimeoutSeconds, cfg.DeliveryConcurrency))) * time.Second
	if cfg.ClaimTimeout <= worst {
		slog.Warn("claim_timeout_below_worst_case_stage",
			"claim_timeout", cfg.ClaimTimeout.String(),
			"worst_case_stage", worst.String(),
			"risk", "under a full delivery backlog the reaper may reclaim a delivery still in progress, causing a duplicate",
			"fix", "raise NXS_ANOMALY_NOTIFICATION_CLAIM_TIMEOUT_SECONDS above the worst-case stage",
		)
	}
	if cfg.WorkerCycleTimeout > 0 && cfg.ClaimTimeout <= cfg.WorkerCycleTimeout {
		slog.Warn("claim_timeout_too_small",
			"claim_timeout", cfg.ClaimTimeout.String(),
			"worker_cycle_timeout", cfg.WorkerCycleTimeout.String(),
			"risk", "reaper may reclaim a notification still being delivered, causing a duplicate",
			"fix", "set NXS_ANOMALY_NOTIFICATION_CLAIM_TIMEOUT_SECONDS greater than NXS_ANOMALY_WORKER_CYCLE_TIMEOUT_SECONDS",
		)
	}
}

// loadAsteriskInstances reads Asterisk AMI settings from env at startup. dial is
// the SOCKS5 dialler for the call channel, or nil for a direct connection.
func loadAsteriskInstances(dial func(network, addr string) (net.Conn, error)) []asteriskInstance {
	var instances []asteriskInstance
	for i := 0; ; i++ {
		prefix := fmt.Sprintf("NXS_ANOMALY_ASTERISK_%d_", i)
		host := os.Getenv(prefix + "HOST")
		if host == "" {
			break
		}
		instances = append(instances, asteriskInstance{
			host:         host,
			username:     os.Getenv(prefix + "USERNAME"),
			secret:       os.Getenv(prefix + "SECRET"),
			channel:      os.Getenv(prefix + "CHANNEL"),
			context:      os.Getenv(prefix + "CONTEXT"),
			exten:        os.Getenv(prefix + "EXTEN"),
			priority:     strDefault(os.Getenv(prefix+"PRIORITY"), "1"),
			callerID:     strDefault(os.Getenv(prefix+"CALLER_ID"), "nxs-anomaly"),
			variableName: strDefault(os.Getenv(prefix+"TRIGGER_VARIABLE"), "TRIGGER_MESSAGE"),
		})
	}
	if len(instances) == 0 {
		if host := os.Getenv("NXS_ANOMALY_ASTERISK_HOST"); host != "" {
			instances = append(instances, asteriskInstance{
				host:         host,
				username:     os.Getenv("NXS_ANOMALY_ASTERISK_USERNAME"),
				secret:       os.Getenv("NXS_ANOMALY_ASTERISK_SECRET"),
				channel:      os.Getenv("NXS_ANOMALY_ASTERISK_CHANNEL"),
				context:      os.Getenv("NXS_ANOMALY_ASTERISK_CONTEXT"),
				exten:        os.Getenv("NXS_ANOMALY_ASTERISK_EXTEN"),
				priority:     strDefault(os.Getenv("NXS_ANOMALY_ASTERISK_PRIORITY"), "1"),
				callerID:     strDefault(os.Getenv("NXS_ANOMALY_ASTERISK_CALLER_ID"), "nxs-anomaly"),
				variableName: strDefault(os.Getenv("NXS_ANOMALY_ASTERISK_TRIGGER_VARIABLE"), "TRIGGER_MESSAGE"),
			})
		}
	}
	for i := range instances {
		instances[i].dial = dial
	}
	return instances
}
