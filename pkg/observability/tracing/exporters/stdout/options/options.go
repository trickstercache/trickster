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

// Options is a collection of Stdout-specific options
type Options struct {
	PrettyPrint bool `yaml:"pretty_print,omitempty"`
}

// Clone returns a perfect copy of the subject *Options
func (o *Options) Clone() *Options {
	return &Options{PrettyPrint: o.PrettyPrint}
}
