/*
 *  Zif
 *  Copyright (C) 2017 Zif LTD
 *
 *  This program is free software: you can redistribute it and/or modify
 *  it under the terms of the GNU Affero General Public License as published
 *  by the Free Software Foundation, either version 3 of the License, or
 *  (at your option) any later version.
 *
 *  This program is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *  GNU Affero General Public License for more details.

 *  You should have received a copy of the GNU Affero General Public License
 *  along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */
package util

import "time"

type Limiter struct {
	Throttle chan time.Time
	Ticker   *time.Ticker
	quit     chan bool
}

// Return a new rate limiter. This is used to make sure that something like a
// requrest for instance does not run too many times. However, it does allow
// bursting. For example, it may refill at a rate of 3 tokens per minute, and
// have a burst of three. This means that if it has been running for more than
// a minute without being used then, it will be able to be used 3 times in
// rapid succession - no limiting will apply.
func NewLimiter(rate time.Duration, burst int, fill bool) *Limiter {
	tick := time.NewTicker(rate)
	throttle := make(chan time.Time, burst)
	quit := make(chan bool)

	if fill {
		for i := 0; i < burst; i++ {
			throttle <- time.Now()
		}
	}

	go func() {
		for t := range tick.C {
			select {
			case _ = <-quit:
				return
			case throttle <- t:
			default:
			}
		}
	}()

	return &Limiter{throttle, tick, quit}
}

// Block until the given time has elapsed. Or just use a token from the bucket.
func (l *Limiter) Wait() {
	_, _ = <-l.Throttle
}

// Finish running.
func (l *Limiter) Stop() {
	l.Ticker.Stop()
	l.quit <- true
	close(l.Throttle)
}

// Limits requests from peers
type PeerLimiter struct {
	queryLimiter    *Limiter
	announceLimiter *Limiter
}

func (pl *PeerLimiter) Setup() {
	// Allow an announce every 10 minutes, bursting to allow three.
	// The burst is there as people may make "mistakes" with titles or descriptions
	pl.announceLimiter = NewLimiter(time.Minute*10, 3, true)

	pl.queryLimiter = NewLimiter(time.Second/3, 3, true)
}
