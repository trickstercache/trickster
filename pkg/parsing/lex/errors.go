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

package lex

import "errors"

// ErrContinue prompts the loop to immediately continue to the next iteration
// and is not an actual fatal error (think EOF)
var ErrContinue = errors.New("continue")

// ErrBreak prompts the loop to break immediately, but return a nil error to the caller
// and is not an actual fatal error (think EOF)
var ErrBreak = errors.New("break")

// ErrMissingRequiredKeyword indicates that the input's was missing a required keyword
var ErrMissingRequiredKeyword = errors.New("missing required keyword")
