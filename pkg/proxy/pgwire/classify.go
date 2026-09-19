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
package pgwire

import (
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlscan"
)

type statementKind uint8

const (
	// stmtEmpty has no tokens: blank, or only comments (pgx's pool ping).
	stmtEmpty statementKind = iota
	// stmtRead returns rows and is a caching candidate when it stands alone.
	stmtRead
	// stmtNeutral leaves session state alone: transactions, DML, SHOW, EXPLAIN.
	stmtNeutral
	stmtSet
	stmtReset
	stmtDiscardAll
	// stmtOther is anything whose effect on the session is not modeled.
	stmtOther
)

const (
	varTimeZone             = "timezone"
	varRole                 = "role"
	varSessionAuthorization = "session_authorization"
	varClientEncoding       = "client_encoding"
	varSearchPath           = "search_path"
	varExtraFloatDigits     = "extra_float_digits"
	varByteaOutput          = "bytea_output"
	varAll                  = "all"
	// maxSetTokens bounds how much of a SET statement is read for its value.
	maxSetTokens = 64
)

var (
	readKeywords = map[string]struct{}{"select": {}, "values": {}, "table": {}, "with": {}}
	// writeKeywords inside a read make it a write: a WITH query may change data
	// (WITH gone AS (DELETE ... RETURNING *) SELECT ...), and FOR UPDATE takes locks.
	writeKeywords   = map[string]struct{}{"insert": {}, "update": {}, "delete": {}, "merge": {}}
	neutralKeywords = map[string]struct{}{
		"begin": {}, "start": {}, "commit": {}, "end": {}, "rollback": {}, "abort": {},
		"savepoint": {}, "release": {}, "show": {}, "explain": {}, "insert": {}, "update": {},
		"delete": {}, "merge": {}, "copy": {}, "truncate": {}, "fetch": {}, "move": {},
		"close": {}, "declare": {}, "listen": {}, "unlisten": {}, "notify": {}, "analyze": {},
		"vacuum": {}, "checkpoint": {}, "lock": {},
	}
)

type statementClass struct {
	kind      statementKind
	multi     bool
	unsafe    bool
	local     bool
	name      string
	value     string
	isDefault bool
}

func classify(sql string, backslashEscapes bool) statementClass {
	// reads a message's SQL lexically. The first statement decides the
	// kind; later statements can only make the message unsafe.
	scanner := sqlscan.New(sql, sqlscan.Options{BackslashEscapes: backslashEscapes})
	var (
		class      statementClass
		statements int
		atStart    = true
		first      []sqlscan.Token
		// startedAsRead survives the statement being reclassified as a write
		startedAsRead bool
	)
	for {
		token, ok := scanner.Next()
		if !ok {
			break
		}
		if token.Kind == sqlscan.Punct && token.Depth == 0 && sql[token.Start] == ';' {
			atStart = true
			continue
		}
		if atStart {
			atStart = false
			statements++
			kind := leadingKind(scanner, token)
			if statements == 1 {
				class.kind, startedAsRead = kind, kind == stmtRead
			} else if kind != stmtRead && kind != stmtNeutral {
				class.unsafe = true
			}
		}
		if statements == 1 && len(first) < maxSetTokens {
			first = append(first, token)
		}
		if token.Kind == sqlscan.Word {
			switch {
			case scanner.IsWord(token, "set_config"):
				class.unsafe = true
			case token.Depth == 0 && scanner.IsWord(token, "into") && startedAsRead:
				class.unsafe = true
			case statements == 1 && class.kind == stmtRead && isWriteKeyword(scanner.Text(token)):
				// relayed like any other write: never cached, and no reason to distrust the session
				class.kind = stmtNeutral
			}
		}
	}
	class.multi = statements > 1
	if scanner.Unterminated || class.kind == stmtOther {
		class.unsafe = true
	}
	switch class.kind {
	case stmtSet:
		parseSet(scanner, first[1:], &class)
	case stmtReset:
		parseReset(scanner, first[1:], &class)
	case stmtDiscardAll:
		if len(first) < 2 || !scanner.IsWord(first[1], varAll) {
			// DISCARD PLANS, SEQUENCES and TEMP leave the modeled state alone
			class.kind = stmtNeutral
		}
	}
	return class
}

func isWriteKeyword(word string) bool {
	if len(word) < 5 || len(word) > 6 {
		return false
	}
	_, ok := writeKeywords[strings.ToLower(word)]
	return ok
}

func leadingKind(scanner *sqlscan.Scanner, token sqlscan.Token) statementKind {
	if token.Kind != sqlscan.Word {
		// a parenthesized SELECT is still a read
		if token.Kind == sqlscan.Punct && scanner.Text(token) == "(" {
			return stmtRead
		}
		return stmtOther
	}
	word := strings.ToLower(scanner.Text(token))
	if _, ok := readKeywords[word]; ok {
		return stmtRead
	}
	if _, ok := neutralKeywords[word]; ok {
		return stmtNeutral
	}
	switch word {
	case "set":
		return stmtSet
	case "reset":
		return stmtReset
	case "discard":
		return stmtDiscardAll
	}
	return stmtOther
}

