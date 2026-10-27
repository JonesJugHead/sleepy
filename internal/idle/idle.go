// Package idle stops a backend once it has had no players for a while.
package idle

import (
	"time"

	"sleepy/internal/backend"
	"sleepy/internal/logx"
	"sleepy/internal/settings"
)

// Guards can postpone a stop that is due. Both are optional.
type Guards struct {
	// KeepAwake returns the date until which the server must stay up, if any.
	KeepAwake func() (until time.Time, reason string, ok bool)
	// Busy reports work in progress (busy_command). The stop is retried every busy_retry, and
	// happens anyway once it has been postponed for busy_max_delay.
	Busy func() (busy bool, reason string)
}

// Watch stops b after sleep_after with no players, never before it has run for
// minimum_online_time, and never while auto-sleep is off. Settings changes restart the countdown.
func Watch(b *backend.Backend, s *settings.Store, guards Guards) {
	go func() {
		var timer *time.Timer
		var expired <-chan time.Time
		lastActive := 0
		// busySince: when the busy check first postponed the current idle stop.
		var busySince time.Time
		for {
			state, active, backendChanged := b.Snapshot()
			cfg, autoSleep, settingsChanged := s.Snapshot()
			idle := autoSleep && state == backend.Ready && active == 0
			switch {
			case !idle && expired != nil:
				timer.Stop()
				expired = nil
				busySince = time.Time{}
			case !idle:
				busySince = time.Time{}
			case idle && expired == nil:
				// Not logged when the server just became ready: the waiting player is about to be relayed.
				if lastActive > 0 {
					logx.Printf("No players left, sleeping in %v unless someone joins", cfg.SleepAfter)
				}
				timer = time.NewTimer(cfg.SleepAfter)
				expired = timer.C
			}
			lastActive = active

			select {
			case <-backendChanged:
			case <-settingsChanged:
				if expired != nil {
					timer.Stop()
					expired = nil
				}
			case <-expired:
				if remaining := cfg.MinimumOnlineTime - b.Uptime(); remaining > 0 {
					timer = time.NewTimer(remaining)
					expired = timer.C
					continue
				}
				if wait := postpone(guards, cfg.BusyRetry, cfg.BusyMaxDelay, &busySince); wait > 0 {
					timer = time.NewTimer(wait)
					expired = timer.C
					continue
				}
				expired = nil
				busySince = time.Time{}
				b.Stop("no players for " + cfg.SleepAfter.String())
			}
		}
	}()
}

// postpone returns how long to wait before trying to stop again, 0 to stop now.
func postpone(g Guards, busyRetry, busyMax time.Duration, busySince *time.Time) time.Duration {
	if g.KeepAwake != nil {
		if until, reason, ok := g.KeepAwake(); ok {
			if reason == "" {
				reason = "keep-awake"
			}
			wait := time.Until(until)
			logx.Printf("Not sleeping yet: %s until %s", reason, until.Format(time.RFC3339))
			// Checked again at least every 15 minutes, in case the file is removed early.
			return min(wait, 15*time.Minute) + time.Second
		}
	}
	if g.Busy != nil {
		if busy, reason := g.Busy(); busy {
			if busySince.IsZero() {
				*busySince = time.Now()
			}
			if time.Since(*busySince) >= busyMax {
				logx.Printf("Still busy (%s), but postponed for %v already: sleeping anyway", reason, busyMax)
				return 0
			}
			logx.Printf("Not sleeping yet: %s, checking again in %v", reason, busyRetry)
			return busyRetry
		}
	}
	return 0
}
