# MoQ Transport Draft Matrix

Trickster's MoQ relay supports `draft-ietf-moq-transport` revisions -17 and later. The relay core is draft-neutral; one codec per draft family handles the wire differences. This document is the source of truth for those differences. When a new draft is published, add a column here first, then a codec, then a row in the interop matrix.

The registry and layout tables below were extracted mechanically from the draft texts (`https://www.ietf.org/archive/id/draft-ietf-moq-transport-NN.txt`) on 2026-10-04. Field semantics that live in prose are summarized with their section numbers; confirm them against the text when implementing a codec.

## Supported drafts and negotiation

| Draft | Date | ALPN | Codec family | Notes |
|---|---|---|---|---|
| -17 | 2025 | `moqt-17` | `v17` | first draft with the current topology: paired unidirectional control streams, one `SETUP`, requests on their own bidirectional streams |
| -18 | 2026 | `moqt-18` | `v18` | `moqt://` URI, `SUBSCRIBE_TRACKS`, `REDIRECT`, Required Request ID removed, `PUBLISH_OK` removed, padding, 7-byte varint |
| -19 | 2026 | `moqt-19` | `v19` | range filters, `MAX_REQUEST_UPDATES`, multiple subscriptions per track, `PUBLISH_SKIPPED` |
| -20 | 2026 | `moqt-20` | `v20` | fill fetch streams replace Joining FETCH, type-flag bitfields, `PUBLISH_STATE_NOTIFY`, `INCLUDE_PROPERTIES` |
| -21 | 2026-09-08 | `moqt-21` | `v20` | editorial only |
| -22 | 2026-10-01 | `moqt-22` | `v20` + typed `LOCATION_FILTER` | `LOCATION_FILTER` carries an explicit type; relay-to-relay declared out of scope |

Negotiation: ALPN on native QUIC (§6.2 in -22), `WT-Available-Protocols` / `WT-Protocol` on WebTransport (§6.2.1). The final RFC will use ALPN `moqt`. Drafts -15 and -16 also negotiated by ALPN but used a single bidirectional control stream with `CLIENT_SETUP`/`SERVER_SETUP`; -14 and earlier used ALPN `moq-00` with in-band version lists. None of those are supported.

## Common wire format (-17 and later)

- **Variable-length integers** (`vi64`): the number of leading 1 bits in the first byte gives the length; 1, 2, 3, 4, 5, 6, 8 or 9 bytes in -17, with the 7-byte form (leading `1111110`) and non-minimal encodings allowed from -18. A decoder for -17 that accepts 7-byte values is harmless; an encoder must not emit them to a -17 peer.
- **Control message framing**: `Type (vi64)`, `Length (16 bits)`, body. The body is called "Message Payload" in -17/-18 and "Message Body" from -19; the bytes are the same.
- **Key-value pairs** (parameters, properties, setup options): `Delta Type (vi64)`, `[Length (vi64)]`, `Value`. Even types carry a varint value with no length; odd types carry a length-prefixed value. Types are delta-encoded against the previous pair in the same list.
- **Location**: `Group (vi64)`, `Object (vi64)`.
- **Reason Phrase**: `Length (vi64)`, bytes.
- **Track Namespace**: `Number of Fields (vi64)`, then `Length (vi64)` + bytes per field; 0 to 32 fields.
- **Authorization token** (`Token`): `Alias Type (vi64)`, `[Token Alias]`, `[Token Type]`, `[Token Value]`; alias types `DELETE 0x0`, `REGISTER 0x1`, `USE_ALIAS 0x2`, `USE_VALUE 0x3`; the alias table is bounded by the peer's `MAX_AUTH_TOKEN_CACHE_SIZE`.
- **Grease**: types of the form `0x7f * N + 0x9D` are reserved for greasing in every registry from -18; -17 greases parameters only.

## Control message types

Identical in every supported draft unless a column says otherwise.

