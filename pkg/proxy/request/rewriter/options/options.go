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

import "github.com/trickstercache/trickster/v2/pkg/util/copiers"

const MaxRewriterChainExecutions int = 32

// RewriteList is a list of Rewrite Instructions
type RewriteList [][]string

// Options is a collection of Options pertaining to Request Rewriter Instructions
type Options struct {
	Instructions RewriteList `yaml:"instructions,omitempty"`
}

// Clone returns an exact copy of the subject *Options
func (o *Options) Clone() *Options {
	o2 := &Options{}
	if len(o.Instructions) > 0 {
		o2.Instructions = o.Instructions.Clone()
	}
	return o2
}

// Clone returns an exact copy of the subject RewriteList
func (rl RewriteList) Clone() RewriteList {
	var rl2 RewriteList
	if len(rl) > 0 {
		rl2 = make(RewriteList, len(rl))
		for i := range rl {
			rl2[i] = copiers.CopyStrings(rl[i])
		}
	}
	return rl2
}
