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

package config

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/evictionmethods"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

func TestLoadConfiguration(t *testing.T) {
	a := []string{"-provider", "testing", "-origin-url", "http://prometheus:9090/test/path"}
	// it should not error if config path is not set
	conf, _, err := Load("trickster-test", "0", a)
	if err != nil {
		t.Fatal(err)
	}

	if conf.Backends["default"].TimeseriesRetention != 1024 {
		t.Errorf("expected 1024, got %d", conf.Backends["default"].TimeseriesRetention)
	}

	if conf.Backends["default"].FastForwardTTL != time.Duration(15)*time.Second {
		t.Errorf("expected 15, got %s", conf.Backends["default"].FastForwardTTL)
	}

	if conf.Caches["default"].Index.ReapInterval != time.Duration(3)*time.Second {
		t.Errorf("expected 3, got %s", conf.Caches["default"].Index.ReapInterval)
	}

}

func TestLoadConfigurationFileFailures(t *testing.T) {

	tests := []struct {
		filename string
		expected string
	}{
		{ // Case 0
			"../../../testdata/test.missing-origin-url.conf",
			`missing origin-url for backend "test"`,
		},
		{ // Case 1
			"../../../testdata/test.bad_origin_url.conf",
			"first path segment in URL cannot contain colon",
		},
		{ // Case 2
			"../../../testdata/test.missing_backend_provider.conf",
			`missing provider for backend "test"`,
		},
		{ // Case 3
			"../../../testdata/test.bad-cache-name.conf",
			`invalid cache name "test_fail" provided in backend options "test"`,
		},
		{ // Case 4
			"../../../testdata/test.invalid-negative-cache-1.conf",
			`invalid negative cache config in default: a is not a valid status code`,
		},
		{ // Case 5
			"../../../testdata/test.invalid-negative-cache-2.conf",
			`invalid negative cache config in default: 1212 is not >= 400 and < 600`,
		},
		{ // Case 6
			"../../../testdata/test.invalid-negative-cache-3.conf",
			`invalid negative cache name: foo`,
		},
		{ // Case 7
			"../../../testdata/test.invalid-pcf-name.conf",
			`invalid collapsed_forwarding name: INVALID`,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			_, _, err := Load("trickster-test", "0", []string{"-config", test.filename})
			if err == nil {
				t.Errorf("expected error `%s` got nothing", test.expected)
			} else if !strings.HasSuffix(err.Error(), test.expected) {
				t.Errorf("expected error `%s` got `%s`", test.expected, err.Error())
			}

		})
	}

}