func parseSet(scanner *sqlscan.Scanner, tokens []sqlscan.Token, class *statementClass) {
	if len(tokens) > 0 && scanner.IsWord(tokens[0], "local") {
		class.local, tokens = true, tokens[1:]
	} else if len(tokens) > 1 && scanner.IsWord(tokens[0], "session") &&
		!scanner.IsWord(tokens[1], "authorization") && !scanner.IsWord(tokens[1], "characteristics") {
		tokens = tokens[1:]
	}
	if len(tokens) == 0 {
		class.unsafe = true
		return
	}
	switch {
	case scanner.IsWord(tokens[0], "transaction") || scanner.IsWord(tokens[0], "constraints") ||
		len(tokens) > 1 && scanner.IsWord(tokens[0], "session") && scanner.IsWord(tokens[1], "characteristics"):
		// transaction modes never change how a result is rendered
		class.kind = stmtNeutral
		return
	case len(tokens) > 1 && scanner.IsWord(tokens[0], "time") && scanner.IsWord(tokens[1], "zone"):
		class.name, tokens = varTimeZone, tokens[2:]
	case len(tokens) > 1 && scanner.IsWord(tokens[0], "session") && scanner.IsWord(tokens[1], "authorization"):
		class.name, tokens = varSessionAuthorization, tokens[2:]
	case scanner.IsWord(tokens[0], "role"):
		class.name, tokens = varRole, tokens[1:]
	case scanner.IsWord(tokens[0], "names"):
		class.name, tokens = varClientEncoding, tokens[1:]
	case scanner.IsWord(tokens[0], "schema"):
		class.name, tokens = varSearchPath, tokens[1:]
	default:
		class.name, tokens = variableName(scanner, tokens)
		if class.name == "" || len(tokens) == 0 ||
			!scanner.IsWord(tokens[0], "to") && scanner.Text(tokens[0]) != "=" {
			class.unsafe = true
			return
		}
		tokens = tokens[1:]
	}
	class.value, class.isDefault = settingValue(scanner, tokens)
	if class.name == "" || class.value == "" && !class.isDefault {
		class.unsafe = true
	}
}

func parseReset(scanner *sqlscan.Scanner, tokens []sqlscan.Token, class *statementClass) {
	class.isDefault = true
	switch {
	case len(tokens) > 1 && scanner.IsWord(tokens[0], "time") && scanner.IsWord(tokens[1], "zone"):
		class.name = varTimeZone
	case len(tokens) > 1 && scanner.IsWord(tokens[0], "session") && scanner.IsWord(tokens[1], "authorization"):
		class.name = varSessionAuthorization
	default:
		class.name, _ = variableName(scanner, tokens)
	}
	if class.name == "" {
		class.unsafe = true
	}
}

func variableName(scanner *sqlscan.Scanner, tokens []sqlscan.Token) (string, []sqlscan.Token) {
	var name strings.Builder
	for len(tokens) > 0 {
		token := tokens[0]
		if token.Kind != sqlscan.Word && token.Kind != sqlscan.QuotedIdent {
			break
		}
		name.WriteString(strings.ToLower(strings.Trim(scanner.Text(token), "\"")))
		tokens = tokens[1:]
		if len(tokens) == 0 || scanner.Text(tokens[0]) != "." {
			break
		}
		name.WriteByte('.')
		tokens = tokens[1:]
	}
	return name.String(), tokens
}

func settingValue(scanner *sqlscan.Scanner, tokens []sqlscan.Token) (string, bool) {
	// normalizes a SET value so equal settings compare equal however
	// they were written: quotes are dropped, words are lowercased, lists are joined.
	var (
		parts    []string
		sign     string
		lastWord bool
	)
	for _, token := range tokens {
		text := scanner.Text(token)
		lastWord = false
		switch token.Kind {
		case sqlscan.Punct:
			if text == "-" {
				sign = text
			}
			continue
		case sqlscan.String:
			if start := strings.IndexByte(text, '\''); start >= 0 && strings.HasSuffix(text, "'") && start < len(text)-1 {
				text = strings.ReplaceAll(text[start+1:len(text)-1], "''", "'")
			}
		case sqlscan.QuotedIdent:
			text = strings.Trim(text, "\"")
		case sqlscan.Word:
			text, lastWord = strings.ToLower(text), true
		}
		parts = append(parts, sign+text)
		sign = ""
	}
	// only the bare keywords mean "the default"; a quoted 'default' is a value
	if len(parts) == 1 && lastWord && (parts[0] == "default" || parts[0] == "local" || parts[0] == "none") {
		return "", true
	}
	return strings.Join(parts, ","), false
}