| Code | -17 | -18 | -19 | -20 | -21 | -22 |
|---|---|---|---|---|---|---|
| `0x2` | `REQUEST_UPDATE` | | | | | |
| `0x3` | `SUBSCRIBE` | | | | | |
| `0x4` | `SUBSCRIBE_OK` | | | | | |
| `0x5` | `REQUEST_ERROR` | | | | | |
| `0x6` | `PUBLISH_NAMESPACE` | | | | | |
| `0x7` | `REQUEST_OK` | | | | | |
| `0x8` | `NAMESPACE` | | | | | |
| `0xB` | `PUBLISH_DONE` | | | | | |
| `0xD` | `TRACK_STATUS` | | | | | |
| `0xE` | `NAMESPACE_DONE` | | | | | |
| `0xF` | `PUBLISH_BLOCKED` | `PUBLISH_BLOCKED` | `PUBLISH_SKIPPED` | `PUBLISH_SKIPPED` | `PUBLISH_SKIPPED` | `PUBLISH_SKIPPED` |
| `0x10` | `GOAWAY` | | | | | |
| `0x11` | `SUBSCRIBE_NAMESPACE` | — | — | — | — | — |
| `0x16` | `FETCH` | | | | | |
| `0x18` | `FETCH_OK` | | | | | |
| `0x1D` | `PUBLISH` | | | | | |
| `0x1E` | `PUBLISH_OK` | `PUBLISH_OK` | reserved | reserved | reserved | reserved |
| `0x22` | — | — | — | `PUBLISH_STATE_NOTIFY` | `PUBLISH_STATE_NOTIFY` | `PUBLISH_STATE_NOTIFY` |
| `0x50` | — | `SUBSCRIBE_NAMESPACE` | `SUBSCRIBE_NAMESPACE` | `SUBSCRIBE_NAMESPACE` | `SUBSCRIBE_NAMESPACE` | `SUBSCRIBE_NAMESPACE` |
| `0x51` | — | `SUBSCRIBE_TRACKS` | `SUBSCRIBE_TRACKS` | `SUBSCRIBE_TRACKS` | `SUBSCRIBE_TRACKS` | `SUBSCRIBE_TRACKS` |
| `0x2F00` | `SETUP` | | | | | |

`PUBLISH_BLOCKED` and `PUBLISH_SKIPPED` share a code and a layout (namespace suffix, track name); only the name changed. `TRACK_STATUS` has the same layout as `SUBSCRIBE` in every draft. The `*_OK` and `*_ERROR` names used in prose (`SUBSCRIBE_NAMESPACE_OK`, `TRACK_STATUS_ERROR`, ...) are all `REQUEST_OK` / `REQUEST_ERROR` on the wire, except `SUBSCRIBE_OK` and `FETCH_OK`, which carry extra fields.

A request stream begins with one of `SUBSCRIBE`, `PUBLISH`, `FETCH`, `TRACK_STATUS`, `PUBLISH_NAMESPACE`, `SUBSCRIBE_NAMESPACE`, or (from -18) `SUBSCRIBE_TRACKS`. Request IDs are even from the client and odd from the server, incremented by 2.

## Control message layouts that differ

- **Required Request ID** (-17 only): `SUBSCRIBE`, `REQUEST_UPDATE`, `PUBLISH`, `FETCH`, `PUBLISH_NAMESPACE` and `SUBSCRIBE_NAMESPACE` carry a `Required Request ID Delta (vi64)` after `Request ID`. Removed in -18 along with the `INVALID_REQUIRED_REQUEST_ID` session error.
- **`SUBSCRIBE_NAMESPACE`** (-17): code `0x11`, and carries `Subscribe Options (vi64)` before the parameters. From -18 it is code `0x50` without that field, and the subscribe-to-everything behavior moved to `SUBSCRIBE_TRACKS` (`0x51`).
- **`REQUEST_OK`**: -17 is parameters only; from -18 `Track Properties` follow the parameters.
- **`REQUEST_ERROR`**: `Error Code`, `Retry Interval`, `Reason Phrase`; from -18 an optional `Redirect` (`Connect URI Length`, `Connect URI`, `Track Namespace`, `Track Name Length`, `Track Name`) follows when the code is `REDIRECT`.
- **`GOAWAY`**: `New Session URI Length`, `New Session URI`, `Timeout (vi64)`; -18 appends an optional `Request ID` (per-request migration), removed again in -19.
- **`PUBLISH_OK`** (-17 only): parameters. From -18 the publisher's acknowledgement is a `REQUEST_OK`.
- **`FETCH`**: -17 through -19 carry `Fetch Type (vi64)` followed by either a `Standalone Fetch` (`Track Namespace`, `Track Name`, `Start Location`, `End Location`) or a `Joining Fetch` (`Joining Request ID`, `Joining Start`), then parameters. From -20 `FETCH` carries only the full track name and parameters; the range lives in `LOCATION_FILTER`, and the joining case is a fill (`FILL_PARAMETERS` on the subscription, delivered on a fill fetch stream).
- **`FETCH_OK`**: `End Of Track (8 bits)`, `End Location`, parameters, `Track Properties` in every draft.
- **`SUBSCRIBE_OK`**: `Track Alias`, parameters, `Track Properties` in every draft.
- **`PUBLISH`**: `Request ID`, full track name, `Track Alias`, parameters, `Track Properties` (plus the Required Request ID in -17).
- **`PUBLISH_DONE`**: `Status Code`, `Stream Count`, `Reason Phrase` in every draft; -19 raised the maximum stream count to 2^64-1.
- **`PUBLISH_STATE_NOTIFY`** (-20+): parameters only.
- **`NAMESPACE` / `NAMESPACE_DONE`**: `Track Namespace Suffix` in every draft.
- **`PUBLISH_NAMESPACE`**: -22 renames the field `Track Namespace Prefix`; the encoding is unchanged.