func TestFullLoadConfiguration(t *testing.T) {

	td := t.TempDir()

	kb, cb, _ := tlstest.GetTestKeyAndCert(false)
	certfile := td + "/cert.pem"
	keyfile := td + "/key.pem"
	confFile := td + "/trickster_test_config.conf"

	err := os.WriteFile(certfile, cb, 0600)
	if err != nil {
		t.Error(err)
	}

	err = os.WriteFile(keyfile, kb, 0600)
	if err != nil {
		t.Error(err)
	}

	b, err := os.ReadFile("../../../testdata/test.full.02.conf")
	if err != nil {
		t.Error(err)
	}
	b = []byte(strings.ReplaceAll(string(b), `../../testdata/test.02.`, td+"/"))

	err = os.WriteFile(confFile, b, 0600)
	if err != nil {
		t.Error(err)
	}

	a := []string{"-config", confFile}
	// it should not error if config path is not set
	conf, _, err := Load("trickster-test", "0", a)
	if err != nil {
		t.Fatal(err)
	}

	// Test Proxy Server
	if conf.Frontend.ListenPort != 57821 {
		t.Errorf("expected 57821, got %d", conf.Frontend.ListenPort)
	}

	if conf.Frontend.ListenAddress != "test" {
		t.Errorf("expected test, got %s", conf.Frontend.ListenAddress)
	}

	if conf.Frontend.TLSListenAddress != "test-tls" {
		t.Errorf("expected test-tls, got %s", conf.Frontend.TLSListenAddress)
	}

	if conf.Frontend.TLSListenPort != 38821 {
		t.Errorf("expected 38821, got %d", conf.Frontend.TLSListenPort)
	}

	// Test Metrics Server
	if conf.Metrics.ListenPort != 57822 {
		t.Errorf("expected 57821, got %d", conf.Metrics.ListenPort)
	}

	if conf.Metrics.ListenAddress != "metrics_test" {
		t.Errorf("expected test, got %s", conf.Metrics.ListenAddress)
	}

	// Test Logging
	if conf.Logging.LogLevel != "test_log_level" {
		t.Errorf("expected test_log_level, got %s", conf.Logging.LogLevel)
	}

	if conf.Logging.LogFile != "test_file" {
		t.Errorf("expected test_file, got %s", conf.Logging.LogFile)
	}

	// Test Backends

	o, ok := conf.Backends["test"]
	if !ok {
		t.Errorf("unable to find backend options: %s", "test")
		return
	}

	if o.Provider != "test_type" {
		t.Errorf("expected test_type, got %s", o.Provider)
	}

	if o.CacheName != "test" {
		t.Errorf("expected test, got %s", o.CacheName)
	}

	if o.Scheme != "scheme" {
		t.Errorf("expected scheme, got %s", o.Scheme)
	}

	if o.Host != "test_host" {
		t.Errorf("expected test_host, got %s", o.Host)
	}

	if o.PathPrefix != "/test_path_prefix" {
		t.Errorf("expected test_path_prefix, got %s", o.PathPrefix)
	}

	if o.TimeseriesRetentionFactor != 666 {
		t.Errorf("expected 666, got %d", o.TimeseriesRetentionFactor)
	}

	if o.TimeseriesEvictionMethod != evictionmethods.EvictionMethodLRU {
		t.Errorf("expected %s, got %s", evictionmethods.EvictionMethodLRU, o.TimeseriesEvictionMethod)
	}

	if !o.FastForwardDisable {
		t.Errorf("expected fast_forward_disable true, got %t", o.FastForwardDisable)
	}

	if o.BackfillToleranceMS != 301000 {
		t.Errorf("expected 301000, got %d", o.BackfillToleranceMS)
	}

	if o.TimeoutMS != 37000 {
		t.Errorf("expected 37000, got %d", o.TimeoutMS)
	}

	if o.IsDefault != true {
		t.Errorf("expected true got %t", o.IsDefault)
	}

	if o.MaxIdleConns != 23 {
		t.Errorf("expected %d got %d", 23, o.MaxIdleConns)
	}

	if o.KeepAliveTimeoutMS != 7000 {
		t.Errorf("expected %d got %d", 7, o.KeepAliveTimeoutMS)
	}

	// MaxTTLMS is 300, thus should override TimeseriesTTLMS = 8666
	if o.TimeseriesTTLMS != 300000 {
		t.Errorf("expected 300000, got %d", o.TimeseriesTTLMS)
	}

	// MaxTTLMS is 300, thus should override FastForwardTTLMS = 382
	if o.FastForwardTTLMS != 300000 {
		t.Errorf("expected 300000, got %d", o.FastForwardTTLMS)
	}

	if o.TLS == nil {
		t.Errorf("expected tls config for backend %s, got nil", "test")
	}

	if !o.TLS.InsecureSkipVerify {
		t.Errorf("expected true got %t", o.TLS.InsecureSkipVerify)
	}

	if o.TLS.FullChainCertPath != certfile {
		t.Errorf("expected ../../testdata/test.02.cert.pem got %s", o.TLS.FullChainCertPath)
	}

	if o.TLS.PrivateKeyPath != keyfile {
		t.Errorf("expected ../../testdata/test.02.key.pem got %s", o.TLS.PrivateKeyPath)
	}

	if o.TLS.ClientCertPath != "test_client_cert" {
		t.Errorf("expected test_client_cert got %s", o.TLS.ClientCertPath)
	}

	if o.TLS.ClientKeyPath != "test_client_key" {
		t.Errorf("expected test_client_key got %s", o.TLS.ClientKeyPath)
	}

	// Test Caches

	c, ok := conf.Caches["test"]
	if !ok {
		t.Errorf("unable to find cache config: %s", "test")
		return
	}

	if c.Provider != "redis" {
		t.Errorf("expected redis, got %s", c.Provider)
	}

	if c.Index.ReapIntervalMS != 4000 {
		t.Errorf("expected 4000, got %d", c.Index.ReapIntervalMS)
	}

	if c.Index.FlushIntervalMS != 6000 {
		t.Errorf("expected 6000, got %d", c.Index.FlushIntervalMS)
	}

	if c.Index.MaxSizeBytes != 536870913 {
		t.Errorf("expected 536870913, got %d", c.Index.MaxSizeBytes)
	}

	if c.Index.MaxSizeBackoffBytes != 16777217 {
		t.Errorf("expected 16777217, got %d", c.Index.MaxSizeBackoffBytes)
	}

	if c.Index.MaxSizeObjects != 80 {
		t.Errorf("expected 80, got %d", c.Index.MaxSizeObjects)
	}

	if c.Index.MaxSizeBackoffObjects != 20 {
		t.Errorf("expected 20, got %d", c.Index.MaxSizeBackoffObjects)
	}

	if c.Index.ReapIntervalMS != 4000 {
		t.Errorf("expected 4000, got %d", c.Index.ReapIntervalMS)
	}

	if c.Redis.ClientType != "test_redis_type" {
		t.Errorf("expected test_redis_type, got %s", c.Redis.ClientType)
	}

	if c.Redis.Protocol != "test_protocol" {
		t.Errorf("expected test_protocol, got %s", c.Redis.Protocol)
	}

	if c.Redis.Endpoint != "test_endpoint" {
		t.Errorf("expected test_endpoint, got %s", c.Redis.Endpoint)
	}

	if c.Redis.SentinelMaster != "test_master" {
		t.Errorf("expected test_master, got %s", c.Redis.SentinelMaster)
	}

	if c.Redis.Password != "test_password" {
		t.Errorf("expected test_password, got %s", c.Redis.Password)
	}

	if c.Redis.DB != 42 {
		t.Errorf("expected 42, got %d", c.Redis.DB)
	}

	if c.Redis.MaxRetries != 6 {
		t.Errorf("expected 6, got %d", c.Redis.MaxRetries)
	}

	if c.Redis.MinRetryBackoffMS != 9 {
		t.Errorf("expected 9, got %d", c.Redis.MinRetryBackoffMS)
	}

	if c.Redis.MaxRetryBackoffMS != 513 {
		t.Errorf("expected 513, got %d", c.Redis.MaxRetryBackoffMS)
	}

	if c.Redis.DialTimeoutMS != 5001 {
		t.Errorf("expected 5001, got %d", c.Redis.DialTimeoutMS)
	}

	if c.Redis.ReadTimeoutMS != 3001 {
		t.Errorf("expected 3001, got %d", c.Redis.ReadTimeoutMS)
	}

	if c.Redis.WriteTimeoutMS != 3002 {
		t.Errorf("expected 3002, got %d", c.Redis.WriteTimeoutMS)
	}

	if c.Redis.PoolSize != 21 {
		t.Errorf("expected 21, got %d", c.Redis.PoolSize)
	}

	if c.Redis.MinIdleConns != 5 {
		t.Errorf("expected 5, got %d", c.Redis.PoolSize)
	}

	if c.Redis.MaxConnAgeMS != 2000 {
		t.Errorf("expected 2000, got %d", c.Redis.MaxConnAgeMS)
	}

	if c.Redis.PoolTimeoutMS != 4001 {
		t.Errorf("expected 4001, got %d", c.Redis.PoolTimeoutMS)
	}

	if c.Redis.IdleTimeoutMS != 300001 {
		t.Errorf("expected 300001, got %d", c.Redis.IdleTimeoutMS)
	}

	if c.Redis.IdleCheckFrequencyMS != 60001 {
		t.Errorf("expected 60001, got %d", c.Redis.IdleCheckFrequencyMS)
	}

	if c.Filesystem.CachePath != "test_cache_path" {
		t.Errorf("expected test_cache_path, got %s", c.Filesystem.CachePath)
	}

	if c.BBolt.Filename != "test_filename" {
		t.Errorf("expected test_filename, got %s", c.BBolt.Filename)
	}

	if c.BBolt.Bucket != "test_bucket" {
		t.Errorf("expected test_bucket, got %s", c.BBolt.Bucket)
	}

	if c.Badger.Directory != "test_directory" {
		t.Errorf("expected test_directory, got %s", c.Badger.Directory)
	}

	if c.Badger.ValueDirectory != "test_value_directory" {
		t.Errorf("expected test_value_directory, got %s", c.Badger.ValueDirectory)
	}
}

