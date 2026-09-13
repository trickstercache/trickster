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
	"strconv"
	"strings"

	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/checksum/sha1"
	"github.com/trickstercache/trickster/v2/pkg/encoding/base64"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/matching"
)

type (
	operation     string
	operationFunc func(input string, arg string, negate bool) string
)

func operationKey(inputType, op string) operation {
	return operation(inputType + "-" + op)
}

// opStringRMatch is bound to its compiled expression at parse time rather
// than looked up here, so it has no entry in this table
var opStringRMatch = operationKey(ro.TypeString, ro.OpRegexMatch)

var operationFuncs = map[operation]operationFunc{
	operationKey(ro.TypeString, ro.OpEqual):    opStringEquality,
	operationKey(ro.TypeString, ro.OpContains): opStringContains,
	operationKey(ro.TypeString, ro.OpPrefix):   opStringPrefix,
	operationKey(ro.TypeString, ro.OpSuffix):   opStringSuffix,
	operationKey(ro.TypeString, ro.OpMD5):      opStringMD5,
	operationKey(ro.TypeString, ro.OpSHA1):     opStringSHA1,
	operationKey(ro.TypeString, ro.OpBase64):   opStringBase64,
	operationKey(ro.TypeString, ro.OpModulo):   opStringModulo,

	operationKey(ro.TypeNum, ro.OpEqual):          opNumEquality,
	operationKey(ro.TypeNum, ro.OpGreaterThan):    opNumGreaterThan,
	operationKey(ro.TypeNum, ro.OpLessThan):       opNumLessThan,
	operationKey(ro.TypeNum, ro.OpGreaterOrEqual): opNumGreaterThanEqual,
	operationKey(ro.TypeNum, ro.OpLessOrEqual):    opNumLessThanEqual,
	operationKey(ro.TypeNum, ro.OpBetween):        opNumBetween,
	operationKey(ro.TypeNum, ro.OpModulo):         opNumModulo,

	operationKey(ro.TypeBool, ro.OpEqual): opBoolEquality,
}

func btos(t bool, negate bool) string {
	if negate {
		t = !t
	}
	if t {
		return ro.ValueTrue
	}
	return ro.ValueFalse
}

// regexOperation binds a compiled expression to the rule that declared it
func regexOperation(re *matching.Regex) operationFunc {
	return func(input, _ string, negate bool) string {
		return btos(re.Match(input), negate)
	}
}

func opStringEquality(input, arg string, negate bool) string {
	return btos(matching.Exact(arg).Match(input), negate)
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

func opStringMD5(input, _ string, _ bool) string {
	return md5.Checksum(input)
}

func opStringSHA1(input, _ string, _ bool) string {
	return sha1.Checksum(input)
}

func opStringBase64(input, _ string, _ bool) string {
	return base64.Encode(input)
}

func opStringModulo(input, arg string, _ bool) string {
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

func opNumModulo(input, arg string, _ bool) string {
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
