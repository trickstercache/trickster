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
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
)

func TestAutoReloader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		var calls atomic.Int32
		var source atomic.Value
		r := newAutoReloader(ctx, func(gotSource string) (bool, error) {
			source.Store(gotSource)
			calls.Add(1)
			return true, nil
		})

		var changed atomic.Bool
		changed.Store(true)
		r.updateSettings(autoReloadSettings{
			interval:   time.Hour,
			hasChanged: changed.Load,
		})
		synctest.Wait()
		if got := calls.Load(); got != 0 {
			t.Fatalf("reload calls before interval = %d; want 0", got)
		}

		time.Sleep(time.Hour)
		synctest.Wait()
		if got := calls.Load(); got != 1 {
			t.Fatalf("reload calls after interval = %d; want 1", got)
		}
		if got, _ := source.Load().(string); got != autoReloadSource {
			t.Errorf("reload source = %q; want %q", got, autoReloadSource)
		}

		changed.Store(false)
		time.Sleep(time.Hour)
		synctest.Wait()
		if got := calls.Load(); got != 1 {
			t.Errorf("reload calls for unchanged config = %d; want 1", got)
		}

		r.updateSettings(autoReloadSettings{
			interval:   2 * time.Hour,
			hasChanged: changed.Load,
		})
		synctest.Wait()
		changed.Store(true)
		time.Sleep(time.Hour)
		synctest.Wait()
		if got := calls.Load(); got != 1 {
			t.Errorf("reload calls before updated interval = %d; want 1", got)
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if got := calls.Load(); got != 2 {
			t.Errorf("reload calls after updated interval = %d; want 2", got)
		}

		r.updateSettings(autoReloadSettings{})
		synctest.Wait()
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		if got := calls.Load(); got != 2 {
			t.Errorf("reload calls while disabled = %d; want 2", got)
		}

		cancel()
		r.Close()
		r.Close()
	})
}

func TestAutoReloaderInstanceHooks(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	si := &instance.ServerInstance{}
	r := bindAutoReloader(ctx, si, func(string) (bool, error) { return false, nil })
	defer r.Close()

	c := config.NewConfig()
	c.MgmtConfig.AutoReloadInterval = 10 * time.Second
	si.Config = c
	notifyAutoReloader(si)
	settings := r.currentSettings()
	if settings.interval != 10*time.Second || settings.hasChanged == nil {
		t.Fatalf("settings = %#v; want configured interval and change callback", settings)
	}
	if settings.hasChanged() {
		t.Error("config without a file path reported changed")
	}

	si.Config = &config.Config{}
	notifyAutoReloader(si)
	settings = r.currentSettings()
	if settings.interval != 0 || settings.hasChanged != nil {
		t.Errorf("settings = %#v; want disabled settings", settings)
	}

	si.OnConfigReloaded = nil
	notifyAutoReloader(si)
}
