package model

import (
	"github.com/trickstercache/trickster/v2/pkg/parsing/token"
	"github.com/trickstercache/trickster/v2/pkg/util/strings"
)

type DataType int

const (
	INTEGER = DataType(iota)
	INT
	SMALLINT
	TINYINT
	MEDIUMINT
	BIGINT

	DECIMAL
	NUMERIC

	FLOAT
	DOUBLE

	BIT

	DATE
	DATETIME
	TIMESTAMP
	TIME
	YEAR

	CHAR
	VARCHAR
	BINARY
	VARBINARY
	BLOB
	TEXT
	ENUM
	SET
)

var dts = []string{
	"integer", "smallint", "tinyint", "mediumint", "bigint",
	"decimal", "numeric",
	"float", "double",
	"bit",
	"date", "datetime", "timestamp", "time", "year",
	"char", "varchar", "binary", "varbinary", "blob", "text", "enum", "set",
}

func IsDataType(tk *token.Token) (string, bool) {
	return tk.Val, tk.Typ == token.Identifier && strings.IndexInSlice(dts, tk.Val) != -1
}

func (dt DataType) String() string {
	return dts[dt]
}

var afs = []string{
	"min", "max", "avg",
}

func IsAggregateFunction(tk *token.Token) (string, bool) {
	return tk.Val, tk.Typ == token.Identifier && strings.IndexInSlice(afs, tk.Val) != -1
}
