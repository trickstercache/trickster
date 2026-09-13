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

package options

// Input sources a rule can read from the request
const (
	SourceMethod      = "method"
	SourceURL         = "url"
	SourceURLNoParams = "url_no_params"
	SourceScheme      = "scheme"
	SourceHost        = "host"
	SourceHostname    = "hostname"
	SourcePort        = "port"
	SourcePath        = "path"
	SourceParams      = "params"
	SourceParam       = "param"
	SourceHeader      = "header"
	SourceHasParam    = "has_param"
	SourceHasHeader   = "has_header"
)

// Input types a rule can evaluate its source as
const (
	TypeString = "string"
	TypeNum    = "num"
	TypeBool   = "bool"
)

// Input encodings a rule can decode its source from
const (
	EncodingBase64 = "base64"
)

// Operations a rule can apply to its input; NegatePrefix ahead of a boolean
// operation inverts its result
const (
	OpEqual          = "eq"
	OpRegexMatch     = "rmatch"
	OpContains       = "contains"
	OpPrefix         = "prefix"
	OpSuffix         = "suffix"
	OpMD5            = "md5"
	OpSHA1           = "sha1"
	OpBase64         = "base64"
	OpModulo         = "modulo"
	OpGreaterThan    = "gt"
	OpLessThan       = "lt"
	OpGreaterOrEqual = "ge"
	OpLessOrEqual    = "le"
	OpBetween        = "bt"
	NegatePrefix     = "!"
)

// Results of a boolean operation, which a case's matches compare against
const (
	ValueTrue  = "true"
	ValueFalse = "false"
)