## Unidirectional stream types

| Type | -17 | -18+ |
|---|---|---|
| `FETCH_HEADER` | `0x05` | `0x05` |
| `SUBGROUP_HEADER` | `0x10`–`0x15`, `0x18`–`0x1D`, `0x30`–`0x35`, `0x38`–`0x3D` | the same, plus `0x50`–`0x5D` and `0x70`–`0x7D` (`FIRST_OBJECT`); -20 describes the value as a bitfield `0b0XX1XXXX` |
| `SETUP` (control stream) | `0x2F00` | `0x2F00` |
| `PADDING` | — | `0x132B3E28` (stream), `0x132B3E29` (datagram) |

An unknown stream type closes the session.

## Data plane

### `SUBGROUP_HEADER`

`Type Flags (vi64)`, `Track Alias (vi64)`, `Group ID (vi64)`, `[Subgroup ID (vi64)]`, `[Publisher Priority (8)]`. Bit meanings (identical across -17..-22, with `FIRST_OBJECT` from -18):

| Bit | Name | Meaning |
|---|---|---|
| `0x01` | `PROPERTIES` | every object in the subgroup carries a Properties field |
| `0x06` | `SUBGROUP_ID_MODE` | `00` absent and 0; `01` absent and equal to the first object ID; `10` present; `11` invalid |
| `0x08` | `END_OF_GROUP` | the subgroup contains the group's largest object |
| `0x10` | always set | a header without it is a `PROTOCOL_VIOLATION` |
| `0x20` | `DEFAULT_PRIORITY` | the Priority field is omitted and inherited from the subscription |
| `0x40` | `FIRST_OBJECT` (-18+) | the first object on the stream is the first the original publisher put in the subgroup |

Objects on a subgroup stream: `Object ID Delta (vi64)`, `[Properties]`, `Object Payload Length (vi64)`, `[Object Status (vi64)]`, `[Object Payload]`. The status is present only when the payload length is 0. Object IDs are delta-encoded within the subgroup in every supported draft (since -14).

### `OBJECT_DATAGRAM`

`Type Flags (vi64)`, `Track Alias (vi64)`, `Group ID (vi64)`, `[Object ID (vi64)]`, `[Publisher Priority (8)]`, `[Properties]`, `[Object Status (vi64)]`, `[Payload]`. -17 through -19 enumerate the legal type values (`0x00`–`0x0F`, `0x20`–`0x21`, `0x24`–`0x25`, `0x28`–`0x29`, `0x2C`–`0x2D`); -20 describes the same bits as a bitfield:

| Bit | Name | Meaning |
|---|---|---|
| `0x01` | `PROPERTIES` | Properties present |
| `0x02` | `END_OF_GROUP` | no larger object ID exists in the group |
| `0x04` | `ZERO_OBJECT_ID` | Object ID omitted and 0 |
| `0x08` | `DEFAULT_PRIORITY` | Priority omitted and inherited |
| `0x10` | invalid | must be 0 |
| `0x20` | `STATUS` | Object Status present and no payload |

`STATUS` together with `END_OF_GROUP` is invalid.

### `FETCH_HEADER` and fetch objects

`Type (vi64) = 0x5`, `Request ID (vi64)`. Each fetched object begins with these flags in every supported draft. From -18 a present Group ID or Object ID is a delta; -17 writes both whole:

| Flags | Meaning |
|---|---|
| low two bits | Subgroup ID: `0` zero, `1` same as prior, `2` prior plus one, `3` field present |
| `0x04` | Object ID Delta present (else prior plus one) |
| `0x08` | Group ID Delta present (else same as prior) |
| `0x10` | Priority present (else prior) |
| `0x20` | Properties present |
| `0x40` | datagram object: ignore the subgroup bits |

End-of-range markers (-22 §11.4.1): `0x8C` End of Non-Existent Range, `0x10C` End of Unknown Range, `0x20C` End of Timed-Out Range (-20+). -17 through -19 define only the first two, so Trickster sends them a timed-out range as an unknown one.

### Object status

`0x0` Normal (payload), `0x3` End of Group, `0x4` End of Track, in every supported draft. Non-Normal objects have no payload and, from -14, no extension headers.

## Setup options

