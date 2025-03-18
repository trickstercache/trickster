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
