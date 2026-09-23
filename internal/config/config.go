// Package config defines the roled configuration schema and its loader and
// validator. Config is read once at startup; restart to apply changes.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// Channel types.
const (
	ChannelConsole = "console"
	ChannelWebhook = "webhook"
	ChannelEmail   = "email"
	ChannelSMSGate = "smsgate"
)

// Alert triggers. TriggerConsecutive alerts after failure_threshold consecutive
// failures; TriggerAvailability alerts when rolling-window availability drops
// below min_availability.
const (
	TriggerConsecutive  = "consecutive"
	TriggerAvailability = "availability"
)

// defaultWindowSize is the rolling-window length applied to any unit that omits
// window_size, so an availability rate is always calculated and published in
// the status snapshot regardless of trigger mode.
const defaultWindowSize = 20

// defaultSummaryIntervalS is the aggregated-summary cadence (15 minutes) used
// when summary_interval_s is omitted.
const defaultSummaryIntervalS = 900

// Retry configures the dispatcher's retry-with-backoff behavior.
type Retry struct {
	MaxAttempts int `json:"max_attempts"`
	BaseDelayMS int `json:"base_delay_ms"`
}

// Channel is a single notification channel definition. Fields are used
// according to Type.
type Channel struct {
	Type string `json:"type"`

	// webhook
	URL string `json:"url,omitempty"`

	// email
	SMTPHost string   `json:"smtp_host,omitempty"`
	Port     int      `json:"port,omitempty"`
	From     string   `json:"from,omitempty"`
	To       []string `json:"to,omitempty"`

	// smsgate
	BaseURL    string   `json:"base_url,omitempty"`
	Recipients []string `json:"recipients,omitempty"`

	// email + smsgate shared
	Username    string `json:"username,omitempty"`
	PasswordEnv string `json:"password_env,omitempty"`

	// webhook + smsgate shared
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

// PongServer is the pong_server role config.
type PongServer struct {
	Enabled       bool   `json:"enabled"`
	Listen        string `json:"listen"`
	ReadTimeoutMS int    `json:"read_timeout_ms"`
}

// PingTarget is a single independent ping monitor within ping_monitor.
type PingTarget struct {
	Target           string   `json:"target"`
	IntervalS        int      `json:"interval_s"`
	TimeoutMS        int      `json:"timeout_ms"`
	FailureThreshold int      `json:"failure_threshold"`
	Trigger          string   `json:"trigger,omitempty"`
	WindowSize       int      `json:"window_size,omitempty"`
	MinAvailability  float64  `json:"min_availability,omitempty"`
	Notify           []string `json:"notify"`
}

// PingMonitor is the ping_monitor role config. Each entry in Targets is an
// independent monitor with its own interval, timeout, threshold, and channels.
type PingMonitor struct {
	Enabled bool         `json:"enabled"`
	Targets []PingTarget `json:"targets"`
}

// InternetCheck is the internet_check role config.
type InternetCheck struct {
	Enabled          bool     `json:"enabled"`
	Sites            []string `json:"sites"`
	IntervalS        int      `json:"interval_s"`
	TimeoutMS        int      `json:"timeout_ms"`
	FailureThreshold int      `json:"failure_threshold"`
	Trigger          string   `json:"trigger,omitempty"`
	WindowSize       int      `json:"window_size,omitempty"`
	MinAvailability  float64  `json:"min_availability,omitempty"`
	Notify           []string `json:"notify"`
}

// SpeedCheck is the speed_check role config.
type SpeedCheck struct {
	Enabled          bool     `json:"enabled"`
	IntervalS        int      `json:"interval_s"`
	ThresholdMbps    float64  `json:"threshold_mbps"`
	Binary           string   `json:"binary"`
	FailureThreshold int      `json:"failure_threshold"`
	Trigger          string   `json:"trigger,omitempty"`
	WindowSize       int      `json:"window_size,omitempty"`
	MinAvailability  float64  `json:"min_availability,omitempty"`
	Notify           []string `json:"notify"`
}

// Roles groups all role configs.
type Roles struct {
	PongServer    PongServer    `json:"pong_server"`
	PingMonitor   PingMonitor   `json:"ping_monitor"`
	InternetCheck InternetCheck `json:"internet_check"`
	SpeedCheck    SpeedCheck    `json:"speed_check"`
}

// Config is the full daemon configuration.
type Config struct {
	Retry Retry `json:"retry"`
	// SummaryIntervalS is how often, in seconds, an instance running any
	// monitoring role logs an aggregated summary. Optional; defaults to 900
	// (15 minutes).
	SummaryIntervalS int                `json:"summary_interval_s,omitempty"`
	Channels         map[string]Channel `json:"channels"`
	Roles            Roles              `json:"roles"`
}

// Load reads and parses the config file at path, then validates it. A
// non-nil error aborts startup.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// applyDefaults fills in sensible defaults for optional fields.
func (c *Config) applyDefaults() {
	if c.Retry.MaxAttempts == 0 {
		c.Retry.MaxAttempts = 3
	}
	if c.Retry.BaseDelayMS == 0 {
		c.Retry.BaseDelayMS = 500
	}
	if c.SummaryIntervalS == 0 {
		c.SummaryIntervalS = defaultSummaryIntervalS
	}
	if c.Roles.SpeedCheck.Binary == "" {
		c.Roles.SpeedCheck.Binary = "speedtest"
	}
	// Default per-request timeout for HTTP-based channels so an omitted
	// timeout_ms does not disable the client timeout and stall a role loop.
	for name, ch := range c.Channels {
		if (ch.Type == ChannelWebhook || ch.Type == ChannelSMSGate) && ch.TimeoutMS == 0 {
			ch.TimeoutMS = 5000
			c.Channels[name] = ch
		}
	}
	// failure_threshold defaults to 1 where a role uses the state machine but
	// omits it; validation still enforces >=1 when explicitly set. trigger
	// defaults to consecutive; window_size is defaulted in both modes so the
	// availability rate is always tracked.
	for i := range c.Roles.PingMonitor.Targets {
		t := &c.Roles.PingMonitor.Targets[i]
		if t.FailureThreshold == 0 {
			t.FailureThreshold = 1
		}
		applyTriggerDefaults(&t.Trigger, &t.WindowSize)
	}
	if c.Roles.InternetCheck.FailureThreshold == 0 {
		c.Roles.InternetCheck.FailureThreshold = 1
	}
	applyTriggerDefaults(&c.Roles.InternetCheck.Trigger, &c.Roles.InternetCheck.WindowSize)
	if c.Roles.SpeedCheck.FailureThreshold == 0 {
		c.Roles.SpeedCheck.FailureThreshold = 1
	}
	applyTriggerDefaults(&c.Roles.SpeedCheck.Trigger, &c.Roles.SpeedCheck.WindowSize)
}

// applyTriggerDefaults fills the trigger mode and a default rolling-window size
// when omitted. The window is defaulted in both modes so the availability rate
// is always tracked and published, not only when the availability trigger is
// used.
func applyTriggerDefaults(trigger *string, windowSize *int) {
	if *trigger == "" {
		*trigger = TriggerConsecutive
	}
	if *windowSize == 0 {
		*windowSize = defaultWindowSize
	}
}

// Validate checks required fields for enabled roles and channels, and that
// every referenced channel exists.
func (c *Config) Validate() error {
	if c.Retry.MaxAttempts < 1 {
		return fmt.Errorf("retry.max_attempts must be >= 1")
	}
	if c.Retry.BaseDelayMS < 0 {
		return fmt.Errorf("retry.base_delay_ms must be >= 0")
	}
	if c.SummaryIntervalS <= 0 {
		return fmt.Errorf("summary_interval_s must be > 0")
	}

	if err := c.validateChannels(); err != nil {
		return err
	}
	if err := c.validateRoles(); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateChannels() error {
	for name, ch := range c.Channels {
		switch ch.Type {
		case ChannelConsole:
			// no required fields
		case ChannelWebhook:
			if ch.URL == "" {
				return fmt.Errorf("channel %q (webhook): url is required", name)
			}
			if ch.TimeoutMS <= 0 {
				return fmt.Errorf("channel %q (webhook): timeout_ms must be > 0", name)
			}
		case ChannelEmail:
			if ch.SMTPHost == "" {
				return fmt.Errorf("channel %q (email): smtp_host is required", name)
			}
			if ch.Port <= 0 {
				return fmt.Errorf("channel %q (email): port must be > 0", name)
			}
			if ch.From == "" {
				return fmt.Errorf("channel %q (email): from is required", name)
			}
			if len(ch.To) == 0 {
				return fmt.Errorf("channel %q (email): to must be non-empty", name)
			}
			if ch.PasswordEnv == "" {
				return fmt.Errorf("channel %q (email): password_env is required", name)
			}
		case ChannelSMSGate:
			if ch.BaseURL == "" {
				return fmt.Errorf("channel %q (smsgate): base_url is required", name)
			}
			if ch.Username == "" {
				return fmt.Errorf("channel %q (smsgate): username is required", name)
			}
			if ch.PasswordEnv == "" {
				return fmt.Errorf("channel %q (smsgate): password_env is required", name)
			}
			if len(ch.Recipients) == 0 {
				return fmt.Errorf("channel %q (smsgate): recipients must be non-empty", name)
			}
			if ch.TimeoutMS <= 0 {
				return fmt.Errorf("channel %q (smsgate): timeout_ms must be > 0", name)
			}
		case "":
			return fmt.Errorf("channel %q: type is required", name)
		default:
			return fmt.Errorf("channel %q: unknown type %q", name, ch.Type)
		}
	}
	return nil
}

func (c *Config) validateRoles() error {
	if c.Roles.PongServer.Enabled {
		if c.Roles.PongServer.Listen == "" {
			return fmt.Errorf("role pong_server: listen is required")
		}
		if c.Roles.PongServer.ReadTimeoutMS <= 0 {
			return fmt.Errorf("role pong_server: read_timeout_ms must be > 0")
		}
	}

	if c.Roles.PingMonitor.Enabled {
		if len(c.Roles.PingMonitor.Targets) == 0 {
			return fmt.Errorf("role ping_monitor: targets must be non-empty")
		}
		for i, tgt := range c.Roles.PingMonitor.Targets {
			// Identify the offending target by index and address.
			who := fmt.Sprintf("target %d (%q)", i, tgt.Target)
			if tgt.Target == "" {
				return fmt.Errorf("role ping_monitor %s: target is required", who)
			}
			if tgt.IntervalS <= 0 {
				return fmt.Errorf("role ping_monitor %s: interval_s must be > 0", who)
			}
			if tgt.TimeoutMS <= 0 {
				return fmt.Errorf("role ping_monitor %s: timeout_ms must be > 0", who)
			}
			if tgt.FailureThreshold < 1 {
				return fmt.Errorf("role ping_monitor %s: failure_threshold must be >= 1", who)
			}
			if err := validateTrigger("ping_monitor "+who, tgt.Trigger, tgt.WindowSize, tgt.MinAvailability); err != nil {
				return err
			}
			if err := c.checkNotify("ping_monitor "+who, tgt.Notify); err != nil {
				return err
			}
		}
	}

	if c.Roles.InternetCheck.Enabled {
		r := c.Roles.InternetCheck
		if len(r.Sites) == 0 {
			return fmt.Errorf("role internet_check: sites must be non-empty")
		}
		if r.IntervalS <= 0 {
			return fmt.Errorf("role internet_check: interval_s must be > 0")
		}
		if r.TimeoutMS <= 0 {
			return fmt.Errorf("role internet_check: timeout_ms must be > 0")
		}
		if r.FailureThreshold < 1 {
			return fmt.Errorf("role internet_check: failure_threshold must be >= 1")
		}
		if err := validateTrigger("internet_check", r.Trigger, r.WindowSize, r.MinAvailability); err != nil {
			return err
		}
		if err := c.checkNotify("internet_check", r.Notify); err != nil {
			return err
		}
	}

	if c.Roles.SpeedCheck.Enabled {
		r := c.Roles.SpeedCheck
		if r.ThresholdMbps <= 0 {
			return fmt.Errorf("role speed_check: threshold_mbps must be > 0")
		}
		if r.IntervalS <= 0 {
			return fmt.Errorf("role speed_check: interval_s must be > 0")
		}
		if r.FailureThreshold < 1 {
			return fmt.Errorf("role speed_check: failure_threshold must be >= 1")
		}
		if err := validateTrigger("speed_check", r.Trigger, r.WindowSize, r.MinAvailability); err != nil {
			return err
		}
		if err := c.checkNotify("speed_check", r.Notify); err != nil {
			return err
		}
	}

	return nil
}

// validateTrigger checks the rolling-window/availability fields for one role.
// Defaults are applied before validation, so an empty trigger is treated as
// consecutive here as a safety net.
func validateTrigger(role, trigger string, windowSize int, minAvailability float64) error {
	switch trigger {
	case "", TriggerConsecutive:
		if windowSize < 0 {
			return fmt.Errorf("role %s: window_size must be >= 0", role)
		}
	case TriggerAvailability:
		if windowSize <= 0 {
			return fmt.Errorf("role %s: window_size must be > 0 when trigger is %q", role, TriggerAvailability)
		}
		if minAvailability <= 0 || minAvailability > 100 {
			return fmt.Errorf("role %s: min_availability must be in (0, 100] when trigger is %q", role, TriggerAvailability)
		}
	default:
		return fmt.Errorf("role %s: unknown trigger %q", role, trigger)
	}
	return nil
}

// checkNotify ensures every channel referenced by a role exists.
func (c *Config) checkNotify(role string, notify []string) error {
	for _, name := range notify {
		if _, ok := c.Channels[name]; !ok {
			return fmt.Errorf("role %s references undefined channel %q", role, name)
		}
	}
	return nil
}
