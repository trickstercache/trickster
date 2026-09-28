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

import (
	"encoding/binary"
	"maps"
	"slices"
	"strings"
	"sync"
)

const (
	// cacheIdentityVersion changes whenever the identity layout does, which
	// orphans every older cache entry instead of misreading it.
	cacheIdentityVersion byte = 1

	varStandardConformingStrings = "standard_conforming_strings"
	paramOptions                 = "options"
	paramApplicationName         = "application_name"
	startupSettingPrefix         = "startup."
	settingOff                   = "off"

	unsafeStatement     = "unmodeled_statement"
	unsafeSetting       = "unmodeled_setting"
	unsafeSetInTx       = "set_in_transaction"
	unsafePipelinedSet  = "pipelined_set"
	unsafeExtendedSet   = "extended_protocol_set"
	unsafeFunctionCall  = "function_call"
	unsafeOversizedText = "oversized_statement"
)

var (
	// reportedIdentity lists the settings that shape how a result is computed
	// or rendered and that an origin may announce with ParameterStatus.
	reportedIdentity = map[string]struct{}{
		varTimeZone: {}, "datestyle": {}, "intervalstyle": {}, varClientEncoding: {},
		varSearchPath: {}, varSessionAuthorization: {}, varStandardConformingStrings: {},
		"server_version": {}, "server_encoding": {}, "integer_datetimes": {},
	}
	// clientIdentity lists result-shaping settings no origin announces, which
	// can therefore only be followed by reading the client's SET statements.
	clientIdentity = map[string]struct{}{
		varRole: {}, varExtraFloatDigits: {}, varByteaOutput: {},
	}
	// neutralSettings never change a result, so a session may set them freely.
	neutralSettings = map[string]struct{}{
		paramApplicationName: {}, "statement_timeout": {}, "lock_timeout": {},
		"idle_in_transaction_session_timeout": {}, "idle_session_timeout": {},
		"work_mem": {}, "jit": {},
	}
)

type sessionTracker struct {
	mtx      sync.Mutex
	user     string
	database string
	options  string
	reported map[string]string
	client   map[string]string
	// defaults holds what the session started with for settings the origin never
	// announces; a RESET returns to them. Empty when the origin login is the client's own.
	defaults         map[string]string
	assumed          map[string]string
	settings         SessionSettings
	pending          *statementClass
	pendingReported  bool
	unsafe           string
	identity         string
	backslashEscapes bool
}

func newSessionTracker(user, database string, params map[string]string, engines ...Engine) *sessionTracker {
	t := &sessionTracker{
		user: user, database: database,
		reported: make(map[string]string), client: make(map[string]string),
		settings: SessionSettings{Tracked: clientIdentity, Neutral: neutralSettings},
	}
	if len(engines) > 0 && engines[0] != nil {
		engine := engines[0]
		if settings, ok := engine.(SessionSettingsEngine); ok {
			t.settings = settings.SessionSettings()
		}
		semantics := engine.TimeSemantics()
		t.assumed = map[string]string{
			"datestyle": semantics.AssumedDateStyle, varTimeZone: semantics.AssumedTimeZone,
			"intervalstyle":              semantics.AssumedIntervalStyle,
			varStandardConformingStrings: semantics.AssumedStandardConformingStrings,
			"integer_datetimes":          semantics.AssumedIntegerDatetimes,
		}
		maps.DeleteFunc(t.assumed, func(_, value string) bool { return value == "" })
	}
	for name, value := range params {
		name = t.settingName(name)
		switch {
		case name == paramUser || name == paramDatabase || name == paramReplication ||
			strings.HasPrefix(name, protocolOptionPrefix):
		case name == paramOptions:
			t.options = value
		default:
			if _, neutral := t.settings.Neutral[name]; neutral {
				continue
			}
			if !t.modeled(name) || t.settings.UnconfirmedStartup {
				// an unknown startup setting is constant for the session, so it
				// partitions the cache instead of disabling it
				name = startupSettingPrefix + name
			}
			t.client[name] = value
		}
	}
	t.updateLexicalOptions()
	return t
}

func (t *sessionTracker) settingName(name string) string {
	name = strings.ToLower(name)
	if canonical, ok := t.settings.Aliases[name]; ok {
		return canonical
	}
	return name
}

func (t *sessionTracker) modeled(name string) bool {
	_, reported := reportedIdentity[name]
	_, client := t.settings.Tracked[name]
	return reported || client
}

func (t *sessionTracker) parameterStatus(name, value string) {
	// records a setting the origin announced. It is authoritative:
	// it reflects SET, RESET, rollbacks and function side effects alike.
	name = t.settingName(name)
	if _, ok := reportedIdentity[name]; !ok {
		return
	}
	t.mtx.Lock()
	defer t.mtx.Unlock()
	t.reported[name] = value
	delete(t.client, name)
	t.identity = ""
	if t.pending != nil && t.pending.name == name {
		t.pendingReported = true
	}
	t.updateLexicalOptions()
}

func (t *sessionTracker) lexicalOptions() bool {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	return t.backslashEscapes
}

