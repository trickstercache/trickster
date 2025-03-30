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

package level

import "strings"

type Level string
type LevelID int

const (
	Debug Level = "debug"
	Info  Level = "info"
	Warn  Level = "warn"
	Error Level = "error"
	Fatal Level = "fatal"

	DebugID LevelID = 1
	InfoID  LevelID = 2
	WarnID  LevelID = 3
	ErrorID LevelID = 4
	TraceID LevelID = 5
)

var validLevels = map[Level]LevelID{
	Debug: DebugID,
	Info:  InfoID,
	Warn:  WarnID,
	Error: ErrorID,
	Fatal: TraceID,
}

func GetLevelID(logLevel Level) LevelID {
	if i, ok := validLevels[Level(strings.ToLower(string(logLevel)))]; ok {
		return i
	}
	return 0
}
