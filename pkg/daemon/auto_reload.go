/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/reload"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"
)

const autoReloadSource = "auto-reload"

type autoReloadSettings struct {
	interval   time.Duration
	hasChanged func() bool
}

type autoReloader struct {
	reloader reload.Reloader

	settingsLock sync.Mutex
	settings     autoReloadSettings
	wake         chan struct{}
	cancel       context.CancelFunc
	done         chan struct{}
	closeOnce    sync.Once
}

func newAutoReloader(parent context.Context, reloader reload.Reloader) *autoReloader {
	ctx, cancel := context.WithCancel(parent) // #nosec G118 -- Close calls the retained cancel function
	r := &autoReloader{
		reloader: reloader,
		wake:     make(chan struct{}, 1),
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	safego.Go(reloadGoroutinePanic("autoReloader", autoReloadSource), func() {
		defer close(r.done)
		r.run(ctx)
	})
	return r
}

func bindAutoReloader(ctx context.Context, si *instance.ServerInstance,
	reloader reload.Reloader,
) *autoReloader {
	r := newAutoReloader(ctx, reloader)
	si.OnConfigReloaded = r.Update
	return r
}

func notifyAutoReloader(si *instance.ServerInstance) {
	if si.OnConfigReloaded != nil {
		si.OnConfigReloaded(si.Config)
	}
}

func (r *autoReloader) Update(c *config.Config) {
	settings := autoReloadSettings{}
	if c != nil && c.MgmtConfig != nil {
		settings.interval = c.MgmtConfig.AutoReloadInterval
		settings.hasChanged = c.HasConfigChanged
	}
	r.updateSettings(settings)
}

func (r *autoReloader) updateSettings(settings autoReloadSettings) {
	r.settingsLock.Lock()
	r.settings = settings
	r.settingsLock.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *autoReloader) currentSettings() autoReloadSettings {
	r.settingsLock.Lock()
	defer r.settingsLock.Unlock()
	return r.settings
}

func (r *autoReloader) Close() {
	r.closeOnce.Do(r.cancel)
	<-r.done
}

func (r *autoReloader) run(ctx context.Context) {
	var settings autoReloadSettings
	var timer *time.Timer
	var timerC <-chan time.Time
	defer func() { stopAutoReloadTimer(timer) }()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
			settings = r.currentSettings()
			timer, timerC = resetAutoReloadTimer(timer, settings.interval)
		case <-timerC:
			if settings.hasChanged != nil && settings.hasChanged() {
				_, _ = r.reloader(autoReloadSource)
			}
			timer, timerC = resetAutoReloadTimer(timer, settings.interval)
		}
	}
}

func resetAutoReloadTimer(timer *time.Timer, interval time.Duration) (*time.Timer, <-chan time.Time) {
	stopAutoReloadTimer(timer)
	if interval <= 0 {
		return nil, nil
	}
	timer = time.NewTimer(interval)
	return timer, timer.C
}

func stopAutoReloadTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}