func TestEmptyLoadConfiguration(t *testing.T) {
	a := []string{"-config", "../../../testdata/test.empty.conf"}
	// it should not error if config path is not set
	conf, _, err := Load("trickster-test", "0", a)
	if err != nil {
		t.Fatal(err)
	}

	if len(conf.Backends) != 1 {
		// we define a "test" cache, but never reference it by a backend,
		// so it should not make it into the running config
		t.Errorf("expected %d, got %d", 1, len(conf.Backends))
	}

	// Test Backends

	o, ok := conf.Backends["test"]
	if !ok {
		t.Errorf("unable to find backend options: %s", "test")
		return
	}

	if o.Provider != "test" {
		t.Errorf("expected %s backend provider, got %s", "test", o.Provider)
	}

	if o.Scheme != "http" {
		t.Errorf("expected %s, got %s", "http", o.Scheme)
	}

	if o.Host != "1" {
		t.Errorf("expected %s, got %s", "1", o.Host)
	}

	if o.PathPrefix != "" {
		t.Errorf("expected '%s', got '%s'", "", o.PathPrefix)
	}

	if o.FastForwardDisable {
		t.Errorf("expected fast_forward_disable false, got %t", o.FastForwardDisable)
	}

	c, ok := conf.Caches["default"]
	if !ok {
		t.Errorf("unable to find cache config: %s", "default")
		return
	}

	if c.Index.ReapIntervalMS != 3000 {
		t.Errorf("expected 3000, got %d", c.Index.ReapIntervalMS)
	}

	if c.Redis.Endpoint != "redis:6379" {
		t.Errorf("expected redis:6379, got %s", c.Redis.Endpoint)
	}

	if c.Redis.SentinelMaster != "" {
		t.Errorf("expected '', got %s", c.Redis.SentinelMaster)
	}

	if c.Redis.Password != "" {
		t.Errorf("expected '', got %s", c.Redis.Password)
	}

	if c.Redis.DB != 0 {
		t.Errorf("expected 0, got %d", c.Redis.DB)
	}

	if c.Redis.MaxRetries != 0 {
		t.Errorf("expected 0, got %d", c.Redis.MaxRetries)
	}

	if c.Redis.MinRetryBackoffMS != 0 {
		t.Errorf("expected 0, got %d", c.Redis.MinRetryBackoffMS)
	}

	if c.Redis.MaxRetryBackoffMS != 0 {
		t.Errorf("expected 0, got %d", c.Redis.MaxRetryBackoffMS)
	}

	if c.Redis.DialTimeoutMS != 0 {
		t.Errorf("expected 0, got %d", c.Redis.DialTimeoutMS)
	}

	if c.Redis.ReadTimeoutMS != 0 {
		t.Errorf("expected 0, got %d", c.Redis.ReadTimeoutMS)
	}

	if c.Redis.WriteTimeoutMS != 0 {
		t.Errorf("expected 0, got %d", c.Redis.WriteTimeoutMS)
	}

	if c.Redis.PoolSize != 0 {
		t.Errorf("expected 0, got %d", c.Redis.PoolSize)
	}

	if c.Redis.MinIdleConns != 0 {
		t.Errorf("expected 0, got %d", c.Redis.PoolSize)
	}

	if c.Redis.MaxConnAgeMS != 0 {
		t.Errorf("expected 0, got %d", c.Redis.MaxConnAgeMS)
	}

	if c.Redis.PoolTimeoutMS != 0 {
		t.Errorf("expected 0, got %d", c.Redis.PoolTimeoutMS)
	}

	if c.Redis.IdleTimeoutMS != 0 {
		t.Errorf("expected 0, got %d", c.Redis.IdleTimeoutMS)
	}

	if c.Redis.IdleCheckFrequencyMS != 0 {
		t.Errorf("expected 0, got %d", c.Redis.IdleCheckFrequencyMS)
	}

	if c.Filesystem.CachePath != "/tmp/trickster" {
		t.Errorf("expected /tmp/trickster, got %s", c.Filesystem.CachePath)
	}

	if c.BBolt.Filename != "trickster.db" {
		t.Errorf("expected trickster.db, got %s", c.BBolt.Filename)
	}

	if c.BBolt.Bucket != "trickster" {
		t.Errorf("expected trickster, got %s", c.BBolt.Bucket)
	}

	if c.Badger.Directory != "/tmp/trickster" {
		t.Errorf("expected /tmp/trickster, got %s", c.Badger.Directory)
	}

	if c.Badger.ValueDirectory != "/tmp/trickster" {
		t.Errorf("expected /tmp/trickster, got %s", c.Badger.ValueDirectory)
	}
}

