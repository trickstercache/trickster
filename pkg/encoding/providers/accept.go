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

package providers

import (
	"math"
	"math/bits"
	"strconv"
	"strings"
)

const (
	// maxWeight is a weight of 1, in the thousandths that a qvalue has the precision for
	maxWeight = 1000
	// numWebProviders is the number of encodings a client can be sent
	numWebProviders = 4
)

// Accepted is the supported encodings a client would rather be sent than identity, most preferred
// first: by its weights, then by Trickster's preference among equals. As a value, it allocates nothing.
type Accepted struct {
	list    [numWebProviders]Provider
	weights [numWebProviders]uint16
	n       int
}

const (
	identityValue = "identity"
	wildcardValue = "*"
	// unweighted marks identity or the wildcard as absent from the header
	unweighted = -1
)

// ParseAcceptEncoding returns what the values of an Accept-Encoding header accept. A coding
// takes the weight of its most specific match: its own entry, or else the wildcard's. One the
// client weights below identity is left out, as identity is always there to be sent instead.
func ParseAcceptEncoding(values ...string) Accepted {
	var named [numWebProviders]int
	for i := range named {
		named[i] = unweighted
	}
	identity, wildcard := unweighted, unweighted
	for _, value := range values {
		for token := range strings.SplitSeq(value, ",") {
			name, params, _ := strings.Cut(token, ";")
			// codings are case-insensitive; lowering one that is already lowercase copies nothing
			name = strings.ToLower(strings.TrimSpace(name))
			switch enc, ok := webProviderLookup[name]; {
			case ok:
				// the first of a coding's entries is the one that counts
				if i := providerIndex(enc); named[i] == unweighted {
					named[i] = int(parseWeight(params))
				}
			case name == identityValue && identity == unweighted:
				identity = int(parseWeight(params))
			case name == wildcardValue && wildcard == unweighted:
				wildcard = int(parseWeight(params))
			}
		}
	}
	// identity not named takes the wildcard's weight, and with neither it is acceptable but
	// least preferred: a client that lists codings is asking for them
	if identity == unweighted {
		identity = max(wildcard, 0)
	}
	var a Accepted
	for i, weight := range named {
		if weight == unweighted {
			weight = wildcard
		}
		if weight > 0 && weight >= identity {
			a.insert(Provider(1)<<i, uint16(weight))
		}
	}
	return a
}

// providerIndex returns the position of a web provider's single bit
func providerIndex(enc Provider) int {
	return bits.TrailingZeros8(uint8(enc))
}

// parseWeight returns a coding's qvalue in thousandths. A missing or malformed one is
// a weight of 1, as is the default, rather than a reason to refuse a coding that was named.
func parseWeight(params string) uint16 {
	for params != "" {
		var param string
		param, params, _ = strings.Cut(params, ";")
		k, v, ok := strings.Cut(param, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || q < 0 || math.IsNaN(q) {
			return maxWeight
		}
		return uint16(min(q, 1)*maxWeight + 0.5)
	}
	return maxWeight
}

// insert places enc by weight, and among equal weights by Trickster's preference,
// which is the order of the providers' values
func (a *Accepted) insert(enc Provider, weight uint16) {
	i := a.n
	for i > 0 && (a.weights[i-1] < weight || (a.weights[i-1] == weight && a.list[i-1] > enc)) {
		a.list[i], a.weights[i] = a.list[i-1], a.weights[i-1]
		i--
	}
	a.list[i], a.weights[i] = enc, weight
	a.n++
}

// Len returns the number of accepted encodings
func (a Accepted) Len() int {
	return a.n
}

// At returns the i'th most preferred encoding
func (a Accepted) At(i int) Provider {
	return a.list[i]
}

// Preferred returns the most preferred encoding, or Identity when none is accepted
func (a Accepted) Preferred() Provider {
	if a.n == 0 {
		return Identity
	}
	return a.list[0]
}

// Bitmap returns the accepted encodings as a bitmap
func (a Accepted) Bitmap() Provider {
	var b Provider
	for _, enc := range a.list[:a.n] {
		b |= enc
	}
	return b
}

// Filter returns the accepted encodings that are also in the bitmap, in the same order
func (a Accepted) Filter(bitmap Provider) Accepted {
	var out Accepted
	for i, enc := range a.list[:a.n] {
		if bitmap&enc != 0 {
			out.list[out.n], out.weights[out.n] = enc, a.weights[i]
			out.n++
		}
	}
	return out
}

// String returns an Accept-Encoding header value that asks for what the client did
// of what Trickster supports, keeping the client's weights where it gave any
func (a Accepted) String() string {
	weighted := false
	for _, weight := range a.weights[:a.n] {
		weighted = weighted || weight != maxWeight
	}
	if !weighted {
		// the preference order of equals is the order of the bitmap, whose values are prebuilt
		return bitmapHeaderValues[a.Bitmap()]
	}
	var sb strings.Builder
	for i, enc := range a.list[:a.n] {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(providerValLookup[enc])
		if weight := a.weights[i]; weight != maxWeight {
			sb.WriteString(";q=0.")
			digits := strconv.Itoa(int(weight) + maxWeight)[1:]
			sb.WriteString(strings.TrimRight(digits, "0"))
		}
	}
	return sb.String()
}

// bitmapHeaderValues holds the header value of every combination of web providers
var bitmapHeaderValues [maxWebProvider << 1]string

func init() {
	for b := range Provider(len(bitmapHeaderValues)) {
		names := make([]string, 0, numWebProviders)
		for p := Provider(1); p <= maxWebProvider; p <<= 1 {
			if b&p != 0 {
				names = append(names, providerValLookup[p])
			}
		}
		bitmapHeaderValues[b] = strings.Join(names, ", ")
	}
}
