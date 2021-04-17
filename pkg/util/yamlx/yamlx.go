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

package yamlx

import (
	"errors"
	"strings"
)

// KeyLookup is a lookup of keys available in the parsed yaml
type KeyLookup map[string]interface{}

// GetKeyList parses a YAML-formatted file and returns its list of fully-qualified key names.
// This assumes the yml blob has already been linted and is strictly valid
func GetKeyList(yml string) (KeyLookup, error) {

	lines := strings.Split(yml, "\n")
	keys := make(map[string]interface{})

	var lk depthLookup
	var depths []int
	var baseDepth = -1

	for _, line := range lines {
		if line == "" {
			continue
		}
		j := getIndentDepth(line)
		if j == -1 || j == len(line) {
			continue
		}
		key := getKeyword(line, j)
		if key == "" {
			continue
		}
		if baseDepth == -1 {
			baseDepth = j
		}
		if j == baseDepth {
			lk, depths = rootDepthData(j, key)
			keys[key] = nil
		} else {
			pd, err := getParentDepthData(j, depths, lk)
			if err != nil {
				return nil, err
			}
			key = pd.key + "." + key
			keys[key] = nil
			lk[j] = depthData{key: key, idx: pd.idx + 1, depth: j}
			depths = append(depths[:pd.idx+1], j)
		}
	}
	return keys, nil
}

func (k KeyLookup) IsDefined(s ...string) bool {
	_, ok := k[strings.Join(s, ".")]
	return ok
}

func getIndentDepth(line string) int {
	var depth int
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case 32: // count the number of spaces at the front of the line
			depth++
			continue
		case 35, 45: // next line if this one is a comment (#) or list item (-)
			return -1
		default:
			return depth // returns depth upon reaching the first non-space character
		}
	}
	return depth
}

type depthData struct {
	key        string
	idx, depth int
}

type depthLookup map[int]depthData

var errDepthNotInList = errors.New("depth not in list")

func getDepthData(depth int, dl depthLookup) (depthData, error) {
	if dd, ok := dl[depth]; ok {
		return dd, nil
	}
	return depthData{}, errDepthNotInList
}

var errEmptyDepthList = errors.New("empty depth list")

func getParentDepthData(depth int, l []int, dl depthLookup) (depthData, error) {

	if len(l) == 0 {
		return depthData{}, errEmptyDepthList
	}

	if len(l) == 1 {
		return getDepthData(0, dl)
	}

	var dd depthData
	var err error

	for i := len(l) - 1; i > -1; i-- {
		if l[i] >= depth {
			continue
		}
		dd, err = getDepthData(l[i], dl)
		break
	}
	return dd, err
}

func rootDepthData(depth int, key string) (depthLookup, []int) {
	depths := make([]int, 1, 16)
	depths[0] = depth
	l := depthLookup{depth: depthData{key: key, depth: depth}}
	return l, depths
}

func getKeyword(line string, j int) string {
	for i := j; i < len(line); i++ {
		if line[i] == 58 {
			return line[j:i]
		}
	}
	return ""
}
