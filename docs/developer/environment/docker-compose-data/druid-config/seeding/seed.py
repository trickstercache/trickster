#!/usr/bin/env python3
"""Load the shared NYC taxi seed files into Apache Druid.

The shared seed-data fetcher writes seed-window.env after deriving a shift that puts
the source pickup midpoint at the current time. Druid reads the same compressed
TSV files through its native batch API and applies that shift to __time during
ingestion. Keeping the transform in the ingestion spec means this helper does
not need to materialize another (very large) copy of the source data.
"""

# Copyright 2018 The Trickster Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone
from pathlib import Path


DRUID_URL = os.environ.get("DRUID_URL", "http://druid:8888").rstrip("/")
DATASOURCE = os.environ.get("DRUID_DATASOURCE", "trips")
DATA_DIR = Path(os.environ.get("DRUID_SEED_DATA", "/seed-data"))
METADATA = DATA_DIR / "seed-window.env"
FILES = [DATA_DIR / "trips_1.gz", DATA_DIR / "trips_2.gz"]

COLUMNS = [
    "trip_id",
    "vendor_id",
    "pickup_date",
    "pickup_datetime",
    "dropoff_date",
    "dropoff_datetime",
    "store_and_fwd_flag",
    "rate_code_id",
    "pickup_longitude",
    "pickup_latitude",
    "dropoff_longitude",
    "dropoff_latitude",
    "passenger_count",
    "trip_distance",
    "fare_amount",
    "extra",
    "mta_tax",
    "tip_amount",
    "tolls_amount",
    "ehail_fee",
    "improvement_surcharge",
    "total_amount",
    "payment_type",
    "trip_type",
    "pickup",
    "dropoff",
    "cab_type",
    "pickup_nyct2010_gid",
    "pickup_ctlabel",
    "pickup_borocode",
    "pickup_ct2010",
    "pickup_boroct2010",
    "pickup_cdeligibil",
    "pickup_ntacode",
    "pickup_ntaname",
    "pickup_puma",
    "dropoff_nyct2010_gid",
    "dropoff_ctlabel",
    "dropoff_borocode",
    "dropoff_ct2010",
    "dropoff_boroct2010",
    "dropoff_cdeligibil",
    "dropoff_ntacode",
    "dropoff_ntaname",
    "dropoff_puma",
]

DIMENSIONS = [
    "cab_type",
    "pickup_ntaname",
    "dropoff_ntaname",
    "payment_type",
    "vendor_id",
]


def fail(message: str) -> None:
    print(f"druid seed: {message}", file=sys.stderr)
    raise SystemExit(1)


def read_metadata() -> dict[str, int]:
    if not METADATA.is_file():
        fail(f"missing {METADATA}; run seed_data_fetch first")
    values: dict[str, int] = {}
    for raw_line in METADATA.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or "=" not in line:
            continue
        key, raw_value = line.split("=", 1)
        if key not in {
            "SOURCE_ROWS",
            "SOURCE_PICKUP_MIN_EPOCH",
            "SOURCE_PICKUP_MAX_EPOCH",
            "SEED_EPOCH",
            "SHIFT_SECONDS",
        }:
            continue
        try:
            values[key] = int(raw_value)
        except ValueError:
            fail(f"invalid {key} in {METADATA}")
    required = {
        "SOURCE_ROWS",
        "SOURCE_PICKUP_MIN_EPOCH",
        "SOURCE_PICKUP_MAX_EPOCH",
        "SEED_EPOCH",
        "SHIFT_SECONDS",
    }
    missing = sorted(required - values.keys())
    if missing:
        fail(f"missing metadata fields: {', '.join(missing)}")
    if values["SOURCE_ROWS"] <= 0:
        fail("SOURCE_ROWS must be positive")
    for path in FILES:
        if not path.is_file() or path.stat().st_size == 0:
            fail(f"missing or empty source file {path}")
    return values


def iso_second(epoch: int) -> str:
    return datetime.fromtimestamp(epoch, timezone.utc).strftime(
        "%Y-%m-%dT%H:%M:%S.000Z"
    )


