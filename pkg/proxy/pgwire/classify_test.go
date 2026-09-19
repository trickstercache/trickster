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

package pgwire

import "testing"

const (
	varExtraFloatDigits = "extra_float_digits"
	varTenant           = "app.tenant"
)

func TestClassifyKinds(t *testing.T) {
	for sql, want := range map[string]statementClass{
		"":                                 {kind: stmtEmpty},
		"-- ping":                          {kind: stmtEmpty},
		" /* only */ ; ;":                  {kind: stmtEmpty},
		"SELECT 1":                         {kind: stmtRead},
		"select 1;":                        {kind: stmtRead},
		"(SELECT 1) UNION (SELECT 2)":      {kind: stmtRead},
		"WITH x AS (SELECT 1) TABLE x":     {kind: stmtRead},
		"VALUES (1)":                       {kind: stmtRead},
		"SELECT ';' ; ":                    {kind: stmtRead},
		"SELECT 1; SELECT 2":               {kind: stmtRead, multi: true},
		"BEGIN; SELECT 1; COMMIT":          {kind: stmtNeutral, multi: true},
		"SELECT 1; SET x = 1":              {kind: stmtRead, multi: true, unsafe: true},
		"INSERT INTO t VALUES (1)":         {kind: stmtNeutral},
		"SHOW TimeZone":                    {kind: stmtNeutral},
		"COPY t FROM STDIN":                {kind: stmtNeutral},
		"SELECT set_config('a','b',false)": {kind: stmtRead, unsafe: true},
		"SELECT pg_catalog.SET_CONFIG('a','b',true)": {kind: stmtRead, unsafe: true},
		"SELECT 'set_config'":                        {kind: stmtRead},
		"SELECT * INTO TEMP x FROM t":                {kind: stmtRead, unsafe: true},
		"SELECT (SELECT 1 INTO y) FROM t":            {kind: stmtRead},
		"CREATE TEMP TABLE x (i int)":                {kind: stmtOther, unsafe: true},
		"DO $$ BEGIN PERFORM 1; END $$":              {kind: stmtOther, unsafe: true},
		"CALL p()":                                   {kind: stmtOther, unsafe: true},
		"42":                                         {kind: stmtOther, unsafe: true},
		"SELECT 'unterminated":                       {kind: stmtRead, unsafe: true},
		"DISCARD ALL":                                {kind: stmtDiscardAll},
		"DISCARD PLANS":                              {kind: stmtNeutral},
		"DISCARD":                                    {kind: stmtNeutral},
	} {
		if got := classify(sql, false); got != want {
			t.Errorf("%q: got %+v, want %+v", sql, got, want)
		}
	}
}

