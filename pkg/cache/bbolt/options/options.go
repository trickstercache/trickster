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

// Options is a collection of Configurations for storing cached data on the Filesystem
type Options struct {
	// Filename represents the filename (including path) of the BotlDB database
	Filename string `yaml:"filename,omitempty"`
	// Bucket represents the name of the bucket within BBolt under which Trickster's keys will be stored.
	Bucket string `yaml:"bucket,omitempty"`
}

// New returns a reference to a new bbolt Options
func New() *Options {
	return &Options{Filename: DefaultBBoltFile, Bucket: DefaultBBoltBucket}
}
