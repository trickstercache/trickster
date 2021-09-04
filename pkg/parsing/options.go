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

package parsing

import (
	"github.com/trickstercache/trickster/v2/pkg/parsing/lex"
)

// Options represents the options for the parser
type Options struct {
	entryFunc StateFn
	lexer     lex.Lexer
	lexOpts   *lex.Options

	Decisions map[string]DecisionSet
}

// New returns a new Options with the provided parameters
func New(
	entryFunc StateFn,
	lexer lex.Lexer,
	lo *lex.Options,
) *Options {
	return &Options{
		entryFunc: entryFunc,
		lexer:     lexer,
		lexOpts:   lo,
	}
}

// WithDecisions merges the provided named DecisionSet into the root named DS
func (o *Options) WithDecisions(name string, ds DecisionSet) *Options {
	if o.Decisions == nil {
		o.Decisions = make(map[string]DecisionSet)
	}
	m, ok := o.Decisions[name]
	if !ok {
		m = make(DecisionSet)
	}
	for k, v := range ds {
		m[k] = v
	}
	o.Decisions[name] = m
	return o
}

// GetDecisions returns the named DS
func (o *Options) GetDecisions(name string) DecisionSet {
	if o.Decisions == nil {
		return nil
	}
	if m, ok := o.Decisions[name]; ok {
		return m
	}
	return nil
}

// WithEntryFunc sets the Parser's entryFunc
func (o *Options) WithEntryFunc(f StateFn) *Options {
	o.entryFunc = f
	return o
}

// WithLexer sets the Lexer for the Parser
func (o *Options) WithLexer(l lex.Lexer, lo *lex.Options) *Options {
	o.lexer = l
	o.lexOpts = lo
	return o
}

// Lexer returns the Lexer
func (o *Options) Lexer() (lex.Lexer, *lex.Options) {
	return o.lexer, o.lexOpts
}

// EntryFunc returns the entryFunc
func (o *Options) EntryFunc() StateFn {
	return o.entryFunc
}
