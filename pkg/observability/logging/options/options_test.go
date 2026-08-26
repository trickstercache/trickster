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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging/manager"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sizeconv"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
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

func TestRotationRetentionYAML(t *testing.T) {
	t.Parallel()

	doc := `
log_file: /var/log/trickster/trickster.log
log_level: info
rotation:
  size: 64MB
  interval: 1d
retention:
  count: 5
  age: 3d
compress: false
`
	o := New()
	require.NoError(t, yaml.Unmarshal([]byte(doc), o))
	mo := o.ManagerOptions()
	require.Equal(t, "/var/log/trickster/trickster.log", mo.Filename)
	require.Equal(t, int64(64*1024*1024), mo.MaxSizeBytes)
	require.Equal(t, 24*time.Hour, mo.Interval)
	require.Equal(t, 5, mo.RetentionCount)
	require.Equal(t, 3*24*time.Hour, mo.RetentionAge)
	require.False(t, mo.Compress)
}

func TestManagerOptionsDefaults(t *testing.T) {
	t.Parallel()

	o := New()
	o.LogFile = "/tmp/trickster.log"
	mo := o.ManagerOptions()
	require.Equal(t, manager.DefaultMaxSizeBytes, mo.MaxSizeBytes)
	require.Equal(t, time.Duration(0), mo.Interval)
	require.Equal(t, manager.DefaultRetentionCount, mo.RetentionCount)
	require.Equal(t, manager.DefaultRetentionAge, mo.RetentionAge)
	require.True(t, mo.Compress)
}

func TestCloneDeepCopiesRotation(t *testing.T) {
	t.Parallel()

	size := sizeconv.Size(64)
	count := 5
	compress := false
	o := New()
	o.Rotation = &manager.RotationOptions{Size: &size}
	o.Retention = &manager.RetentionOptions{Count: &count}
	o.Compress = &compress

	c := o.Clone()
	require.NotSame(t, o.Rotation, c.Rotation)
	require.NotSame(t, o.Retention, c.Retention)
	require.NotSame(t, o.Compress, c.Compress)
	*c.Rotation.Size = 128
	*c.Retention.Count = 9
	require.Equal(t, sizeconv.Size(64), *o.Rotation.Size)
	require.Equal(t, 5, *o.Retention.Count)
}

func TestRotationEqual(t *testing.T) {
	t.Parallel()

	var nilOpts *Options
	require.True(t, nilOpts.RotationEqual(nil))
	require.False(t, nilOpts.RotationEqual(New()))
	require.False(t, New().RotationEqual(nil))

	a, b := New(), New()
	// different filenames and levels do not affect rotation equality
	a.LogFile, b.LogFile = "/tmp/a.log", "/tmp/b.log"
	a.LogLevel, b.LogLevel = "info", "debug"
	require.True(t, a.RotationEqual(b))

	// explicit values equal to the defaults still compare equal
	size := sizeconv.Size(manager.DefaultMaxSizeBytes)
	a.Rotation = &manager.RotationOptions{Size: &size}
	require.True(t, a.RotationEqual(b))

	size2 := sizeconv.Size(64)
	b.Rotation = &manager.RotationOptions{Size: &size2}
	require.False(t, a.RotationEqual(b))

	b.Rotation = nil
	compress := false
	b.Compress = &compress
	require.False(t, a.RotationEqual(b))
}