func TestClassifySettings(t *testing.T) {
	for sql, want := range map[string]statementClass{
		"SET extra_float_digits = 3":                           {kind: stmtSet, name: varExtraFloatDigits, value: "3"},
		"set EXTRA_FLOAT_DIGITS to '3'":                        {kind: stmtSet, name: varExtraFloatDigits, value: "3"},
		"SET extra_float_digits = -1":                          {kind: stmtSet, name: varExtraFloatDigits, value: "-1"},
		"SET SESSION extra_float_digits TO 0":                  {kind: stmtSet, name: varExtraFloatDigits, value: "0"},
		"SET LOCAL extra_float_digits TO 0":                    {kind: stmtSet, name: varExtraFloatDigits, value: "0", local: true},
		"SET extra_float_digits TO DEFAULT":                    {kind: stmtSet, name: varExtraFloatDigits, isDefault: true},
		"SET bytea_output = 'default'":                         {kind: stmtSet, name: "bytea_output", value: "default"},
		"SET search_path TO \"My\", public":                    {kind: stmtSet, name: varSearchPath, value: "My,public"},
		"SET TIME ZONE 'America/New_York'":                     {kind: stmtSet, name: varTimeZone, value: "America/New_York"},
		"SET TIME ZONE LOCAL":                                  {kind: stmtSet, name: varTimeZone, isDefault: true},
		"SET timezone = 'it''s'":                               {kind: stmtSet, name: varTimeZone, value: "it's"},
		"SET ROLE analyst":                                     {kind: stmtSet, name: varRole, value: "analyst"},
		"SET ROLE NONE":                                        {kind: stmtSet, name: varRole, isDefault: true},
		"SET SESSION AUTHORIZATION 'bob'":                      {kind: stmtSet, name: varSessionAuthorization, value: "bob"},
		"SET SESSION AUTHORIZATION DEFAULT":                    {kind: stmtSet, name: varSessionAuthorization, isDefault: true},
		"SET NAMES 'UTF8'":                                     {kind: stmtSet, name: varClientEncoding, value: "UTF8"},
		"SET SCHEMA 'metrics'":                                 {kind: stmtSet, name: varSearchPath, value: "metrics"},
		"SET app.tenant = '42'":                                {kind: stmtSet, name: varTenant, value: "42"},
		"SET \"App\".Tenant = 42":                              {kind: stmtSet, name: varTenant, value: "42"},
		"SET TRANSACTION ISOLATION LEVEL SERIALIZABLE":         {kind: stmtNeutral},
		"SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY": {kind: stmtNeutral},
		"SET CONSTRAINTS ALL DEFERRED":                         {kind: stmtNeutral},
		"RESET extra_float_digits":                             {kind: stmtReset, name: varExtraFloatDigits, isDefault: true},
		"RESET ALL":                                            {kind: stmtReset, name: varAll, isDefault: true},
		"RESET TIME ZONE":                                      {kind: stmtReset, name: varTimeZone, isDefault: true},
		"RESET SESSION AUTHORIZATION":                          {kind: stmtReset, name: varSessionAuthorization, isDefault: true},
		"RESET ROLE":                                           {kind: stmtReset, name: varRole, isDefault: true},
		"SET":                                                  {kind: stmtSet, unsafe: true},
		"SET LOCAL":                                            {kind: stmtSet, local: true, unsafe: true},
		"SET extra_float_digits":                               {kind: stmtSet, name: varExtraFloatDigits, unsafe: true},
		"SET extra_float_digits =":                             {kind: stmtSet, name: varExtraFloatDigits, unsafe: true},
		"SET = 3":                                              {kind: stmtSet, unsafe: true},
		"RESET":                                                {kind: stmtReset, isDefault: true, unsafe: true},
	} {
		if got := classify(sql, false); got != want {
			t.Errorf("%q: got %+v, want %+v", sql, got, want)
		}
	}
}

func TestClassifyHonorsBackslashEscapes(t *testing.T) {
	// with standard_conforming_strings off the backslash escapes the quote, so
	// everything after it is still inside the string
	const sql = `SELECT 'a\'; SET app.tenant = 1; --'`
	if got := classify(sql, true); got.multi || got.unsafe || got.kind != stmtRead {
		t.Fatalf("expected one read, got %+v", got)
	}
	if got := classify(sql, false); !got.multi || !got.unsafe {
		t.Fatalf("expected a second, unsafe statement, got %+v", got)
	}
}

func FuzzClassify(f *testing.F) {
	for _, seed := range []string{"SELECT 1", "SET a.b = 'x', 2", "RESET", "SET TIME ZONE", "$$", "SET SESSION"} {
		f.Add(seed, false)
	}
	f.Fuzz(func(t *testing.T, sql string, backslash bool) {
		class := classify(sql, backslash)
		if (class.kind == stmtSet || class.kind == stmtReset) && !class.unsafe && class.name == "" {
			t.Fatalf("%q: a setting change with no name must be unsafe: %+v", sql, class)
		}
		if class.kind == stmtOther && !class.unsafe {
			t.Fatalf("%q: an unmodeled statement must be unsafe: %+v", sql, class)
		}
	})
}