| Type | Name | Drafts |
|---|---|---|
| `0x01` | `PATH` | all (native QUIC only) |
| `0x03` | `AUTHORIZATION_TOKEN` | all |
| `0x04` | `MAX_AUTH_TOKEN_CACHE_SIZE` | all |
| `0x05` | `AUTHORITY` | all (native QUIC only) |
| `0x06` | `MAX_FILTER_RANGES` | -19+ |
| `0x07` | `MOQT_IMPLEMENTATION` | all |
| `0x08` | `MAX_REQUEST_UPDATES` | -19+ |

-17 defines these in prose (§9.4.1) rather than an IANA table; the codes match.

## Message parameters

| Type | -17 | -18 | -19 | -20..-22 |
|---|---|---|---|---|
| `0x02` | `DELIVERY_TIMEOUT` | `OBJECT_DELIVERY_TIMEOUT` | same | same |
| `0x03` | `AUTHORIZATION_TOKEN` | | | |
| `0x04` | `RENDEZVOUS_TIMEOUT` | | | |
| `0x06` | — | `SUBGROUP_DELIVERY_TIMEOUT` | same | same |
| `0x08` | `EXPIRES` | | | |
| `0x09` | `LARGEST_OBJECT` | | | |
| `0x0A` | — | `FILL_TIMEOUT` | same | same |
| `0x10` | `FORWARD` | | | |
| `0x20` | `SUBSCRIBER_PRIORITY` | | | |
| `0x21` | `SUBSCRIPTION_FILTER` | `SUBSCRIPTION_FILTER` | `LOCATION_FILTER` | `LOCATION_FILTER` |
| `0x22` | `GROUP_ORDER` | | | |
| `0x23` | — | — | — | `FILL_PARAMETERS` |
| `0x25` | — | — | `SUBGROUP_FILTER` | same |
| `0x26` | — | — | `OBJECTID_FILTER` | same |
| `0x27` | — | — | `PRIORITY_FILTER` | same |
| `0x28` | — | — | `OBJECT_PROPERTY_FILTER` | same |
| `0x29` | — | — | `TRACK_PROPERTY_FILTER` | same |
| `0x32` | `NEW_GROUP_REQUEST` | | | |
| `0x34` | — | `TRACK_NAMESPACE_PREFIX` | same | same |
| `0x35` | — | — | — | `INCLUDE_PROPERTIES` |

### Location filter encodings

- **-17 / -18 `SUBSCRIPTION_FILTER`, -19 `LOCATION_FILTER`**: `Filter Type (vi64)`, `[Start Location]`, `[End Group Delta (vi64)]`. Types: `0x1` Next Group Start (`{Largest.Group + 1, 0}`), `0x2` Largest Object (`{Largest.Group, Largest.Object + 1}`), `0x3` AbsoluteStart (explicit start, open ended), `0x4` AbsoluteRange (explicit start and end group).
- **-20 / -21 `LOCATION_FILTER`**: `Length (vi64)` followed by zero to four varints (`StartGroup`, `StartObject`, `EndGroupDelta`, `EndObject`); which fields are present is inferred from the length.
- **-22 `LOCATION_FILTER`**: `Location Filter Type (vi64)` selects the fields: `0x00` none; `0x01` Relative Start, whose start is `{Largest.Group + 1 - StartGroup, 0}`; `0x02` Absolute Start (`StartGroup`, `StartObject`, open ended); `0x03` Absolute Start with Group End (adds `EndGroupDelta`, ending at the last object of that group); `0x04` Absolute Range (all four fields); `0x05` Next Object.
- **Range filters (-19+)**: `Type`, `Length`, `[SetID (8)]`, `[Property Type]` (property filters only), then `Range` entries of `Start (vi64)`, `[End (vi64)]`, each Start a delta from the prior End and each End a delta from its Start. -19 (§5.1.3) and -20 share the layout.
- **`FILL_PARAMETERS` (-20+)**: a nested parameter list that may contain `FILL_TIMEOUT`, `SUBSCRIBER_PRIORITY`, `LOCATION_FILTER`, `GROUP_ORDER` and the four object-level range filters.

## Properties

| Type | Name | Scope | Drafts |
|---|---|---|---|
| `0x02` | `DELIVERY_TIMEOUT` (-17) / `OBJECT_DELIVERY_TIMEOUT` | Track, Object | all |
| `0x04` | `MAX_CACHE_DURATION` | Track | all |
| `0x06` | `SUBGROUP_DELIVERY_TIMEOUT` | Track, Object | -18+ |
| `0x0B` | `IMMUTABLE_PROPERTIES` (-17: "Immutable Extensions") | Track, Object | all |
| `0x0E` | `DEFAULT_PUBLISHER_PRIORITY` | Track | all |
| `0x22` | `DEFAULT_PUBLISHER_GROUP_ORDER` | Track | all |
| `0x30` | `DYNAMIC_GROUPS` | Track | all |
| `0x3C` | `PRIOR_GROUP_ID_GAP` | Object | all |
| `0x3E` | `PRIOR_OBJECT_ID_GAP` | Object | all |

