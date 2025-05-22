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

type Level string
type ID int

const (
	Debug Level = "debug"
	Info  Level = "info"
	Warn  Level = "warn"
	Error Level = "error"
	Fatal Level = "fatal"

	DebugID ID = 1
	InfoID  ID = 2
	WarnID  ID = 3
	ErrorID ID = 4
	TraceID ID = 5
)

func GetID(logLevel Level) ID {
	switch logLevel {
	case Debug:
		return DebugID
	case Info:
		return InfoID
	case Warn:
		return WarnID
	case Error:
		return ErrorID
	case Fatal:
		return TraceID
	}
	return 0
}
