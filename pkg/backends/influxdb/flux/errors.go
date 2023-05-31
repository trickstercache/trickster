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

package flux

import "fmt"

type InvalidTimeFormatError struct {
	rd error
	at error
	ut error
}

type FluxSyntaxError struct {
	token string
	rule  string
}

type FluxSemanticsError struct {
	rule string
}

func (err *InvalidTimeFormatError) Error() string {
	return fmt.Sprintf("invalid time format; must be relative duration (%s), RFC3999 string (%s), or Unix timestamp (%s)", err.rd, err.at, err.ut)
}

func ErrInvalidTimeFormat(relativeDuration, absoluteTime, unixTimestamp error) *InvalidTimeFormatError {
	return &InvalidTimeFormatError{
		rd: relativeDuration,
		at: absoluteTime,
		ut: unixTimestamp,
	}
}

func ErrFluxSyntax(token, rule string) error {
	return &FluxSyntaxError{
		token: token,
		rule:  rule,
	}
}

func (err *FluxSyntaxError) Error() string {
	return fmt.Sprintf("flux syntax error at '%s': %s", err.token, err.rule)
}

func ErrFluxSemantics(rule string) error {
	return &FluxSemanticsError{
		rule: rule,
	}
}

func (err *FluxSemanticsError) Error() string {
	return fmt.Sprintf("flux semantics error: %s", err.rule)
}