def request_json(path: str, payload: object | None = None) -> object:
    url = f"{DRUID_URL}{path}"
    body = None
    headers = {"Accept": "application/json"}
    method = "GET"
    if payload is not None:
        body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        headers["Content-Type"] = "application/json"
        method = "POST"
    last_error: Exception | None = None
    for attempt in range(1, 31):
        try:
            request = urllib.request.Request(
                url, data=body, headers=headers, method=method
            )
            with urllib.request.urlopen(request, timeout=15) as response:  # nosec B310 - URL is configured by the compose service
                raw = response.read()
            if not raw:
                return None
            return json.loads(raw.decode("utf-8"))
        except (urllib.error.HTTPError, urllib.error.URLError, TimeoutError, json.JSONDecodeError) as error:
            last_error = error
            if isinstance(error, urllib.error.HTTPError) and error.code not in {408, 429, 500, 502, 503, 504}:
                try:
                    detail = error.read().decode("utf-8", errors="replace")
                except Exception:
                    detail = str(error)
                fail(f"Druid API {path} returned HTTP {error.code}: {detail[:400]}")
            if attempt == 30:
                break
            time.sleep(min(2.0, 0.25 * attempt))
    fail(f"Druid API {path} unavailable: {last_error}")
    return None  # unreachable, keeps type checkers happy


def mark_existing_segments_unused() -> None:
    datasources = request_json("/druid/coordinator/v1/metadata/datasources")
    if not isinstance(datasources, list) or any(
        not isinstance(value, str) for value in datasources
    ):
        fail(f"unexpected datasource metadata response: {datasources!r}")
    if DATASOURCE not in datasources:
        return

    encoded_name = urllib.parse.quote(DATASOURCE, safe="")
    segments = request_json(
        f"/druid/coordinator/v1/metadata/datasources/{encoded_name}/segments"
    )
    if not isinstance(segments, list) or any(
        not isinstance(value, str) for value in segments
    ):
        fail(f"unexpected segment metadata response: {segments!r}")
    if not segments:
        return

    response = request_json(
        f"/druid/indexer/v1/datasources/{encoded_name}/markUnused",
        {"segmentIds": segments},
    )
    if response is not None and not isinstance(response, dict):
        fail(f"unexpected markUnused response: {response!r}")
    print(f"marked {len(segments)} existing Druid segments unused", flush=True)


def ingestion_spec(metadata: dict[str, int]) -> dict[str, object]:
    shift_millis = metadata["SHIFT_SECONDS"] * 1000
    target_min = metadata["SOURCE_PICKUP_MIN_EPOCH"] + metadata["SHIFT_SECONDS"]
    target_max = metadata["SOURCE_PICKUP_MAX_EPOCH"] + metadata["SHIFT_SECONDS"]
    start = datetime.fromtimestamp(target_min, timezone.utc).replace(
        hour=0, minute=0, second=0, microsecond=0
    )
    end = datetime.fromtimestamp(target_max, timezone.utc).replace(
        hour=0, minute=0, second=0, microsecond=0
    ) + timedelta(days=1)
    interval = f"{start.strftime('%Y-%m-%dT%H:%M:%S.000Z')}/{end.strftime('%Y-%m-%dT%H:%M:%S.000Z')}"
    # __time is populated by timestampSpec before transforms are evaluated.
    # Shadowing it here keeps the same source rows useful for every seeder run,
    # while the explicit interval prevents accidental replacement elsewhere.
    if shift_millis >= 0:
        shift_expression = f"__time + {shift_millis}"
    else:
        shift_expression = f"__time - {abs(shift_millis)}"
    return {
        "type": "index_parallel",
        "spec": {
            "dataSchema": {
                "dataSource": DATASOURCE,
                "timestampSpec": {"column": "pickup_datetime", "format": "auto"},
                "transformSpec": {
                    "transforms": [
                        {"type": "expression", "name": "__time", "expression": shift_expression}
                    ]
                },
                "dimensionsSpec": {"dimensions": DIMENSIONS},
                "metricsSpec": [
                    {"type": "count", "name": "trip_count"},
                    {"type": "doubleSum", "name": "fare_amount", "fieldName": "fare_amount"},
                    {"type": "doubleSum", "name": "tip_amount", "fieldName": "tip_amount"},
                    {"type": "doubleSum", "name": "total_amount", "fieldName": "total_amount"},
                    {"type": "doubleSum", "name": "trip_distance", "fieldName": "trip_distance"},
                    {"type": "longSum", "name": "passenger_count", "fieldName": "passenger_count"},
                ],
                "granularitySpec": {
                    "type": "uniform",
                    "segmentGranularity": "DAY",
                    "queryGranularity": "NONE",
                    "rollup": False,
                    "intervals": [interval],
                },
            },
            "ioConfig": {
                "type": "index_parallel",
                "inputSource": {
                    "type": "local",
                    "files": [str(path) for path in FILES],
                },
                "inputFormat": {
                    "type": "tsv",
                    "columns": COLUMNS,
                    "skipHeaderRows": 1,
                    "tryParseNumbers": True,
                },
                "appendToExisting": False,
                "dropExisting": True,
            },
            "tuningConfig": {
                "type": "index_parallel",
                "partitionsSpec": {"type": "dynamic"},
                "maxNumConcurrentSubTasks": 1,
                "maxRetry": 2,
            },
        },
        "context": {"useLineageBasedSegmentAllocation": False},
    }


