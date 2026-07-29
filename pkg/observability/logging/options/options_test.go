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

package options

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	t.Parallel()

	o := New()
	require.Equal(t, DefaultLogLevel, o.LogLevel)
	require.Equal(t, DefaultLogFile, o.LogFile)
}

func TestClone(t *testing.T) {
	t.Parallel()

	o := New()
	o.LogFile = "/tmp/trickster.log"
	o.LogLevel = "debug"

	c := o.Clone()
	require.NotSame(t, o, c)
	require.Equal(t, o.LogFile, c.LogFile)
	require.Equal(t, o.LogLevel, c.LogLevel)

	c.LogLevel = "error"
	require.Equal(t, "debug", o.LogLevel)
	require.Equal(t, "error", c.LogLevel)
}

func TestInitialize(t *testing.T) {
	t.Parallel()

	o := &Options{}
	require.NoError(t, o.Initialize(""))
	require.Equal(t, DefaultLogLevel, o.LogLevel)

	o = &Options{LogLevel: "warn", LogFile: "/var/log/trickster.log"}
	require.NoError(t, o.Initialize(""))
	require.Equal(t, "warn", o.LogLevel)
	require.Equal(t, "/var/log/trickster.log", o.LogFile)
}

func TestValidate(t *testing.T) {
	t.Parallel()

	validLevels := []string{"error", "warn", "fatal", "info", "debug",
		"ERROR", "Warn", "FATAL", "Info", "DEBUG"}
	for _, lvl := range validLevels {
		t.Run(lvl, func(t *testing.T) {
			t.Parallel()
			o := &Options{LogLevel: lvl}
			ok, err := o.Validate()
			require.True(t, ok)
			require.NoError(t, err)
		})
	}

	invalid := []string{"", "trace", "x", "verbose"}
	for _, lvl := range invalid {
		t.Run("invalid_"+lvl, func(t *testing.T) {
			t.Parallel()
			o := &Options{LogLevel: lvl}
			ok, err := o.Validate()
			require.False(t, ok)
			require.True(t, errors.Is(err, ErrInvalidLogLevel))
		})
	}
}
