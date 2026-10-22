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

package session

import (
	"bytes"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

const tokenOverhead = 16

type tokenCache struct {
	max     uint64
	size    uint64
	byAlias map[uint64]wire.Token
}

func (c *tokenCache) apply(t wire.Token, inSetup bool) (wire.Token, error) {
	// every token the peer sends resolves to a use-value form; registrations and deletions update the cache
	// whose size this endpoint advertised in MAX_AUTH_TOKEN_CACHE_SIZE
	switch t.AliasType {
	case wire.TokenUseValue:
		return t, nil
	case wire.TokenRegister:
		if _, dup := c.byAlias[t.Alias]; dup {
			return t, &message.SessionError{Code: message.CodeDuplicateAuthTokenAlias, Reason: "token alias in use"}
		}
		value := wire.Token{AliasType: wire.TokenUseValue, Type: t.Type, Value: bytes.Clone(t.Value)}
		need := uint64(tokenOverhead + len(t.Value))
		if c.size+need > c.max {
			if inSetup {
				// a SETUP registration past the limit is used once rather than failing the session
				return value, nil
			}
			return t, &message.SessionError{Code: message.CodeAuthTokenCacheOverflow, Reason: "token cache full"}
		}
		if c.byAlias == nil {
			c.byAlias = make(map[uint64]wire.Token)
		}
		c.byAlias[t.Alias] = value
		c.size += need
		return value, nil
	case wire.TokenUseAlias:
		v, ok := c.byAlias[t.Alias]
		if !ok {
			return t, &message.SessionError{Code: message.CodeUnknownAuthTokenAlias, Reason: "unknown token alias"}
		}
		return v, nil
	case wire.TokenDelete:
		v, ok := c.byAlias[t.Alias]
		if !ok {
			return t, &message.SessionError{Code: message.CodeUnknownAuthTokenAlias, Reason: "unknown token alias"}
		}
		delete(c.byAlias, t.Alias)
		c.size -= uint64(tokenOverhead + len(v.Value))
		return t, nil
	}
	return t, &message.SessionError{Code: message.CodeMalformedAuthToken, Reason: "unknown alias type"}
}
