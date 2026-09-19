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

package postgres

import (
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlscan"
)

type wordClass uint8

const (
	// wordVolatile is a function whose result changes between identical calls.
	wordVolatile wordClass = iota + 1
	// wordClock reads the clock; as a time bound it is resolved, anywhere else it is volatile.
	wordClock
	// wordBareClock is a clock function written without parentheses.
	wordBareClock
	// wordUnfaithful is a keyword or type the parser drops or re-spells with another meaning.
	wordUnfaithful
	// wordGapfill fills empty buckets from the range it is asked for.
	wordGapfill
	// wordCarry reads values from outside the bucket it is reported in.
	wordCarry
	// wordSetReturning is a function that yields several rows per call.
	wordSetReturning
)

const unicodeEscapePrefix = "u&"

var guardedWords = map[string]wordClass{
	"random": wordVolatile, "random_normal": wordVolatile, "setseed": wordVolatile,
	"clock_timestamp": wordVolatile, "timeofday": wordVolatile,
	"gen_random_uuid": wordVolatile, "uuid_generate_v1": wordVolatile, "uuid_generate_v1mc": wordVolatile,
	"uuid_generate_v4": wordVolatile, "uuidv4": wordVolatile, "uuidv7": wordVolatile,
	"nextval": wordVolatile, "currval": wordVolatile, "lastval": wordVolatile, "setval": wordVolatile,
	"txid_current": wordVolatile, "pg_current_xact_id": wordVolatile, "pg_backend_pid": wordVolatile,
	"pg_sleep": wordVolatile, "pg_sleep_for": wordVolatile, "pg_sleep_until": wordVolatile,
	"set_config": wordVolatile, "current_setting": wordVolatile,
	"pg_advisory_lock": wordVolatile, "pg_try_advisory_lock": wordVolatile, "pg_advisory_unlock": wordVolatile,
	"pg_advisory_xact_lock": wordVolatile, "pg_try_advisory_xact_lock": wordVolatile,
	"pg_advisory_lock_shared": wordVolatile, "pg_advisory_unlock_all": wordVolatile,
	// functions called for what they do rather than what they return
	"pg_notify": wordVolatile, "pg_cancel_backend": wordVolatile, "pg_terminate_backend": wordVolatile,
	"pg_reload_conf": wordVolatile, "pg_switch_wal": wordVolatile, "pg_logical_emit_message": wordVolatile,
	"lo_create": wordVolatile, "lo_creat": wordVolatile, "lo_import": wordVolatile, "lo_export": wordVolatile,
	"lo_unlink": wordVolatile, "lo_put": wordVolatile, "lowrite": wordVolatile,
	"dblink": wordVolatile, "dblink_exec": wordVolatile,

	"now": wordClock, "statement_timestamp": wordClock, "transaction_timestamp": wordClock, "age": wordClock,
	"current_timestamp": wordBareClock, "current_date": wordBareClock, "current_time": wordBareClock,
	"localtimestamp": wordBareClock, "localtime": wordBareClock,

	"only": wordUnfaithful, "json": wordUnfaithful,

	fnTimeBucketGapfill: wordGapfill,
	"locf":              wordCarry, "interpolate": wordCarry,

	"generate_series": wordSetReturning, "generate_subscripts": wordSetReturning, "unnest": wordSetReturning,
	"regexp_matches": wordSetReturning, "regexp_split_to_table": wordSetReturning, "string_to_table": wordSetReturning,
	"json_each": wordSetReturning, "json_each_text": wordSetReturning, "json_array_elements": wordSetReturning,
	"json_array_elements_text": wordSetReturning, "json_object_keys": wordSetReturning,
	"jsonb_each": wordSetReturning, "jsonb_each_text": wordSetReturning, "jsonb_array_elements": wordSetReturning,
	"jsonb_array_elements_text": wordSetReturning, "jsonb_object_keys": wordSetReturning,
	"jsonb_path_query": wordSetReturning,
}

type statementFacts struct {
	volatile     bool
	clock        bool
	unfaithful   bool
	gapfill      bool
	carries      bool
	setReturning bool
}

func scanFacts(sql string) statementFacts {
	// finds the guarded words of a statement outside quotes and comments.
	// A function name counts only when a call follows it.
	var facts statementFacts
	scanner := sqlscan.New(sql, sqlscan.Options{})
	pending := wordClass(0)
	for {
		token, more := scanner.Next()
		if !more {
			return facts
		}
		called := token.Kind == sqlscan.Punct && sql[token.Start] == '('
		switch {
		case !called:
		case pending == wordVolatile:
			facts.volatile = true
		case pending == wordClock:
			facts.clock = true
		case pending == wordGapfill:
			facts.gapfill = true
		case pending == wordCarry:
			facts.carries = true
		case pending == wordSetReturning:
			facts.setReturning = true
		}
		pending = 0
		switch token.Kind {
		case sqlscan.Word:
			// the map is keyed in lower case; most words are already
			word := sql[token.Start:token.End]
			class, ok := guardedWords[word]
			if !ok {
				class = guardedWords[strings.ToLower(word)]
			}
			switch class {
			case wordBareClock:
				facts.clock = true
			case wordUnfaithful:
				facts.unfaithful = true
			default:
				pending = class
			}
		case sqlscan.QuotedIdent:
			// a quoted name calls the same function; built-in names are lower case
			if class := guardedWords[strings.Trim(sql[token.Start:token.End], `"`)]; class != wordUnfaithful {
				pending = class
			}
			fallthrough
		case sqlscan.String:
			if token.End-token.Start > len(unicodeEscapePrefix) &&
				strings.EqualFold(sql[token.Start:token.Start+len(unicodeEscapePrefix)], unicodeEscapePrefix) {
				facts.unfaithful = true
			}
		}
	}
}
