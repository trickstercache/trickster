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

package rule

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/encoding/base64"
	"github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/checksum/sha1"
)

type operation string
type operationFunc func(input string, arg string, negate bool) string

var compiledRegexes = make(map[string]*regexp.Regexp)

var operationFuncs = map[operation]operationFunc{

	"string-rmatch":   opStringRMatch,
	"string-eq":       opStringEquality,
	"string-contains": opStringContains,
	"string-prefix":   opStringPrefix,
	"string-suffix":   opStringSuffix,
	// TODO: understand use case and implementation for these string funcs
	"string-md5":    opStringMD5,
	"string-sha1":   opStringSHA1,
	"string-base64": opStringBase64,
	"string-modulo": opStringModulo,

	"num-eq": opNumEquality,
	"num-gt": opNumGreaterThan,
	"num-lt": opNumLessThan,
	"num-ge": opNumGreaterThanEqual,
	"num-le": opNumLessThanEqual,
	"num-bt": opNumBetween,
	// TODO: understand use case and implementation for these num funcs
	"num-modulo": opNumModulo,

	"bool-eq": opBoolEquality,
}

func btos(t bool, negate bool) string {

	if negate {
		t = !t
	}
	if t {
		return "true"
	}
	return "false"
}

func opStringRMatch(input, arg string, negate bool) string {
	re, ok := compiledRegexes[arg]
	if !ok {
		var err error
		re, err = regexp.Compile(arg)
		if err != nil {
			compiledRegexes[arg] = nil
			return "false"
		}
		compiledRegexes[arg] = re
	}
	if re != nil && re.Match([]byte(input)) {
		return "true"
	}
	return "false"
}

func opStringEquality(input, arg string, negate bool) string {
	return btos(input == arg, negate)
}

func opStringContains(input, arg string, negate bool) string {
	return btos(strings.Contains(input, arg), negate)
}

func opStringPrefix(input, arg string, negate bool) string {
	return btos(strings.HasPrefix(input, arg), negate)
}

func opStringSuffix(input, arg string, negate bool) string {
	return btos(strings.HasSuffix(input, arg), negate)
}

func opStringMD5(input, arg string, negate bool) string {
	return md5.Checksum(input)
}

func opStringSHA1(input, arg string, negate bool) string {
	return sha1.Checksum(input)
}

func opStringBase64(input, arg string, negate bool) string {
	return base64.Encode(input)
}

func opStringModulo(input, arg string, negate bool) string {
	d, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return ""
	}
	bytes := []byte(input)
	var sum int64
	for _, i := range bytes {
		sum += int64(i)
	}
	return strconv.FormatInt(sum%d, 10)
}

func areNums(input1, input2 string) (float64, float64, bool) {
	out1, err := strconv.ParseFloat(input1, 64)
	if err != nil {
		return 0, 0, false
	}
	out2, err := strconv.ParseFloat(input2, 64)
	if err != nil {
		return 0, 0, false
	}
	return out1, out2, true
}

func opNumEquality(input, arg string, negate bool) string {
	if i, a, ok := areNums(input, arg); ok {
		t := i == a
		return btos(t, negate)
	}
	return ""
}

func opNumGreaterThan(input, arg string, negate bool) string {
	if i, a, ok := areNums(input, arg); ok {
		t := i > a
		return btos(t, negate)
	}
	return ""
}

func opNumLessThan(input, arg string, negate bool) string {
	if i, a, ok := areNums(input, arg); ok {
		t := i < a
		return btos(t, negate)
	}
	return ""
}

func opNumGreaterThanEqual(input, arg string, negate bool) string {
	if i, a, ok := areNums(input, arg); ok {
		t := i >= a
		return btos(t, negate)
	}
	return ""
}

func opNumLessThanEqual(input, arg string, negate bool) string {
	if i, a, ok := areNums(input, arg); ok {
		t := i <= a
		return btos(t, negate)
	}
	return ""
}

func opNumBetween(input, arg string, negate bool) string {
	h := strings.Index(arg, "-")
	if h < 1 {
		return ""
	}
	start := arg[:h]
	end := arg[h+1:]
	i, err := strconv.ParseFloat(input, 64)
	if err != nil {
		return ""
	}
	if s, e, ok := areNums(start, end); ok {
		t := i >= s && i <= e
		return btos(t, negate)
	}
	return ""
}

func opNumModulo(input, arg string, unused bool) string {
	if i, a, ok := areNums(input, arg); ok {
		return strconv.FormatInt(int64(i)%int64(a), 10)
	}
	return ""
}

func areBools(input1, input2 string) (bool, bool, bool) {
	out1, err := strconv.ParseBool(input1)
	if err != nil {
		return false, false, false
	}
	out2, err := strconv.ParseBool(input2)
	if err != nil {
		return false, false, false
	}
	return out1, out2, true
}

func opBoolEquality(input, arg string, negate bool) string {
	if i, a, ok := areBools(input, arg); ok {
		t := i == a
		return btos(t, negate)
	}
	return ""
}
