/*
 * Copyright 2026 The Trickster Authors
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

package questdb

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlscan"
)

var errUnsupportedSampleBy = errors.New("unsupported QuestDB SAMPLE BY clause")

type sampleByClause struct {
	sql string
}

type sampleByRewriter struct{}

var _ cockroach.ClauseRewriter = sampleByRewriter{}

func (sampleByRewriter) Lift(statement string) (string, *cockroach.LiftedClause, error) {
	start, byEnd, ok := cockroach.FindKeywords(statement, "SAMPLE", "BY")
	if !ok {
		return statement, nil, nil
	}
	clauseEnd := sampleByEnd(statement, byEnd)
	if clauseEnd <= byEnd {
		return "", nil, errUnsupportedSampleBy
	}
	body := statement[byEnd:clauseEnd]
	step, err := parseSampleByBody(body)
	if err != nil {
		return "", nil, err
	}
	rewritten := statement[:start] + statement[clauseEnd:]
	if _, _, duplicate := cockroach.FindKeywords(rewritten, "SAMPLE", "BY"); duplicate {
		return "", nil, fmt.Errorf("%w: multiple SAMPLE BY clauses", errUnsupportedSampleBy)
	}
	return rewritten, &cockroach.LiftedClause{
		Payload: sampleByClause{sql: strings.TrimSpace(statement[start:clauseEnd])},
		Bucket:  &cockroach.BucketMatch{Step: step},
		// QuestDB applies SAMPLE BY to the designated timestamp, which is
		// the first plain column in the select list. The shared analyzer
		// rejects any other shape instead of guessing.
		InferTimeColumn:  true,
		ImplicitGrouping: true,
	}, nil
}

func (sampleByRewriter) Restore(rendered string, lifted *cockroach.LiftedClause) (string, error) {
	clause, ok := lifted.Payload.(sampleByClause)
	if !ok || clause.sql == "" {
		return "", errUnsupportedSampleBy
	}
	return cockroach.InsertClause(rendered, clause.sql, "ORDER BY", "LIMIT", "OFFSET", "FETCH"), nil
}

func sampleByEnd(statement string, offset int) int {
	scanner := sqlscan.New(statement[offset:], sqlscan.Options{})
	for {
		token, ok := scanner.Next()
		if !ok {
			return len(statement)
		}
		if token.Depth != 0 || token.Kind != sqlscan.Word {
			continue
		}
		switch strings.ToLower(scanner.Text(token)) {
		case "order", "limit", "offset", "fetch":
			return offset + token.Start
		}
	}
}

func parseSampleByBody(body string) (time.Duration, error) {
	scanner := sqlscan.New(body, sqlscan.Options{})
	first, ok := scanner.Next()
	if !ok || first.Kind != sqlscan.Number {
		return 0, errUnsupportedSampleBy
	}
	step, ok := parseSampleInterval(scanner.Text(first))
	if !ok {
		return 0, errUnsupportedSampleBy
	}
	token, more := scanner.Next()
	if !more {
		return step, nil
	}
	if token.Kind != sqlscan.Word || !strings.EqualFold(scanner.Text(token), "fill") {
		return 0, errUnsupportedSampleBy
	}
	open, more := scanner.Next()
	if !more || open.Kind != sqlscan.Punct || scanner.Text(open) != "(" {
		return 0, errUnsupportedSampleBy
	}
	value, more := scanner.Next()
	if !more || value.Kind != sqlscan.Word || !strings.EqualFold(scanner.Text(value), "null") {
		return 0, errUnsupportedSampleBy
	}
	close, more := scanner.Next()
	if !more || close.Kind != sqlscan.Punct || scanner.Text(close) != ")" {
		return 0, errUnsupportedSampleBy
	}
	// QuestDB fills only the gaps between observations in the selected
	// range. The generated NULL rows therefore depend on the surrounding
	// range and cannot be merged from independent delta extents.
	return 0, errUnsupportedSampleBy
}

func parseSampleInterval(value string) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	unit := value[len(value)-1]
	digits := value[:len(value)-1]
	if digits == "" {
		return 0, false
	}
	var multiplier time.Duration
	switch unit {
	case 's':
		multiplier = time.Second
	case 'm':
		multiplier = time.Minute
	case 'h':
		multiplier = time.Hour
	case 'd':
		multiplier = 24 * time.Hour
	default:
		return 0, false
	}
	maxDuration := int64(^uint64(0) >> 1)
	maxNumber := maxDuration / int64(multiplier)
	var number int64
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return 0, false
		}
		value := int64(digit - '0')
		if number > (maxNumber-value)/10 {
			return 0, false
		}
		number = number*10 + value
	}
	if number <= 0 {
		return 0, false
	}
	return time.Duration(number) * multiplier, true
}
