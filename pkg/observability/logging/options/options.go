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

// Options is a collection of Logging options
type Options struct {
	// LogFile provides the filepath to the instances's logfile. Set as empty string to Log to Console
	LogFile string `yaml:"log_file,omitempty"`
	// LogLevel provides the most granular level (e.g., DEBUG, INFO, ERROR) to log
	LogLevel string `yaml:"log_level,omitempty"`
}

// New returns a new Options with default values
func New() *Options {
	return &Options{LogLevel: DefaultLogLevel, LogFile: DefaultLogFile}
}

// Clone returns a clone of the Options
func (o *Options) Clone() *Options {
	return &Options{LogLevel: o.LogLevel, LogFile: o.LogFile}
}
