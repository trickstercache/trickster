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
	"github.com/trickstercache/trickster/v2/pkg/parsing/lex"
	lsql "github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
)

// DataFusion SQL token types
const (
	// Unordered keywords / functions
	tokenDateBin token.Typ = iota + (lsql.TokenSQLFunction + 40)
	tokenDateTrunc
	tokenInterval
)

// token values
const (
	tokenValInterval = "interval"
)

var dfKey = map[string]token.Typ{
	"date_bin":       tokenDateBin,
	"date_trunc":     tokenDateTrunc,
	tokenValInterval: tokenInterval,
}

// LexerOptions returns DataFusion-crafted Lexer Options
func LexerOptions() *lex.Options {
	return &lex.Options{
		CustomKeywords: dfKey,
	}
}
