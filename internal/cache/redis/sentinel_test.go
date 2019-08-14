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

package redis

import (
	"fmt"
	"testing"
	"time"
)

func TestSentinelConnect(t *testing.T) {

	const expectedMaster = "master"
	const expectedPW = "12345"
	const expectedDB = 1
	const expectedMR = 2
	const expectedNRB = 8
	const expectedXRB = 512
	const expectedDT = 5000
	const expectedRT = 3000
	const expectedWT = 4000
	const expectedPS = 20
	const expectedMIC = 2
	const expectedMCA = 60000
	const expectedPT = 20000
	const expectedIT = 20000
	const expectedICF = 2000

	c, close := setupRedisCache("sentinel")
	defer close()

	c.Config.Redis.ClientType = "sentinel"
	c.Config.Redis.SentinelMaster = expectedMaster
	c.Config.Redis.Endpoints = []string{c.Config.Redis.Endpoint}
	c.Config.Redis.Password = expectedPW
	c.Config.Redis.DB = expectedDB
	c.Config.Redis.MaxRetries = expectedMR
	c.Config.Redis.MinRetryBackoffMS = expectedNRB
	c.Config.Redis.MaxRetryBackoffMS = expectedXRB
	c.Config.Redis.DialTimeoutMS = expectedDT
	c.Config.Redis.ReadTimeoutMS = expectedRT
	c.Config.Redis.WriteTimeoutMS = expectedWT
	c.Config.Redis.PoolSize = expectedPS
	c.Config.Redis.MinIdleConns = expectedMIC
	c.Config.Redis.MaxConnAgeMS = expectedMCA
	c.Config.Redis.PoolTimeoutMS = expectedPT
	c.Config.Redis.IdleTimeoutMS = expectedIT
	c.Config.Redis.IdleCheckFrequencyMS = expectedICF

	o, err := c.sentinelOpts()
	if err != nil {
		fmt.Println(err.Error())
	}

	if o.MasterName != expectedMaster {
		t.Errorf("expected %s got %s", expectedMaster, o.MasterName)
	}

	if o.Password != expectedPW {
		t.Errorf("expected %s got %s", expectedPW, o.Password)
	}

	if o.DB != expectedDB {
		t.Errorf("expected %d got %d", expectedDB, o.DB)
	}

	if o.MaxRetries != expectedMR {
		t.Errorf("expected %d got %d", expectedMR, o.MaxRetries)
	}

	if o.MinRetryBackoff != time.Duration(expectedNRB)*time.Millisecond {
		t.Errorf("expected %d got %d", expectedNRB, o.MinRetryBackoff)
	}

	if o.MaxRetryBackoff != time.Duration(expectedXRB)*time.Millisecond {
		t.Errorf("expected %d got %d", expectedXRB, o.MaxRetryBackoff)
	}

	if o.DialTimeout != time.Duration(expectedDT)*time.Millisecond {
		t.Errorf("expected %d got %d", expectedDT, o.DialTimeout)
	}

	if o.ReadTimeout != time.Duration(expectedRT)*time.Millisecond {
		t.Errorf("expected %d got %d", expectedRT, o.ReadTimeout)
	}

	if o.WriteTimeout != time.Duration(expectedWT)*time.Millisecond {
		t.Errorf("expected %d got %d", expectedWT, o.WriteTimeout)
	}

	if o.PoolSize != expectedPS {
		t.Errorf("expected %d got %d", expectedPS, o.PoolSize)
	}

	if o.MinIdleConns != expectedMIC {
		t.Errorf("expected %d got %d", expectedMIC, o.MinIdleConns)
	}

	if o.MaxConnAge != time.Duration(expectedMCA)*time.Millisecond {
		t.Errorf("expected %d got %d", expectedMCA, o.MaxConnAge)
	}

	if o.PoolTimeout != time.Duration(expectedPT)*time.Millisecond {
		t.Errorf("expected %d got %d", expectedPT, o.PoolTimeout)
	}

	if o.IdleTimeout != time.Duration(expectedIT)*time.Millisecond {
		t.Errorf("expected %d got %d", expectedIT, o.IdleTimeout)
	}

	if o.IdleCheckFrequency != time.Duration(expectedICF)*time.Millisecond {
		t.Errorf("expected %d got %d", expectedICF, o.IdleCheckFrequency)
	}

}
