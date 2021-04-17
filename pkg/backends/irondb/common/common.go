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

package common

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FormatTimestamp returns a string containing a timestamp in the format used
// by the IRONdb API.
func FormatTimestamp(t time.Time, milli bool) string {
	if milli {
		return fmt.Sprintf("%d.%03d", t.Unix(), t.Nanosecond()/1000000)
	}

	return fmt.Sprintf("%d", t.Unix())
}

// ParseTimestamp attempts to parse an IRONdb API timestamp string into a valid
// time value.
func ParseTimestamp(s string) (time.Time, error) {
	sp := strings.Split(s, ".")
	sec, nsec := int64(0), int64(0)
	var err error
	if len(sp) > 0 {
		if sec, err = strconv.ParseInt(sp[0], 10, 64); err != nil {
			return time.Time{}, fmt.Errorf("unable to parse timestamp %s: %s",
				s, err.Error())
		}
	}

	if len(sp) > 1 {
		if nsec, err = strconv.ParseInt(sp[1], 10, 64); err != nil {
			return time.Time{}, fmt.Errorf("unable to parse timestamp %s: %s",
				s, err.Error())
		}

		nsec *= 1000000
	}

	return time.Unix(sec, nsec), nil
}

// ParseDuration attempts to parse an IRONdb API duration string into a valid
// duration value.
func ParseDuration(s string) (time.Duration, error) {
	if !strings.HasSuffix(s, "s") {
		s += "s"
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("unable to parse duration %s: %s",
			s, err.Error())
	}

	return d, nil
}
