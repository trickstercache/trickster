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
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

const MaxRewriterChainExecutions int32 = 32

// RewriteList is a list of Rewrite Instructions
type RewriteList [][]string

// Options is a collection of Options pertaining to Request Rewriter Instructions
type Options struct {
	Name         string      `yaml:"-"` // populated from a Lookup key
	Instructions RewriteList `yaml:"instructions,omitempty"`
}

// Lookup is a map of Options keyed by Rule Name
type Lookup map[string]*Options

var ErrInvalidName = errors.New("invalid rewriter name")
var restrictedNames = sets.New([]string{"", "none"})

// Clone returns an exact copy of the subject *Options
func (o *Options) Clone() *Options {
	o2 := &Options{}
	if len(o.Instructions) > 0 {
		o2.Instructions = o.Instructions.Clone()
	}
	return o2
}

// Validate returns an error if there are issues with the Rewriter options.
func (o *Options) Validate() error {
	if restrictedNames.Contains(o.Name) {
		return ErrInvalidName
	}
	return nil
}

// Validate returns an error if there are issues with any of the Rewriters options.
func (l Lookup) Validate() error {
	for k, o := range l {
		if o == nil {
			continue
		}
		o.Name = k
		if err := o.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Clone returns an exact copy of the subject RewriteList
func (rl RewriteList) Clone() RewriteList {
	var rl2 RewriteList
	if len(rl) > 0 {
		rl2 = make(RewriteList, len(rl))
		for i := range rl {
			rl2[i] = slices.Clone(rl[i])
		}
	}
	return rl2
}
