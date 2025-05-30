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

// Int64 implements Deltaable for int64 (bytes, IPv4s, etc.)
type Int64 struct{}

func (Int64) Add(a int64, step int64) int64 { return a + step }
func (Int64) Less(a, b int64) bool          { return a < b }
func (Int64) Equal(a, b int64) bool         { return a == b }
func (Int64) Zero() int64                   { return 0 }
func (Int64) Neg(step int64) int64          { return -step }
