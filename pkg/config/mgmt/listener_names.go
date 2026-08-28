/*
 * Copyright 2026 The Trickster Authors
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

package mgmt

const (
	// ListenerNameMetrics identifies the metrics HTTP listener.
	ListenerNameMetrics = "metrics"
	// ListenerNameMgmt identifies the management HTTP listener.
	ListenerNameMgmt = "mgmt"
	// ListenerNameBoth selects both the metrics and management HTTP listeners.
	ListenerNameBoth = "both"
	// ListenerNameOff disables registration on both HTTP listeners.
	ListenerNameOff = "off"
)
