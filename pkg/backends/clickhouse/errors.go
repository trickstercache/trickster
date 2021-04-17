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

package clickhouse

import "errors"

// ErrLimitUnsupported indicates the input a LIMIT keyword, which is currently unsupported
// in the caching layer
var ErrLimitUnsupported = errors.New("limit queries are not supported")

// ErrUnsupportedOutputFormat indicates the FORMAT value for the query is not supported
var ErrUnsupportedOutputFormat = errors.New("unsupported output format requested")

// ErrInvalidWithClause indicates the WITH clause of the query is not properly formatted
var ErrInvalidWithClause = errors.New("invalid WITH expression list")

// ErrUnsupportedToStartOfFunc indicates the ToStartOf func used in the query is not supported by Trickster
var ErrUnsupportedToStartOfFunc = errors.New("unsupported ToStartOf* func")

// ErrNotAtPreWhere indicates AtPreWhere was called but the current token is not of type tokenPreWhere
var ErrNotAtPreWhere = errors.New("not at PREWHERE")
