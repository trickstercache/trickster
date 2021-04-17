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

package timeseries

import "testing"

func TestFieldDefinitionClone(t *testing.T) {

	fd := FieldDefinition{
		Name:     "test",
		DataType: FieldDataType(1),
	}

	fd2 := fd.Clone()

	if fd2 != fd {
		t.Error("clone mismatch")
	}

}

func TestFieldDefinitionString(t *testing.T) {

	fd := FieldDefinitions{
		FieldDefinition{
			Name:     "test",
			DataType: FieldDataType(1),
		},
	}

	const expected = `[{"name":"test","type":1,"pos":0,"stype":"","provider1":0}]`

	if fd.String() != expected {
		t.Errorf("expected `%s` got `%s`", expected, fd.String())
	}

}
