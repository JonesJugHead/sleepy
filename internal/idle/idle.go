// Package idle stops a backend once it has had no players for a while.
package idle

import (
	"time"

	"sleepy/internal/backend"
	"sleepy/internal/logx"
	"sleepy/internal/settings"
)

// Watch stops b after sleep_after with no players, never before it has run for
// minimum_online_time, and never while auto-sleep is off. Settings changes restart the countdown.
func Watch(b *backend.Backend, s *settings.Store) {
	go func() {
		var timer *time.Timer
		var expired <-chan time.Time
		lastActive := 0
		for {
			state, active, backendChanged := b.Snapshot()
			cfg, autoSleep, settingsChanged := s.Snapshot()
			idle := autoSleep && state == backend.Ready && active == 0
			switch {
			case !idle && expired != nil:
				timer.Stop()
				expired = nil
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
				expired = nil
				b.Stop("no players for " + cfg.SleepAfter.String())
			}
		}
	}()
}
