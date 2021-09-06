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

package sql

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
	testutil "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestIsKeyword(t *testing.T) {
	tests := []struct {
		typ      token.Typ
		expected bool
	}{
		{token.EOF, false},
		{TokenHaving, true},
		{TokenCount, false},
		{TokenNull, false},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			b := IsKeyword(test.typ)
			if b != test.expected {
				t.Errorf("expected %t got %t", test.expected, b)
			}
		})
	}
}

func TestIsPrimaryKeyword(t *testing.T) {
	tests := []struct {
		typ      token.Typ
		expected bool
	}{
		{token.EOF, false},
		{TokenHaving, true},
		{TokenCount, false},
		{TokenNull, false},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			b := IsPrimaryKeyword(test.typ)
			if b != test.expected {
				t.Errorf("expected %t got %t", test.expected, b)
			}
		})
	}
}

func TestIsNonVerbPrimaryKeyword(t *testing.T) {
	tests := []struct {
		typ      token.Typ
		expected bool
	}{
		{token.EOF, false},
		{TokenHaving, true},
		{TokenCount, false},
		{TokenNull, false},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			b := IsNonVerbPrimaryKeyword(test.typ)
			if b != test.expected {
				t.Errorf("expected %t got %t", test.expected, b)
			}
		})
	}
}

func TestIsVerb(t *testing.T) {
	tests := []struct {
		typ      token.Typ
		expected bool
	}{
		{token.EOF, false},
		{TokenHaving, false},
		{TokenCount, false},
		{TokenNull, false},
		{TokenSelect, true},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			b := IsVerb(test.typ)
			if b != test.expected {
				t.Errorf("expected %t got %t", test.expected, b)
			}
		})
	}
}

var zeroTime = time.Time{}

func TestTokenToTime(t *testing.T) {
	tests := []struct {
		tk       *token.Token
		et       time.Time
		ee       error
		variance time.Duration
	}{
		{&token.Token{Typ: token.String, Val: "invalid time"}, zeroTime, ErrInvalidInputLength, 0},
		{&token.Token{Typ: token.String, Val: "2020-01-01 00:00:00"}, testutil.Time2020, nil, 0},
		{&token.Token{Typ: token.Number, Val: strconv.FormatInt(testutil.Epoch2020, 10)}, testutil.Time2020, nil, 0},
		{&token.Token{Typ: token.Number, Val: "not a number"}, zeroTime, strconv.ErrSyntax, 0},
		{&token.Token{Typ: token.Identifier, Val: "x"}, time.Now(), nil, time.Second},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			tm, _, err := TokenToTime(test.tk)
			if err != test.ee && !errors.Is(err, test.ee) {
				t.Error(err)
			}
			if test.et.After(tm.Add(test.variance)) && test.et.Before(tm.Add(-test.variance)) {
				t.Errorf("expected %s got %s", test.et.String(), tm.String())
			}
		})
	}
}

// // TokenToTime returns a time value derived from the token
// func TokenToTime(i *token.Token) (time.Time, error) {
// 	var t time.Time
// 	var err error
// 	switch i.Typ {
// 	case token.String:
// 		t, err = ParseBasicDateTime(UnQuote(i.Val))
// 		if err != nil {
// 			return t, err
// 		}
// 	case token.Number:
// 		n, err := i.Int64()
// 		if err != nil {
// 			return t, err
// 		}
// 		t = time.Unix(n, 0)
// 	default:
// 		t = time.Now()
// 	}
// 	return t, err
// }