Application properties (`TIMESTAMP 0x10`, `TIMESCALE 0x08`, `VIDEO_FRAME_MARKING 0x09`, `AUDIO_LEVEL 0x0C`, `VIDEO_CONFIG 0x0D`, `AUDIO_CONFIG 0x0F`, `ENCRYPTED_LIST 0x0A`, `PADDING 0x32`) belong to LOC and Secure Objects; the relay forwards them opaquely.

## Error codes

### Session termination

| Code | Name | Drafts |
|---|---|---|
| `0x0` | `NO_ERROR` | all |
| `0x1` | `INTERNAL_ERROR` | all |
| `0x2` | `UNAUTHORIZED` | all |
| `0x3` | `PROTOCOL_VIOLATION` | all |
| `0x4` | `INVALID_REQUEST_ID` | all |
| `0x5` | `DUPLICATE_TRACK_ALIAS` | all |
| `0x6` | `KEY_VALUE_FORMATTING_ERROR` | all |
| `0x7` | `INVALID_REQUIRED_REQUEST_ID` | -17 |
| `0x8` | `INVALID_PATH` | all |
| `0x9` | `MALFORMED_PATH` | all |
| `0x10` | `GOAWAY_TIMEOUT` | all |
| `0x11` | `CONTROL_MESSAGE_TIMEOUT` | all |
| `0x12` | `DATA_STREAM_TIMEOUT` | all |
| `0x13` | `AUTH_TOKEN_CACHE_OVERFLOW` | all |
| `0x14` | `DUPLICATE_AUTH_TOKEN_ALIAS` | all |
| `0x15` | `VERSION_NEGOTIATION_FAILED` | -17..-19 |
| `0x16` | `MALFORMED_AUTH_TOKEN` | all |
| `0x17` | `UNKNOWN_AUTH_TOKEN_ALIAS` | all |
| `0x18` | `EXPIRED_AUTH_TOKEN` | all |
| `0x19` | `INVALID_AUTHORITY` | all |
| `0x1A` | `MALFORMED_AUTHORITY` | all |
| `0x1B` | `TOO_MANY_REQUEST_UPDATES` | -19+ |

### `REQUEST_ERROR`

| Code | Name | Drafts |
|---|---|---|
| `0x0` | `INTERNAL_ERROR` | all |
| `0x1` | `UNAUTHORIZED` | all |
| `0x2` | `TIMEOUT` | all |
| `0x3` | `NOT_SUPPORTED` | all |
| `0x4` | `MALFORMED_AUTH_TOKEN` | all |
| `0x5` | `EXPIRED_AUTH_TOKEN` | all |
| `0x6` | `GOING_AWAY` | all |
| `0x9` | `EXCESSIVE_LOAD` | all |
| `0x10` | `DOES_NOT_EXIST` | all |
| `0x11` | `INVALID_RANGE` | all |
| `0x12` | `MALFORMED_TRACK` | all |
| `0x19` | `DUPLICATE_SUBSCRIPTION` | -17, -18 |
| `0x20` | `UNINTERESTED` | all |
| `0x30` | `PREFIX_OVERLAP` | all |
| `0x31` | `NAMESPACE_TOO_LARGE` | all |
| `0x32` | `INVALID_JOINING_REQUEST_ID` | -17..-19 |
| `0x33` | `UNSUPPORTED_EXTENSION` | -18+ |
| `0x34` | `REDIRECT` | -18+ |
| `0x35` | `CONFLICTING_FILTERS` | -19+ |
| `0x36` | `INVALID_FILTER` | -19+ |

### `PUBLISH_DONE`

| Code | -17 | -18+ |
|---|---|---|
| `0x0` | `INTERNAL_ERROR` | same |
| `0x1` | `UNAUTHORIZED` | same |
| `0x2` | `TRACK_ENDED` | same |
| `0x3` | `SUBSCRIPTION_ENDED` | `SUBSCRIPTION_ENDED` through -19; removed in -20 |
| `0x4` | `GOING_AWAY` | same |
| `0x5` | `EXPIRED` | `TOO_FAR_BEHIND` |
| `0x6` | `TOO_FAR_BEHIND` | `EXPIRED` |
| `0x8` | `UPDATE_FAILED` | same |
| `0x9` | `EXCESSIVE_LOAD` | same |
| `0x12` | `MALFORMED_TRACK` | same |

