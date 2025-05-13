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

package model

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func marshalTimeseriesXSV(w io.Writer, ds *dataset.DataSet,
	rlo *timeseries.RequestOptions, writeNames bool, writeTypes bool,
	separator byte) error {
	fds, tags, vals, tfd := ds.FieldDefinitions()

	var ctPart, fmtPart string
	switch separator {
	case '\t':
		ctPart = "tab"
		fmtPart = "TSV"
	default:
		ctPart = "comma"
		fmtPart = "CSV"
		separator = ','
	}

	ctHeader := "text/" + ctPart + "-separated-values; charset=UTF-8"
	var fmtHeader string

	switch {
	case writeTypes:
		fmtHeader = fmtPart + "WithNamesAndTypes"
	case writeNames:
		fmtHeader = fmtPart + "WithNames"
	default:
		fmtHeader = fmtPart
	}

	if hw, ok := w.(http.ResponseWriter); ok && hw != nil {
		hw.Header().Set(headers.NameContentType, ctHeader)
		hw.Header().Set(formatHeader, fmtHeader)
	}

	if (len(tags) == 0 && len(vals) == 0) || tfd.DataType < 1 {
		return timeseries.ErrNoTimerangeQuery
	}

	if len(ds.Results) == 0 {
		return nil
	}

	fieldCount := len(fds)
	if tfd.OutputPosition > fieldCount {
		return timeseries.ErrTableHeader
	}

	lookup := make(map[string]timeseries.FieldDefinition)
	for _, fd := range vals {
		lookup[fd.Name] = fd
	}

	// at this point, we're going to write the TSV/CSV
	cw := csv.NewWriter(w)
	cw.Comma = rune(separator)
	if writeNames || writeTypes {
		row := make([]string, fieldCount)
		fd := tfd
		row[fd.OutputPosition] = fd.Name
		for _, fd = range tags {
			if fd.Name == tfd.Name {
				continue
			}
			if fd.OutputPosition > fieldCount {
				continue
			}
			row[fd.OutputPosition] = fd.Name
		}
		for _, fd = range vals {
			row[fd.OutputPosition] = fd.Name
		}
		cw.Write(row)
	}
	if writeTypes {
		row := make([]string, fieldCount)
		fd := tfd
		row[fd.OutputPosition] = fd.SDataType
		for _, fd = range tags {
			if fd.Name == tfd.Name {
				continue
			}
			if fd.OutputPosition > fieldCount {
				continue
			}
			row[fd.OutputPosition] = fd.SDataType
		}
		for _, fd = range vals {
			row[fd.OutputPosition] = fd.SDataType
		}
		cw.Write(row)
	}
	for _, s := range ds.Results[0].SeriesList {
		for _, p := range s.Points {
			row := make([]string, fieldCount)
			var i int
			for _, fd := range fds {
				if fd.OutputPosition >= fieldCount || fd.OutputPosition < 0 {
					continue
				}
				switch fd.Role {
				case timeseries.RoleTimestamp:
					row[fd.OutputPosition] = p.Epoch.Format(fd.DataType, false)
				case timeseries.RoleUntracked:
					if fd.DefaultValue != "" {
						row[fd.OutputPosition] = fd.DefaultValue
					}
				case timeseries.RoleTag:
					row[fd.OutputPosition] = s.Header.Tags[fd.Name]
				case timeseries.RoleValue:
					if i < len(p.Values) {
						row[fd.OutputPosition] = fmt.Sprintf("%v", p.Values[i])
						i++
					}
				}
			}
			cw.Write(row)
		}
	}
	cw.Flush()
	return nil
}
