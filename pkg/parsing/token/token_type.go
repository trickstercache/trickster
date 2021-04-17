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

import "strconv"

// Typ identifies the type of lex items.
type Typ uint

// Types
const (
	Error              Typ = iota // error occurred; value is text of error
	EOF                           // end of the input was reached and there are no more tokens coming
	Char                          // printable ASCII character; grab bag for comma etc.
	Equals                        // equals ('=') introducing an assignment
	Bool                          // boolean constant
	Complex                       // complex constant (1+2i); imaginary is just a number
	Identifier                    // unquoted, non-keyword alphanumeric identifier not starting with '.'
	LeftParen                     // '('
	Number                        // simple number, including imaginary
	RightParen                    // ')'
	Space                         // run of spaces separating arguments
	String                        // quoted string (includes quotes)
	LessThan                      // <
	GreaterThan                   // >
	LessThanOrEqual               // <=
	GreaterThanOrEqual            // >=
	InequalityOperator            // !, NOT, depending upon the applied language specification
	NotEqualOperator              // !=
	Comma                         // ','
	//
	// TokenTypeLogicalOps is the value after where Logical Operators start
	TokenTypeLogicalOps    Typ = iota + 32 // this value boundary should not be used for an item.Typ
	LogicalAnd                             // AND, &&, etc., depending upon the applied language specification
	LogicalOr                              // OR, ||, etc.,  depending upon the applied language specification
	TokenTypeLogicalOpsEnd                 // this value boundary should not be used for an item.Typ
	//
	// TypeMathOps is the value after where Mathemathical Operators start
	TokenTypeMathOps Typ = iota + 48 // this value boundary is should not be used for an item.Typ
	Plus
	Minus
	Multiply
	Divide
	Modulo
	TokenTypeMathOpsEnd // this value boundary should not be used for an item.Typ
	//
	// CustomNonKeyword is the value after which custom implementations start custom item types
	CustomNonKeyword Typ = 1024 //
	// CustomNonKeyword is the value after where custom non-Keywords end
	CustomNonKeywordEnd Typ = 65535
	// CustomKeyword defines the value after where custom Keywords item types start
	// We pad to 65K, so that any custom expansions can use the Type value space as regular item or keywords
	CustomKeyword Typ = 65536 // used only to delimit Keywords
)

// TypeCheckFunc represents a function that returns true or false based on the  Typ
type TypeCheckFunc func(Typ) bool

// TypLookup represents a map of Typs to their human-readable descriptions
type TypLookup map[Typ]string

var typLookup = TypLookup{
	Error:              "error",
	EOF:                "eof",
	Char:               "char",
	Equals:             "equals",
	Bool:               "bool",
	Complex:            "complex",
	Identifier:         "ident",
	LeftParen:          "lp",
	Number:             "num",
	RightParen:         "rp",
	Space:              "space",
	String:             "string",
	LessThan:           "lt",
	GreaterThan:        "gt",
	LessThanOrEqual:    "lte",
	GreaterThanOrEqual: "gte",
	InequalityOperator: "not",
	NotEqualOperator:   "ne",
	Comma:              "comma",
	LogicalAnd:         "logand",
	LogicalOr:          "logor",
	Plus:               "plus",
	Minus:              "minus",
	Multiply:           "multiply",
	Divide:             "divide",
	Modulo:             "modulo",
}

func (t Typ) String() string {
	if s, ok := typLookup[t]; ok {
		return s
	}
	return strconv.Itoa(int(t))
}

// IsBreakable returns true if the Typ is EOF or Error
func (t Typ) IsBreakable() bool {
	return t <= EOF
}

// IsGreaterOrLessThan returns true if the Typ is >, >=, <, or <=
func (t Typ) IsGreaterOrLessThan() bool {
	return t >= LessThan && t <= GreaterThanOrEqual
}

// IsGTorGTE returns true if the Typ is > or >=
func (t Typ) IsGTorGTE() bool {
	return t == GreaterThan || t == GreaterThanOrEqual
}

// IsLTorLTE returns true if the Typ is < or <=
func (t Typ) IsLTorLTE() bool {
	return t == LessThan || t == LessThanOrEqual
}

// IsOrEquals returns true if the Typ is <= or >=
func (t Typ) IsOrEquals() bool {
	return t == GreaterThanOrEqual || t == LessThanOrEqual
}

// IsMathOperator returns true if the subject Typ falls in the Math Operator range
func (t Typ) IsMathOperator() bool {
	return t > TokenTypeMathOps && t < TokenTypeMathOpsEnd
}

// IsBoolean returns true if the subject Typ falls in the Boolean Operator range
func (t Typ) IsBoolean() bool {
	return t > TokenTypeLogicalOps && t < TokenTypeLogicalOpsEnd
}

// IsAddOrSubtract returns true if the subject Typ is Plus or Minus
func (t Typ) IsAddOrSubtract() bool {
	return t == Plus || t == Minus
}

// IsErr returns true if the sbuject Typ is Error
func (t Typ) IsErr() bool {
	return t == Error
}

// IsEOF returns true if the  Typ is EOF
func (t Typ) IsEOF() bool {
	return t == EOF
}

// IsSpaceChar returns true if the  Typ is a Space, Tab, CR or LF
func (t Typ) IsSpaceChar() bool {
	return t == Space
}

// IsLogicalOperator returns true if the subject Typ falls in the Logical Operator range
func IsLogicalOperator(t Typ) bool {
	return t > TokenTypeLogicalOps && t < TokenTypeLogicalOpsEnd
}

// IsComma returns true if the subject Typ is Comma
func IsComma(t Typ) bool {
	return t == Comma
}
