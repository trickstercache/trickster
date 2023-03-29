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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
	testutil "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestNoop(t *testing.T) {
	f := Noop(nil, nil, nil)
	if f != nil {
		t.Error("expected nil func pointer")
	}
}

func TestParserError(t *testing.T) {
	err := ParserError(nil, nil)
	if err != nil {
		t.Error("expected nil err")
	}
	err = ParserError(testutil.ErrTest, &token.Token{})
	if err == nil {
		t.Error("expected non-nil err")
	}
	against := testutil.ErrTest
	if !err.(*parsingError).Is(against) {
		t.Error("parsing error should be a test error")
	}
	if !err.(*parsingError).Is(err) {
		t.Error("parsing error should be a parsing error")
	}
	against = ErrInvalidKeywordOrder
	if err.(*parsingError).Is(against) {
		t.Error("parsing error should not be a invalid keyword order error")
	}
	if err.Error() != "parser error='test error', position=0, token='', type=0" {
		t.Errorf("incorrect parser error %s", err.Error())
	}
}