def wait_for_task(task_id: str) -> None:
    deadline = time.monotonic() + float(os.environ.get("DRUID_SEED_TIMEOUT", "3600"))
    while time.monotonic() < deadline:
        document = request_json(f"/druid/indexer/v1/task/{task_id}/status")
        status = document.get("status", {}) if isinstance(document, dict) else {}
        state = status.get("status") if isinstance(status, dict) else None
        print(f"druid seed task {task_id}: {state or 'unknown'}", flush=True)
        if state == "SUCCESS":
            return
        if state in {"FAILED", "CANCELED"}:
            report = request_json(f"/druid/indexer/v1/task/{task_id}/reports")
            fail(f"batch task {task_id} ended {state}: {json.dumps(report)[:1000]}")
        time.sleep(5)
    fail(f"batch task {task_id} did not finish before timeout")


def validate(metadata: dict[str, int]) -> None:
    query = {
        "query": f'SELECT COUNT(*) AS "rows", MIN(__time) AS "min_time", MAX(__time) AS "max_time" FROM "{DATASOURCE}"',
        "resultFormat": "object",
    }
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        try:
            response = request_json("/druid/v2/sql", query)
            if isinstance(response, list) and response and isinstance(response[0], dict):
                row = response[0]
                count = int(row.get("rows", 0))
                if count == metadata["SOURCE_ROWS"]:
                    expected_min = metadata["SOURCE_PICKUP_MIN_EPOCH"] + metadata["SHIFT_SECONDS"]
                    expected_max = metadata["SOURCE_PICKUP_MAX_EPOCH"] + metadata["SHIFT_SECONDS"]
                    min_value = row.get("min_time")
                    max_value = row.get("max_time")
                    if min_value is not None and max_value is not None:
                        # Druid commonly serializes SQL timestamps as ISO text;
                        # accepting numeric millis keeps this check version-safe.
                        min_epoch = parse_sql_epoch(min_value)
                        max_epoch = parse_sql_epoch(max_value)
                        if min_epoch != expected_min or max_epoch != expected_max:
                            fail(
                                "timestamp bounds mismatch: "
                                f"got {min_epoch}..{max_epoch}, expected {expected_min}..{expected_max}"
                            )
                    print(f"druid seed complete: {count} rows", flush=True)
                    return
        except (ValueError, TypeError):
            pass
        time.sleep(3)
    fail(f"expected {metadata['SOURCE_ROWS']} rows in {DATASOURCE}")


def parse_sql_epoch(value: object) -> int:
    if isinstance(value, (int, float)):
        return int(value) // 1000
    if isinstance(value, str):
        text = value.replace("Z", "+00:00")
        parsed = datetime.fromisoformat(text)
        if parsed.tzinfo is None:
            parsed = parsed.replace(tzinfo=timezone.utc)
        return int(parsed.timestamp())
    raise ValueError(f"unsupported timestamp value {value!r}")


def main() -> None:
    metadata = read_metadata()
    mark_existing_segments_unused()
    print(
        "submitting Druid batch seed: "
        f"rows={metadata['SOURCE_ROWS']} shift_seconds={metadata['SHIFT_SECONDS']}",
        flush=True,
    )
    response = request_json("/druid/indexer/v1/task", ingestion_spec(metadata))
    if not isinstance(response, dict) or not isinstance(response.get("task"), str):
        fail(f"Druid did not return a task id: {response!r}")
    task_id = response["task"]
    wait_for_task(task_id)
    validate(metadata)


if __name__ == "__main__":
    main()
