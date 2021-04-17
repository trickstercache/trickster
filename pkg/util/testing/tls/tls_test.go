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

// Package tls provides functionality for use when conducting tests with TLS
package tls

import (
	"testing"
)

func TestGetTestKeyAndCert(t *testing.T) {

	_, _, err := GetTestKeyAndCert(true)
	if err != nil {
		t.Error(err)
	}

}

func TestGetTestKeyAndCertFiles(t *testing.T) {

	_, _, closer, err := GetTestKeyAndCertFiles("invalid-key")
	if closer != nil {
		defer closer()
	}
	if err != nil {
		t.Error(err)
	}

	_, _, closer2, err2 := GetTestKeyAndCertFiles("invalid-cert")
	if closer2 != nil {
		defer closer2()
	}
	if err2 != nil {
		t.Error(err2)
	}

}

func TestWriteKeyAndCert(t *testing.T) {

	err := WriteTestKeyAndCert(true, t.TempDir()+"/test.key", t.TempDir()+"/test.cert")
	if err != nil {
		t.Error(err)
	}

}

// func WriteTestKeyAndCert(isCA bool, keyPath, certPath string) error {
