/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package config

import (
	"testing"
	"time"
)

func TestLoadConfiguration(t *testing.T) {
	a := []string{}
	// it should not error if config path is not set
	err := Load("trickster-test", "0", a)
	if err != nil {
		t.Error(err)
	}

	if Origins["default"].TimeseriesRetention != 1024 {
		t.Errorf("expected 1024, got %d", Origins["default"].TimeseriesRetention)
	}

	if Caches["default"].FastForwardTTL != time.Duration(15)*time.Second {
		t.Errorf("expected 15, got %s", Caches["default"].FastForwardTTL)
	}

	if Caches["default"].Index.ReapInterval != time.Duration(3)*time.Second {
		t.Errorf("expected 3, got %s", Caches["default"].Index.ReapInterval)
	}

}

func TestFullLoadConfiguration(t *testing.T) {
	a := []string{"-config", "../../testdata/test.full.conf"}
	// it should not error if config path is not set
	err := Load("trickster-test", "0", a)
	if err != nil {
		t.Error(err)
	}

	// Test Proxy Server
	if ProxyServer.ListenPort != 57821 {
		t.Errorf("expected 57821, got %d", ProxyServer.ListenPort)
	}

	if ProxyServer.ListenAddress != "test" {
		t.Errorf("expected test, got %s", ProxyServer.ListenAddress)
	}

	// Test Metrics Server
	if Metrics.ListenPort != 57822 {
		t.Errorf("expected 57821, got %d", Metrics.ListenPort)
	}

	if Metrics.ListenAddress != "metrics_test" {
		t.Errorf("expected test, got %s", Metrics.ListenAddress)
	}

	// Test Logging
	if Logging.LogLevel != "test_log_level" {
		t.Errorf("expected test_log_level, got %s", Logging.LogLevel)
	}

	if Logging.LogFile != "test_file" {
		t.Errorf("expected test_file, got %s", Logging.LogFile)
	}

	// Test Origins

	o, ok := Origins["test"]
	if !ok {
		t.Errorf("unable to find origin config: %s", "test")
		return
	}

	if o.Type != "test_type" {
		t.Errorf("expected test_type, got %s", o.Type)
	}

	if o.CacheName != "test" {
		t.Errorf("expected test, got %s", o.CacheName)
	}

	if o.Scheme != "test_scheme" {
		t.Errorf("expected test_scheme, got %s", o.Scheme)
	}

	if o.Host != "test_host" {
		t.Errorf("expected test_host, got %s", o.Host)
	}

	if o.PathPrefix != "test_path_prefix" {
		t.Errorf("expected test_path_prefix, got %s", o.PathPrefix)
	}

	if o.APIPath != "test_api_path" {
		t.Errorf("expected test_api_path, got %s", o.APIPath)
	}

	if !o.IgnoreNoCacheHeader {
		t.Errorf("expected ignore_no_cache_header true, got %t", o.IgnoreNoCacheHeader)
	}

	if o.TimeseriesRetentionFactor != 666 {
		t.Errorf("expected 666, got %d", o.TimeseriesRetentionFactor)
	}

	if !o.FastForwardDisable {
		t.Errorf("expected fast_forward_disable true, got %t", o.FastForwardDisable)
	}

	if o.BackfillToleranceSecs != 301 {
		t.Errorf("expected 301, got %d", o.BackfillToleranceSecs)
	}

	if o.TimeoutSecs != 37 {
		t.Errorf("expected 37, got %d", o.TimeoutSecs)
	}

	// Test Caches

	c, ok := Caches["test"]
	if !ok {
		t.Errorf("unable to find cache config: %s", "test")
		return
	}

	if c.Type != "test_type" {
		t.Errorf("expected test_type, got %s", c.Type)
	}

	if !c.Compression {
		t.Errorf("expected compression %t, got %t", defaultCacheCompression, c.Compression)
	}

	if c.TimeseriesTTLSecs != 8666 {
		t.Errorf("expected 8666, got %d", c.TimeseriesTTLSecs)
	}

	if c.FastForwardTTLSecs != 17 {
		t.Errorf("expected 17, got %d", c.FastForwardTTLSecs)
	}

	if c.ObjectTTLSecs != 39 {
		t.Errorf("expected 39, got %d", c.ObjectTTLSecs)
	}

	if c.Index.ReapIntervalSecs != 4 {
		t.Errorf("expected 4, got %d", c.Index.ReapIntervalSecs)
	}

	if c.Index.FlushIntervalSecs != 6 {
		t.Errorf("expected 6, got %d", c.Index.FlushIntervalSecs)
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

	if c.Index.ReapIntervalSecs != 4 {
		t.Errorf("expected 4, got %d", c.Index.ReapIntervalSecs)
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
	a := []string{"-config", "../../testdata/test.empty.conf"}
	// it should not error if config path is not set
	err := Load("trickster-test", "0", a)
	if err != nil {
		t.Error(err)
	}

	if len(Origins) != 1 {
		// we define a "test" cache, but never reference it by an origin,
		// so it should not make it into the running config
		t.Errorf("expected %d, got %d", 1, len(Origins))
	}

	// Test Proxy Server
	if ProxyServer.ListenPort != defaultProxyListenPort {
		t.Errorf("expected %d, got %d", defaultProxyListenPort, ProxyServer.ListenPort)
	}

	if ProxyServer.ListenAddress != defaultProxyListenAddress {
		t.Errorf("expected '%s', got '%s'", defaultProxyListenAddress, ProxyServer.ListenAddress)
	}

	// Test Metrics Server
	if Metrics.ListenPort != defaultMetricsListenPort {
		t.Errorf("expected %d, got %d", defaultMetricsListenPort, Metrics.ListenPort)
	}

	if Metrics.ListenAddress != defaultMetricsListenAddress {
		t.Errorf("expected '%s', got '%s'", defaultMetricsListenAddress, Metrics.ListenAddress)
	}

	// Test Logging
	if Logging.LogLevel != defaultLogLevel {
		t.Errorf("expected %s, got %s", defaultLogLevel, Logging.LogLevel)
	}

	if Logging.LogFile != defaultLogFile {
		t.Errorf("expected '%s', got '%s'", defaultLogFile, Logging.LogFile)
	}

	// Test Origins

	o, ok := Origins["test"]
	if !ok {
		t.Errorf("unable to find origin config: %s", "test")
		return
	}

	if o.Type != defaultOriginServerType {
		t.Errorf("expected %s, got %s", defaultOriginServerType, o.Type)
	}

	if o.CacheName != defaultOriginCacheName {
		t.Errorf("expected %s, got %s", defaultOriginCacheName, o.CacheName)
	}

	if o.Scheme != defaultOriginScheme {
		t.Errorf("expected %s, got %s", defaultOriginScheme, o.Scheme)
	}

	if o.Host != defaultOriginHost {
		t.Errorf("expected %s, got %s", defaultOriginHost, o.Host)
	}

	if o.PathPrefix != defaultOriginPathPrefix {
		t.Errorf("expected '%s', got '%s'", defaultOriginPathPrefix, o.PathPrefix)
	}

	if o.APIPath != defaultOriginAPIPath {
		t.Errorf("expected %s, got %s", defaultOriginAPIPath, o.APIPath)
	}

	if o.IgnoreNoCacheHeader != defaultOriginINCH {
		t.Errorf("expected ignore_no_cache_header %t, got %t", defaultOriginINCH, o.IgnoreNoCacheHeader)
	}

	if o.TimeseriesRetentionFactor != defaultOriginTRF {
		t.Errorf("expected %d, got %d", defaultOriginTRF, o.TimeseriesRetentionFactor)
	}

	if o.FastForwardDisable {
		t.Errorf("expected fast_forward_disable false, got %t", o.FastForwardDisable)
	}

	if o.BackfillToleranceSecs != defaultBackfillToleranceSecs {
		t.Errorf("expected %d, got %d", defaultBackfillToleranceSecs, o.BackfillToleranceSecs)
	}

	if o.TimeoutSecs != defaultOriginTimeoutSecs {
		t.Errorf("expected %d, got %d", defaultOriginTimeoutSecs, o.TimeoutSecs)
	}

	c, ok := Caches["default"]
	if !ok {
		t.Errorf("unable to find cache config: %s", "default")
		return
	}

	if c.Type != defaultCacheType {
		t.Errorf("expected %s, got %s", defaultCacheType, c.Type)
	}

	if !c.Compression {
		t.Errorf("expected compression %t, got %t", defaultCacheCompression, c.Compression)
	}

	if c.TimeseriesTTLSecs != defaultTimeseriesTTLSecs {
		t.Errorf("expected %d, got %d", defaultTimeseriesTTLSecs, c.TimeseriesTTLSecs)
	}

	if c.FastForwardTTLSecs != defaultFastForwardTTLSecs {
		t.Errorf("expected %d, got %d", defaultFastForwardTTLSecs, c.FastForwardTTLSecs)
	}

	if c.ObjectTTLSecs != defaultObjectTTLSecs {
		t.Errorf("expected %d, got %d", defaultObjectTTLSecs, c.ObjectTTLSecs)
	}

	if c.Index.ReapIntervalSecs != defaultCacheIndexReap {
		t.Errorf("expected %d, got %d", defaultCacheIndexReap, c.Index.ReapIntervalSecs)
	}

	if c.Index.FlushIntervalSecs != defaultCacheIndexFlush {
		t.Errorf("expected %d, got %d", defaultCacheIndexFlush, c.Index.FlushIntervalSecs)
	}

	if c.Index.MaxSizeBytes != defaultCacheMaxSizeBytes {
		t.Errorf("expected %d, got %d", defaultCacheMaxSizeBytes, c.Index.MaxSizeBytes)
	}

	if c.Index.MaxSizeBackoffBytes != defaultMaxSizeBackoffBytes {
		t.Errorf("expected %d, got %d", defaultMaxSizeBackoffBytes, c.Index.MaxSizeBackoffBytes)
	}

	if c.Index.MaxSizeObjects != defaultMaxSizeObjects {
		t.Errorf("expected %d, got %d", defaultMaxSizeObjects, c.Index.MaxSizeObjects)
	}

	if c.Index.MaxSizeBackoffObjects != defaultMaxSizeBackoffObjects {
		t.Errorf("expected %d, got %d", defaultMaxSizeBackoffObjects, c.Index.MaxSizeBackoffObjects)
	}

	if c.Index.ReapIntervalSecs != 3 {
		t.Errorf("expected 3, got %d", c.Index.ReapIntervalSecs)
	}

	if c.Redis.ClientType != defaultRedisClientType {
		t.Errorf("expected %s, got %s", defaultRedisClientType, c.Redis.ClientType)
	}

	if c.Redis.Protocol != defaultRedisProtocol {
		t.Errorf("expected %s, got %s", defaultRedisProtocol, c.Redis.Protocol)
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
	err := Load("trickster-test", "0", a)
	if err != nil {
		t.Error(err)
	}

	if !Flags.PrintVersion {
		t.Errorf("expected true got false")
	}
}

func TestLoadConfigurationBadPath(t *testing.T) {

	const badPath = "/afeas/aasdvasvasdf48/ag4a4gas"

	a := []string{"-config", badPath}
	// it should not error if config path is not set
	err := Load("trickster-test", "0", a)
	if err == nil {
		t.Errorf("Expected error: open %s: no such file or directory", badPath)
	}
}

func TestLoadConfigurationBadUrl(t *testing.T) {
	const badURL = ":httap:]/]/example.com9091"
	a := []string{"-origin", badURL}
	err := Load("trickster-test", "0", a)
	if err == nil {
		t.Errorf("Expected error: parse %s: missing protocol scheme", badURL)
	}
}
