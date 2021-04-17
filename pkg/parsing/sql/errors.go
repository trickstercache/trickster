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

import "errors"

// ErrNotAtWith is an error for when the RunState expected, but did not get, a WITH token
var ErrNotAtWith = errors.New("not at a WITH token")

// ErrNotAtSelect is an error for when the RunState expected, but did not get, a SELECT token
var ErrNotAtSelect = errors.New("not at a SELECT token")

// ErrNotAtFrom is an error for when the RunState expected, but did not get, a FROM token
var ErrNotAtFrom = errors.New("not at a FROM token")

// ErrNotAtWhere is an error for when the RunState expected, but did not get, a WHERE token
var ErrNotAtWhere = errors.New("not at a WHERE token")

// ErrNotAtGroupBy is an error for when the RunState expected, but did not get, a GROUP BY token
var ErrNotAtGroupBy = errors.New("not at a GROUP BY token")

// ErrNotAtHaving is an error for when the RunState expected, but did not get, a HAVING token
var ErrNotAtHaving = errors.New("not at a HAVING token")

// ErrNotAtOrderBy is an error for when the RunState expected, but did not get, an ORDER BY token
var ErrNotAtOrderBy = errors.New("not at an ORDER BY token")

// ErrEmptyGroupBy is an error for when a GROUP BY clause is missing or has no columngs
var ErrEmptyGroupBy = errors.New("missing or empty GROUP BY section")

// ErrNotAtLimit is an error for when the RunState expected, but did not get, a LIMIT token
var ErrNotAtLimit = errors.New("not at a LIMIT token")
