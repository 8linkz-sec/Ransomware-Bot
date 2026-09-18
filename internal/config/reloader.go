package config

import "time"

const DefaultReloadInterval = time.Minute

// Reloader owns file-backed config change detection and loading.
type Reloader struct {
	configDir     string
	interval      time.Duration
	lastSignature string
	nextSignature string

	// reloadSignatureHook, when set, is called between the "before" signature
	// read and the config load inside Reload(), on every retry attempt.
	// Tests use it to land a simulated concurrent edit inside that window;
	// nil in production and untouched by Check(), which does not retry.
	reloadSignatureHook func()
}

// maxReloadSignatureAttempts bounds Reload()'s read-verify retry: giving up
// after a few attempts (rather than looping forever under a directory that
// keeps changing) simply leaves nextSignature unset, so MarkApplied is a
// no-op and the next Check() cycle re-evaluates from disk -- it never marks a
// change applied without having verified it.
const maxReloadSignatureAttempts = 5

func NewReloader(configDir string) (*Reloader, error) {
	return NewReloaderWithInterval(configDir, DefaultReloadInterval)
}

func NewReloaderWithInterval(configDir string, interval time.Duration) (*Reloader, error) {
	if interval <= 0 {
		interval = DefaultReloadInterval
	}
	reloader := &Reloader{
		configDir: configDir,
		interval:  interval,
	}
	signature, err := ConfigFileSignature(configDir)
	if err != nil {
		return reloader, err
	}
	reloader.lastSignature = signature
	return reloader, nil
}

func (r *Reloader) Interval() time.Duration {
	if r == nil || r.interval <= 0 {
		return DefaultReloadInterval
	}
	return r.interval
}

func (r *Reloader) Check() (*Config, bool, error) {
	signature, err := ConfigFileSignature(r.configDir)
	if err != nil {
		return nil, false, err
	}
	if signature == r.lastSignature {
		return nil, false, nil
	}
	cfg, err := LoadConfig(r.configDir)
	if err != nil {
		return nil, true, err
	}
	r.nextSignature = signature
	return cfg, true, nil
}

// Reload loads the current config and, when it can verify the file signature
// did not move between the read and the load, tags the reloader with that
// signature so a later MarkApplied()/Check() pair correctly reports "no
// pending change". It retries the signature/load pair up to
// maxReloadSignatureAttempts times when a concurrent edit is caught landing
// inside that window, and always returns the last config it loaded even if
// it never managed to verify a stable signature.
func (r *Reloader) Reload() (*Config, error) {
	var cfg *Config
	var stableSignature string
	for attempt := 0; attempt < maxReloadSignatureAttempts; attempt++ {
		before, beforeErr := ConfigFileSignature(r.configDir)
		if r.reloadSignatureHook != nil {
			r.reloadSignatureHook()
		}
		loaded, err := LoadConfig(r.configDir)
		if err != nil {
			return nil, err
		}
		cfg = loaded
		after, afterErr := ConfigFileSignature(r.configDir)
		if beforeErr == nil && afterErr == nil && before == after {
			stableSignature = after
			break
		}
	}
	if stableSignature != "" {
		r.nextSignature = stableSignature
	}
	return cfg, nil
}

func (r *Reloader) MarkApplied() {
	if r == nil || r.nextSignature == "" {
		return
	}
	r.lastSignature = r.nextSignature
	r.nextSignature = ""
}