Note the `0x5`/`0x6` swap between -17 and -18.

### Stream reset

| Code | -17 | -18+ |
|---|---|---|
| `0x0` | `INTERNAL_ERROR` | same |
| `0x1` | `CANCELLED` | same |
| `0x2` | `DELIVERY_TIMEOUT` | same |
| `0x3` | `SESSION_CLOSED` | same |
| `0x4` | `UNKNOWN_OBJECT_STATUS` | `GOING_AWAY` |
| `0x5` | `TOO_FAR_BEHIND` | same |
| `0x6` | — | `UNKNOWN_OBJECT_STATUS` |
| `0x7` | — | `EXPIRED_AUTH_TOKEN` |
| `0x9` | `EXCESSIVE_LOAD` | same |
| `0x12` | `MALFORMED_TRACK` | same |

## Capability flags

The session and relay layers never compare draft numbers; they read these flags from the negotiated codec.

| Flag | True for | Effect |
|---|---|---|
| `RequiredRequestID` | -17 | requests carry a Required Request ID Delta; a request is held until the one it names has been handed to the application or refused, for up to two seconds, then is refused with `TIMEOUT`; `INVALID_REQUIRED_REQUEST_ID` |
| `PublishOK` | -17 | `PUBLISH` is answered with `PUBLISH_OK` (`0x1E`) rather than `REQUEST_OK` |
| `AbsoluteFetchIDs` | -17 | fetch objects carry whole Group and Object IDs rather than deltas |
| `LegacyDoneCodes` | -17 | `PUBLISH_DONE` numbers `EXPIRED` `0x5` and `TOO_FAR_BEHIND` `0x6` |
| `SubscribeTracks` | -18+ | `SUBSCRIBE_TRACKS` exists and `SUBSCRIBE_NAMESPACE` is `0x50`; -17's `0x11` carries Subscribe Options, which select namespaces, `PUBLISH`es or both |
| `Redirect` | -18+ | `REQUEST_ERROR` may carry a `Redirect`; `REDIRECT` code |
| `TrackPropertiesInRequestOK` | -18+ | `REQUEST_OK` carries track properties |
| `GoAwayRequestID` | -18 | `GOAWAY` may carry a request ID |
| `FirstObjectBit` | -18+ | `SUBGROUP_HEADER` `0x40` |
| `Padding` | -18+ | padding streams and datagrams are accepted |
| `SevenByteVarint` | -18+ | the 7-byte varint form may be sent |
| `Params18` | -18+ | `SUBGROUP_DELIVERY_TIMEOUT`, `FILL_TIMEOUT` and `TRACK_NAMESPACE_PREFIX` exist; -17's `DELIVERY_TIMEOUT` is the object timeout |
| `RangeFilters` | -19+ | the five range filter parameters and `MAX_FILTER_RANGES`; applied at the relay for peers without them |
| `RequestUpdateLimit` | -19+ | `MAX_REQUEST_UPDATES`, `TOO_MANY_REQUEST_UPDATES` |
| `JoiningFetch` | -17..-19 | `FETCH` carries a Fetch Type with a Standalone range or a joining form, and `FETCH_OK` codes its End Location the same way; joining fetches are served from the fill path |
| `LegacyLocationFilter` | -17..-19 | Location Filters are Next Group Start, Largest Object, AbsoluteStart or AbsoluteRange, translated to and from the -22 forms |
| `FillFetch` | -20+ | `FILL_PARAMETERS`, fill fetch streams and `INCLUDE_PROPERTIES` |
| `TypeFlagBitfields` | -20+ | unknown set bits in `SUBGROUP_HEADER` / `OBJECT_DATAGRAM` are a `PROTOCOL_VIOLATION` rather than an unknown type |
| `PublishStateNotify` | -20+ | `PUBLISH_STATE_NOTIFY` |
| `LengthInferredLocationFilter` | -20, -21 | `LOCATION_FILTER` fields inferred from length |
| `TypedLocationFilter` | -22+ | `LOCATION_FILTER` carries an explicit type |
| `EndOfTimedOutRange` | -20+ | fetch marker `0x20C` |

Every draft accepts more than one subscription to a track, so the relay never sends -17 and -18's `DUPLICATE_SUBSCRIPTION`.

## Per-draft change highlights

Taken from the drafts' own change logs; issue numbers refer to the `moq-wg/moq-transport` repository.

