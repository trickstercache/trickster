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

package segments

import "time"

// Time implements Diffable for time.Time
type Time struct{}

func (Time) Add(a time.Time, step time.Duration) time.Time { return a.Add(step) }
func (Time) Less(a, b time.Time) bool                      { return a.Before(b) }
func (Time) Equal(a, b time.Time) bool                     { return a.Equal(b) }
func (Time) Zero() time.Time                               { return time.Time{} }
func (Time) Neg(step time.Duration) time.Duration          { return -step }
