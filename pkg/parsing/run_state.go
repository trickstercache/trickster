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
	"context"

	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

// RunState maintains the state of a unique parsing run
type RunState struct {
	tokens                   chan *token.Token
	prev, curr, next, lastkw *token.Token
	err                      error
	ctx                      context.Context
	nextOverride             StateFn
	isPeeked                 bool
	results                  map[string]interface{}
}

// NewRunState returns a new RunState object for the parser
func NewRunState(ctx context.Context) *RunState {
	rs := &RunState{
		tokens:  make(chan *token.Token, 8),
		ctx:     ctx,
		results: make(map[string]interface{}),
	}
	return rs
}

// SetResultsCollection places a collection of results into the results map
func (rs *RunState) SetResultsCollection(collectionName string, val interface{}) {
	rs.results[collectionName] = val
}

// GetResultsCollection retrieves a collection from the results map
func (rs *RunState) GetResultsCollection(collectionName string) (interface{}, bool) {
	v, ok := rs.results[collectionName]
	return v, ok
}

// Results returns the results objecxt from the RunState
func (rs *RunState) Results() map[string]interface{} {
	return rs.results
}

// WithContext attaches the provided context to the RunState
func (rs *RunState) WithContext(ctx context.Context) *RunState {
	rs.ctx = ctx
	return rs
}

// Context returns the RunState's context
func (rs *RunState) Context() context.Context {
	return rs.ctx
}

// WithError attaches an error to the RunState and aborts the run
func (rs *RunState) WithError(err error) *RunState {
	if rs.err == nil {
		// if an error was already attached, leave it alone
		rs.err = err
	}
	return rs
}

// WithNextOverride attaches an override func for the next RunState invocation
func (rs *RunState) WithNextOverride(f StateFn) *RunState {
	rs.nextOverride = f
	return rs
}

// GetReturnFunc will check for an error or EOF, before returning the Override func,
// if present, otherwise returns the provided func f
func (rs *RunState) GetReturnFunc(f StateFn, ds DecisionSet, exitOnEOF bool) StateFn {
	if rs.err != nil || (exitOnEOF && rs.curr.Typ == token.EOF) {
		return nil
	}
	if rs.nextOverride != nil {
		f = rs.nextOverride
		rs.nextOverride = nil
	} else if ds != nil && rs.curr != nil {
		if f1, ok := ds[rs.curr.Typ]; ok {
			if rs.lastkw != nil && rs.curr.Typ > token.CustomKeyword &&
				!(rs.curr.Typ > rs.lastkw.Typ) {
				rs.WithError(ErrInvalidKeywordOrder)
				return nil
			}
			rs.lastkw = rs.curr
			f = f1
		}
	}
	return f
}

// Error returns the RunState's error condition
func (rs *RunState) Error() error {
	return rs.err
}

// Previous returns the previous Token
func (rs *RunState) Previous() *token.Token {
	return rs.prev
}

// Current returns the current token
func (rs *RunState) Current() *token.Token {
	return rs.curr
}

// Peek looks at the next token and saves it to Next, but does not
// advance the state location
func (rs *RunState) Peek() *token.Token {
	if rs.curr != nil && rs.curr.Typ == token.EOF {
		return rs.curr
	}
	// this filters nil tokens so the parser is guaranteed to never encounter them
	for ; rs.next == nil; rs.next = <-rs.tokens {
	}
	return rs.next
}

// IsPeeked returnst true if the RunState has peeked to the next token
func (rs *RunState) IsPeeked() bool {
	return rs.next != nil
}

// Next retrieves the next location by peeking and then advancing
// the state
func (rs *RunState) Next() *token.Token {
	rs.Peek()
	rs.prev = rs.curr
	rs.curr = rs.next
	rs.next = nil
	return rs.curr
}

// Tokens returns the Tokens Channel for the Run
func (rs *RunState) Tokens() chan *token.Token {
	return rs.tokens
}
