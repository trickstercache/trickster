package mysql

import (
	"github.com/trickstercache/trickster/v2/pkg/parsing/lex"
	"github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
)

var lopts = LexerOptions()
var lexer = sql.NewLexer(lopts)

func LexerOptions() *lex.Options {
	return &lex.Options{
		SpacedKeywordHints: sql.SpacedKeywords(),
	}
}