func TestLoadConfigurationVersion(t *testing.T) {
	a := []string{"-version"}
	// it should not error if config path is not set
	_, flags, err := Load("trickster-test", "0", a)
	if err != nil {
		t.Fatal(err)
	}

	if !flags.PrintVersion {
		t.Errorf("expected true got false")
	}
}

func TestLoadConfigurationBadPath(t *testing.T) {
	const badPath = "/afeas/aasdvasvasdf48/ag4a4gas"

	a := []string{"-config", badPath}
	// it should not error if config path is not set
	_, _, err := Load("trickster-test", "0", a)
	if err == nil {
		t.Errorf("expected error: open %s: no such file or directory", badPath)
	}
}

func TestLoadConfigurationBadUrl(t *testing.T) {
	const badURL = ":httap:]/]/example.com9091"
	a := []string{"-origin-url", badURL}
	_, _, err := Load("trickster-test", "0", a)
	if err == nil {
		t.Errorf("expected error: parse %s: missing protocol scheme", badURL)
	}
}

func TestLoadConfigurationBadArg(t *testing.T) {
	const url = "http://0.0.0.0"
	a := []string{"-origin-url", url, "-provider", "rpc", "-unknown-flag"}
	_, _, err := Load("trickster-test", "0", a)
	if err == nil {
		t.Error("expected error: flag provided but not defined: -unknown-flag")
	}
}

func TestLoadConfigurationWarning1(t *testing.T) {

	a := []string{"-config", "../../../testdata/test.warning1.conf"}
	// it should not error if config path is not set
	conf, _, err := Load("trickster-test", "0", a)
	if err != nil {
		t.Fatal(err)
	}

	expected := 1
	l := len(conf.LoaderWarnings)

	if l != expected {
		t.Errorf("expected %d got %d", expected, l)
	}

}

func TestLoadConfigurationWarning2(t *testing.T) {

	a := []string{"-config", "../../../testdata/test.warning2.conf"}
	// it should not error if config path is not set
	conf, _, err := Load("trickster-test", "0", a)
	if err != nil {
		t.Fatal(err)
	}

	expected := 1
	l := len(conf.LoaderWarnings)

	if l != expected {
		t.Errorf("expected %d got %d", expected, l)
	}

}

func TestLoadEmptyArgs(t *testing.T) {
	a := []string{}
	_, _, err := Load("trickster-test", "0", a)
	if err == nil {
		t.Error("expected error: no valid backends configured")
	}
}
