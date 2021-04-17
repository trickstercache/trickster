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

package copiers

// CopyBytes returns an exact copy of the byte slice
func CopyBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	clone := make([]byte, len(b))
	copy(clone, b)
	return clone
}

// CopyStrings returns an exact copy of the string slice
func CopyStrings(s []string) []string {
	if s == nil {
		return nil
	}
	clone := make([]string, len(s))
	copy(clone, s)
	return clone
}

// CopyInterfaces returns an exact copy of the Interface slice
// note if the underlying interface value is a Pointer, this will
// be a shallow copy
func CopyInterfaces(i []interface{}) []interface{} {
	if i == nil {
		return nil
	}
	clone := make([]interface{}, len(i))
	copy(clone, i)
	return clone
}

// CopyLookup returns an exact copy of the lookup map
func CopyLookup(l map[string]interface{}) map[string]interface{} {
	if l == nil {
		return nil
	}
	clone := make(map[string]interface{})
	for k := range l {
		clone[k] = nil
	}
	return clone
}

// CopyStringLookup returns an exact copy of the lookup map
func CopyStringLookup(l map[string]string) map[string]string {
	if l == nil {
		return nil
	}
	clone := make(map[string]string)
	for k, v := range l {
		clone[k] = v
	}
	return clone
}

// LookupFromStrings retrurns a lookup map from a list of keys
func LookupFromStrings(s []string) map[string]interface{} {
	if s == nil {
		return nil
	}
	clone := make(map[string]interface{})
	for _, v := range s {
		clone[v] = nil
	}
	return clone
}
