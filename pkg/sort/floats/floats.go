/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

// Package floats provides sorting capabilities to a slice of type float64
package floats

// Floats represents an array of float64's
type Floats []float64

// Len returns the length of an array of float64's
func (t Floats) Len() int {
	return len(t)
}

// Less returns true if i comes before j
func (t Floats) Less(i, j int) bool {
	return t[i] < t[j]
}

// Swap modifies an array of float64's by swapping the values in indexes i and j
func (t Floats) Swap(i, j int) {
	t[i], t[j] = t[j], t[i]
}