func (t *sessionTracker) observe(class *statementClass, txIdle, settled, extended bool) {
	// applies one client message to the session. txIdle is the last
	// reported transaction status; settled means no earlier request is still
	t.mtx.Lock()
	defer t.mtx.Unlock()
	if class.unsafe {
		t.markUnsafe(unsafeStatement)
		return
	}
	if class.kind != stmtSet && class.kind != stmtReset && class.kind != stmtDiscardAll ||
		class.local && !t.settings.LocalPersists {
		return
	}
	class.name = t.settingName(class.name)
	if class.kind != stmtDiscardAll && class.name != varAll {
		if _, neutral := t.settings.Neutral[class.name]; neutral {
			return
		}
		if !t.modeled(class.name) {
			t.markUnsafe(unsafeSetting)
			return
		}
		_, tracked := t.settings.Tracked[class.name]
		if _, announced := t.reported[class.name]; announced && !tracked {
			return
		}
	}
	// From here the change is known only from the client's words, so it must
	// be certain to take effect, to persist, and to pair with one ReadyForQuery.
	switch {
	case class.multi || !settled:
		t.markUnsafe(unsafePipelinedSet)
	case extended:
		t.markUnsafe(unsafeExtendedSet)
	case !txIdle:
		t.markUnsafe(unsafeSetInTx)
	default:
		t.pending = class
		t.pendingReported = false
	}
}

func (t *sessionTracker) ready(failed bool) {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	class := t.pending
	if class == nil {
		return
	}
	t.pending = nil
	if failed || t.pendingReported {
		return
	}
	t.identity = ""
	switch {
	case class.kind == stmtDiscardAll || class.name == varAll:
		for name := range t.client {
			if !strings.HasPrefix(name, startupSettingPrefix) {
				delete(t.client, name)
			}
		}
	case class.isDefault:
		delete(t.client, class.name)
		delete(t.reported, class.name)
	default:
		t.client[class.name] = class.value
		delete(t.reported, class.name)
	}
	t.updateLexicalOptions()
}

func (t *sessionTracker) setting(name string) (string, bool) {
	// returns a result-shaping setting's current value, preferring what
	// the origin announced over what the client asked for.
	t.mtx.Lock()
	defer t.mtx.Unlock()
	return t.settingLocked(name)
}

func (t *sessionTracker) settingLocked(name string) (string, bool) {
	if value, ok := t.reported[name]; ok {
		return value, true
	}
	if value, ok := t.client[name]; ok {
		return value, true
	}
	if value, ok := t.defaults[name]; ok {
		return value, true
	}
	value, ok := t.assumed[name]
	return value, ok
}

func (t *sessionTracker) updateLexicalOptions() {
	value, _ := t.settingLocked(varStandardConformingStrings)
	t.backslashEscapes = strings.EqualFold(value, settingOff)
}

func (t *sessionTracker) sessionDefaults(defaults map[string]string) {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	t.defaults, t.identity = defaults, ""
	// The probe observes the effective startup state, even if the origin ignored
	// a requested parameter. Do not let the request override that observation.
	for name := range defaults {
		delete(t.client, name)
	}
	t.updateLexicalOptions()
}

func (t *sessionTracker) utc() bool {
	zone, ok := t.setting(varTimeZone)
	return ok && isUTCZone(zone)
}

func (t *sessionTracker) markUnsafe(reason string) {
	if t.unsafe == "" {
		t.unsafe = reason
	}
}

func (t *sessionTracker) disable(reason string) {
	t.mtx.Lock()
	t.markUnsafe(reason)
	t.mtx.Unlock()
}

func (t *sessionTracker) cacheable() (bool, string) {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	return t.unsafe == "" && t.pending == nil, t.unsafe
}

func (t *sessionTracker) sessionIdentity() string {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	if t.identity != "" {
		return t.identity
	}
	var identity strings.Builder
	appendIdentityField(&identity, t.user)
	appendIdentityField(&identity, t.database)
	appendIdentityField(&identity, t.options)
	reported := t.reported
	if len(t.assumed) > 0 {
		reported = maps.Clone(t.reported)
		for name, value := range t.assumed {
			_, announced := reported[name]
			_, client := t.client[name]
			_, probed := t.defaults[name]
			if !announced && !client && !probed {
				reported[name] = value
			}
		}
	}
	for _, settings := range []map[string]string{reported, t.client, t.defaults} {
		names := make([]string, 0, len(settings))
		for name := range settings {
			names = append(names, name)
		}
		slices.Sort(names)
		appendIdentityUint(&identity, uint64(len(names)))
		for _, name := range names {
			appendIdentityField(&identity, name)
			appendIdentityField(&identity, settings[name])
		}
	}
	t.identity = identity.String()
	return t.identity
}

func appendIdentityField(identity *strings.Builder, value string) {
	appendIdentityUint(identity, uint64(len(value)))
	identity.WriteString(value)
}

func appendIdentityUint(identity *strings.Builder, value uint64) {
	var encoded [binary.MaxVarintLen64]byte
	identity.Write(encoded[:binary.PutUvarint(encoded[:], value)])
}