- **-17 (since -16):** control stream became a pair of unidirectional streams and `CLIENT_SETUP`/`SERVER_SETUP` collapsed into `SETUP` (#1510); requests moved to bidirectional streams and cancel messages were removed (#1389); `MAX_REQUEST_ID`/`REQUESTS_BLOCKED` removed (#1471); new varint encoding (#1016); parameters as type-value pairs (#1462); grease registries (#1460); `RENDEZVOUS_TIMEOUT` (#1447); `PUBLISH_BLOCKED` (#1452); `GOAWAY` timeout (#1497); `GOING_AWAY`, `EXCESSIVE_LOAD`, `NAMESPACE_TOO_LARGE`, `TOO_FAR_BEHIND` codes; zero-element namespaces (#1472); `DELIVERY_TIMEOUT` 0 means none (#1450).
- **-18 (since -17):** unified `moqt://` URI (#1486) and fragments (#1571); `SUBSCRIBE_NAMESPACE` split into `SUBSCRIBE_NAMESPACE` and `SUBSCRIBE_TRACKS` (#1542); Required Request ID removed (#1615); `REDIRECT` (#1534); `GOAWAY` on request streams with a request ID (#1617, #1559); `PUBLISH_OK` removed (#1611); stream reset codes generalized (#1606); track properties in `REQUEST_OK` (#1576); mandatory-to-understand extensions (#1509); session-level tracks namespace (#1562); fetch object/group IDs delta encoded (#1586); `FIRST_OBJECT` (#1618); `DELIVERY_TIMEOUT` split (#1605); `FILL_TIMEOUT` (#1490); 7-byte and non-minimal varints (#1595); padding (#1475).
- **-19 (since -18):** range filters (#1765); `MAX_REQUEST_UPDATES` (#1613); multiple concurrent subscriptions per track (#1775); `GROUP_ORDER` moved from `PUBLISH_OK` to `SUBSCRIBE_TRACKS` (#1777); `PUBLISH_BLOCKED` renamed `PUBLISH_SKIPPED` (#1779); request ID removed from `GOAWAY` (#1623); FIN vs reset semantics on request streams (#1698); delivery timeouts as both track and object properties (#1476); relays may not reorder or drop objects on a multi-object stream (#1762).
- **-20 (since -19):** fill fetch streams replace Joining FETCH, `FILL_PARAMETERS` (#1673, #1868); `LOCATION_FILTER` restructured and the `FETCH` range moved into it (#1809); `PUBLISH_STATE_NOTIFY` (#1820); `INCLUDE_PROPERTIES` (#1813); `VERSION_NEGOTIATION_FAILED` removed (#1867); `SUBSCRIPTION_ENDED` removed (#1833); type-flag bitfields (#1774); End of Timed-Out Range (#1822); `OBJECT_DELIVERY_TIMEOUT` starts at the last header byte (#1844); a relay must send an upstream `FETCH` to at least one publisher (#1804).
- **-21 (since -20):** editorial restructuring only.
- **-22 (since -21):** `LOCATION_FILTER` carries an explicit Location Filter Type (#1913, #1953); "Forwarding Preference" renamed "Delivery Mode" (#1886); `FETCH_OK` End Location is inclusive (#1910); relay-to-relay communication declared out of scope (#1931).

## Interpretation and interoperability notes

Where the text leaves a choice, or a peer implementation departs from it, Trickster's codec does the following. Each note names the interop peer it was checked against and the date.

- **Interop runs (moq-dev/moq relay 0.17.0 and `moq` CLI 0.14.0, 2026-10-04).** `integration/moq_interop_test.go` publishes synthetic tracks into moq-relay and pulls them through Trickster on each draft from -17 to -22, each read by a subscriber on another draft, including an upstream `FETCH` on -17 to -19. With `TRICKSTER_MOQ_CLI_TEST=1` the `moq` CLI also subscribes to, and fetches from, Trickster on -17 to -21. All of it is native QUIC; see the WebTransport note below.
- **`moqt-22` and `LOCATION_FILTER` (moq-dev/moq, 2026-10-04).** The moq.dev Rust implementation registers draft -22 but encodes it byte for byte as -20, so its `LOCATION_FILTER` is the length-inferred form, not -22's typed form. The two encodings coincide only when every field is below 128 and the filter is neither Next Object nor absolute {0, 0}, so a strict -22 peer and moq-dev desynchronize on most subscriptions. Trickster implements -22 as written. By default a Trickster server prefers `moqt-21` over `moqt-22` (`codec.ServerPreference`), and a Trickster client does not offer `moqt-22` (`codec.ClientOffer`). -21 differs from -22 only in this field, so nothing is lost. Revisit when moq-dev ships typed filters. The CLI interop run stops at -21 for this reason: forced onto -22, the CLI's Next Object filter reads as a filter from {0, 0}.
- **`FIRST_OBJECT` (moq-dev/moq, 2026-10-04).** moq-relay drops a subgroup stream that lacks `FIRST_OBJECT` unless a fill for that group is outstanding. Trickster forwards the bit on a stream that starts with its run's first object, and sets it whenever that object is Object 0, which a -17 publisher has no bit to say.
- **`FETCH_OK` End Location through -19 (moq-dev/moq, 2026-10-04).** -17 to -19 code it like `FETCH`'s End Location: the last object plus one, or Object 0 for all of a group. moq-dev reads it as exclusive, answering a whole-group fetch with `{group + 1, 0}` and treating `{group, 0}` as an empty answer. Trickster decodes the text's form, so moq-dev's answer reads one group long; the relay never relies on an upstream `FETCH_OK` end. When serving, Trickster names the group's last object whenever it is known (in the live edge, or cached through the group's end), which both readings agree on. A group whose end it does not know is still answered with `{group, 0}`.
- **`FETCH` from -20 (moq-dev/moq, 2026-10-04).** moq-relay refuses `FETCH` from -20 with `NOT_SUPPORTED`, and the CLI sends none, so a group behind the live edge is fetched neither from nor by moq-dev on those drafts.
- **Ended groups (moq-dev/moq, 2026-10-04).** When a subscription starts just past the last object of a group that has already ended, Trickster opens an empty subgroup stream for that group with End of Group set, as moq-relay does. moq-dev's subscriber completes the group its fill (-20+) or joining fetch (-17 to -19) delivered only once that stream arrives, so a client waiting for the newest group would otherwise wait for a tail that never comes.
- **WebTransport origins (moq-dev/moq, 2026-10-04).** WebTransport over HTTP/3 requires both endpoints to send the `reset_stream_at` transport parameter. moq-relay's WebTransport endpoint does not, and webtransport-go's client refuses such a server, so Trickster reaches moq-relay with `moqt://` only. Clients reaching Trickster by WebTransport are unaffected, because webtransport-go's server checks only its own side.
- **`SUBSCRIBE_NAMESPACE` to publishers (moq-dev/moq, 2026-10-04).** moq-relay sends every session, publishers included, a `SUBSCRIBE_NAMESPACE` with an empty prefix, and carries on when it is refused.
- **`FILL_PARAMETERS` framing.** The value is "a sequence of Parameters ... encoded as if they were Parameters for a separate message". Trickster encodes it as a length prefix around a parameter count followed by the delta-typed parameters (a nested message parameter block), matching moq-dev/moq.
- **End of Range markers.** The text says the marker's "Group ID and Object ID fields are present", without saying whether they are deltas. Trickster reads and writes them as absolute values. As deltas, a marker inside the current group could not be expressed, because a present Group ID Delta always advances the group. moq-dev/moq does not implement markers (as of 2026-10-04), so this has not been interop-tested.
- **`uint8` parameters.** `FORWARD`, `SUBSCRIBER_PRIORITY`, `GROUP_ORDER` and `INCLUDE_PROPERTIES` are a single raw byte, not a varint. The Range Filter parameters carry an explicit length even though `OBJECTID_FILTER` and `OBJECT_PROPERTY_FILTER` have even types.
- **`SUBSCRIBE_TRACKS` parameters.** §9.18 lists a narrow set, while §3.6.2 allows any subscription parameter. Trickster accepts the union.
- **Repeated known Setup Options.** The text forbids senders to repeat them but gives receivers no rule. Trickster keeps the first instance and ignores the rest; `AUTHORIZATION_TOKEN` may repeat.
- **`FETCH` group order (moq-dev/moq, 2026-10-04).** A `FETCH` without `GROUP_ORDER` is served ascending, as every draft from -17 says, even when the track's `DEFAULT_PUBLISHER_GROUP_ORDER` is descending; a subscription without one follows the track's preference. moq-relay's tracks prefer descending, so the two rules differ in practice: a relay that served such a `FETCH` descending would have its receiver, reading ascending as the text says, decode every Group ID Delta after the first group in the wrong direction.
- **Required Request ID delivery (-17).** "Deliver the referenced request to the application before delivering the dependent request" is read at the hand-off: a dependent request is released once the request it names has been given to the handler or refused. Each request is handled on its own goroutine, so the order of the two handlers' first actions is the application's.

## Adding a new draft

1. Download the text, extract the registries and struct blocks, and diff them against the newest column here. Add the column and any new rows.
2. If nothing on the wire changed, register the new ALPN against the existing codec family. Otherwise add a codec file with only the changed encoders/decoders and a new capability flag for each behavioral change.
3. Add byte vectors for every changed message to the codec tests, a row to the cross-draft bridging matrix (`pkg/moq/relay/drafts_test.go`), and the draft to the interop runs (`integration/moq_interop_test.go`).
4. Update the supported-drafts table in `docs/moq.md`.
