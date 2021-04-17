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

// ErrInvalidGroupByClause indicates the GROUP BY clause of the query is not properly formatted
var ErrInvalidGroupByClause = errors.New("invalid GROUP BY expression list")

// ErrInvalidInputLength indicates the input length was invalid
var ErrInvalidInputLength = errors.New("invalid input length")
