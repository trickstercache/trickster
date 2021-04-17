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

package token

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Common Tokens
const (
	TokenValPlus    = "+"
	TokenValMinus   = "-"
	TokenValStar    = "*"
	TokenValSlash   = "/"
	TokenValPercent = "%"
	TokenValComma   = ","

	TokenValTrue  = "true"
	TokenValFalse = "false"
)

// Token represents a token or text string returned from the scanner.
type Token struct {
	Typ  Typ       // The type of this Token.
	Pos  int       // The starting position, in bytes, of this Token in the input string.
	Val  string    // The value of this Token.
	dict TypLookup // to get the token Typ as a string for String() calls
}

// Lookup represents a map of Strings to Token Reference
type Lookup map[string]*Token

// Tokens represents a slice of Tokens
type Tokens []*Token

// ErrInvalidToken indicates a general invalid token (e.g., nil reference)
var ErrInvalidToken = errors.New("invalid token")

// ErrParsingInt indicates a general integer parsing error
var ErrParsingInt = errors.New("error parsing input for integer")

// Clone makes a perfect copy of the subject Token
func (t *Token) Clone() *Token {
	return &Token{
		Typ: t.Typ,
		Pos: t.Pos,
		Val: t.Val,
	}
}

// Int64 attempts to return an int64 rendition of the token value
func (t *Token) Int64() (int64, error) {
	if t.Typ != Number {
		return 0, ErrParsingInt
	}
	n, err := strconv.ParseInt(t.Val, 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (t *Token) String() string {
	return fmt.Sprintf("{type:%v,pos:%d,val:`%s`}", t.Typ, t.Pos, t.Val)
}

func (t Tokens) String() string {
	lm1 := len(t) - 1
	if lm1 == -1 {
		return "[]"
	}
	sb := &strings.Builder{}
	sb.WriteByte('[')
	for i, v := range t {
		sb.WriteString(v.String())
		if i != lm1 {
			sb.WriteByte(',')
		}
	}
	sb.WriteByte(']')
	return sb.String()
}

// Values returns a []string of the Vals in Tokens
func (t Tokens) Values() []string {
	s := make([]string, len(t))
	for i, v := range t {
		s[i] = v.Val
	}
	return s
}
