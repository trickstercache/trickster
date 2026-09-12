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

package controller

func (v *certVerdicts) size() int {
	v.mtx.Lock()
	defer v.mtx.Unlock()
	return len(v.entries)
}

func (v *certVerdicts) parseCount() uint64 {
	v.mtx.Lock()
	defer v.mtx.Unlock()
	return v.parses
}

func (v *certVerdicts) passCount() uint64 {
	v.mtx.Lock()
	defer v.mtx.Unlock()
	return v.pass
}

func (v *certVerdicts) verdictError(name string) error {
	v.mtx.Lock()
	defer v.mtx.Unlock()
	if e := v.entries[name]; e != nil {
		return e.err
	}
	return nil
}
