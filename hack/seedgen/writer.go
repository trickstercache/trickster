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

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"strconv"
	"time"
)

const header = "trip_id\tvendor_id\tpickup_date\tpickup_datetime\tdropoff_date\tdropoff_datetime\t" +
	"store_and_fwd_flag\trate_code_id\tpickup_longitude\tpickup_latitude\tdropoff_longitude\t" +
	"dropoff_latitude\tpassenger_count\ttrip_distance\tfare_amount\textra\ttransit_tax\ttip_amount\t" +
	"tolls_amount\tehail_fee\timprovement_surcharge\ttotal_amount\tpayment_type\ttrip_type\t" +
	"pickup\tdropoff\tcab_type\tpickup_zone_gid\tpickup_tract_label\tpickup_borough_code\t" +
	"pickup_borough_name\tpickup_tract_code\tpickup_district_class\tpickup_neighborhood_code\t" +
	"pickup_neighborhood_name\tpickup_ward\tdropoff_zone_gid\tdropoff_tract_label\t" +
	"dropoff_borough_code\tdropoff_borough_name\tdropoff_tract_code\tdropoff_district_class\t" +
	"dropoff_neighborhood_code\tdropoff_neighborhood_name\tdropoff_ward\n"

// sink receives the two uncompressed TSV streams in order.
type sink interface {
	file(index int) (io.WriteCloser, error)
}

// summary is what the loaders need to validate and shift the data.
type summary struct {
	rows          int64
	pickupMin     int64
	pickupMax     int64
	dropoffMin    int64
	dropoffMax    int64
	sha256        string
	rowsPerFile   [2]int64
	pickupsByNTA  map[string]int64
	rowsByDay     []int64
	rowsByHour    [24]int64
	rowsByCabType map[string]int64
}

type config struct {
	rowsPerDay int
	stats      bool // collect distribution counters (slower; tests only)
}

// generate writes every row for the window to the sink and returns the
// summary; the SHA-256 covers both files' uncompressed bytes in order.
func generate(cfg config, s sink) (summary, error) {
	sum := summary{rowsByDay: make([]int64, windowDays)}
	if cfg.stats {
		sum.pickupsByNTA = map[string]int64{}
		sum.rowsByCabType = map[string]int64{}
	}
	h := sha256.New()
	var (
		out   io.WriteCloser
		row   uint64
		prev  uint64 = firstTripID - 1
		buf          = make([]byte, 0, 512)
		fileN        = -1
	)
	openFile := func(i int) error {
		var err error
		if out, err = s.file(i); err != nil {
			return err
		}
		fileN = i
		if _, err = io.MultiWriter(out, h).Write([]byte(header)); err != nil {
			return err
		}
		return nil
	}
	if err := openFile(0); err != nil {
		return sum, err
	}
	w := io.MultiWriter(out, h)
	for day := range windowDays {
		if day == splitDay {
			if err := out.Close(); err != nil {
				return sum, err
			}
			if err := openFile(1); err != nil {
				return sum, err
			}
			w = io.MultiWriter(out, h)
		}
		n := rowsForDay(day, cfg.rowsPerDay)
		for k := range n {
			t := makeTrip(row, day, k, n, prev)
			prev = t.id
			buf = appendRow(buf[:0], &t)
			if _, err := w.Write(buf); err != nil {
				return sum, err
			}
			record(&sum, &t, day, cfg.stats, fileN)
			row++
		}
	}
	if err := out.Close(); err != nil {
		return sum, err
	}
	sum.sha256 = hex.EncodeToString(h.Sum(nil))
	return sum, nil
}

func record(sum *summary, t *trip, day int, stats bool, fileN int) {
	if sum.rows == 0 {
		sum.pickupMin, sum.dropoffMin = t.pickupEpoch, t.dropoffEpoch
	}
	sum.rows++
	sum.rowsPerFile[fileN]++
	if t.pickupEpoch < sum.pickupMin {
		sum.pickupMin = t.pickupEpoch
	}
	if t.pickupEpoch > sum.pickupMax {
		sum.pickupMax = t.pickupEpoch
	}
	if t.dropoffEpoch < sum.dropoffMin {
		sum.dropoffMin = t.dropoffEpoch
	}
	if t.dropoffEpoch > sum.dropoffMax {
		sum.dropoffMax = t.dropoffEpoch
	}
	sum.rowsByDay[day]++
	if stats {
		sum.rowsByHour[t.pickupSecondOfDay/3600]++
		sum.pickupsByNTA[t.pickup.name]++
		sum.rowsByCabType[t.cabType]++
	}
}

func appendRow(b []byte, t *trip) []byte {
	b = strconv.AppendUint(b, t.id, 10)
	b = tab(b, t.vendor)
	b = appendDate(append(b, '\t'), t.pickupEpoch)
	b = appendDateTime(append(b, '\t'), t.pickupEpoch)
	b = appendDate(append(b, '\t'), t.dropoffEpoch)
	b = appendDateTime(append(b, '\t'), t.dropoffEpoch)
	b = strconv.AppendInt(append(b, '\t'), int64(t.storeAndFwd), 10)
	b = strconv.AppendInt(append(b, '\t'), int64(t.rateCode), 10)
	b = appendMicro(append(b, '\t'), t.pickupLonMicro)
	b = appendMicro(append(b, '\t'), t.pickupLatMicro)
	b = appendMicro(append(b, '\t'), t.dropoffLonMicro)
	b = appendMicro(append(b, '\t'), t.dropoffLatMicro)
	b = tab(b, t.passengers)
	b = appendHundredths(append(b, '\t'), t.distHundredths)
	b = appendHundredths(append(b, '\t'), t.fareCents)
	b = appendHundredths(append(b, '\t'), t.extraCents)
	b = appendHundredths(append(b, '\t'), t.transitTaxCents)
	b = appendHundredths(append(b, '\t'), t.tipCents)
	b = appendHundredths(append(b, '\t'), t.tollsCents)
	b = appendHundredths(append(b, '\t'), t.ehailCents)
	b = appendHundredths(append(b, '\t'), t.improvementCents)
	b = appendHundredths(append(b, '\t'), t.totalCents)
	b = tab(b, t.payment)
	b = strconv.AppendInt(append(b, '\t'), int64(t.tripType), 10)
	b = tab(b, t.pickupToken)
	b = tab(b, t.dropoffToken)
	b = tab(b, t.cabType)
	b = appendPlace(b, t.pickup)
	b = appendPlace(b, t.dropoff)
	return append(b, '\n')
}

func appendPlace(b []byte, n *neighborhood) []byte {
	br := boroughs[n.boroughIdx]
	b = strconv.AppendInt(append(b, '\t'), int64(n.gid), 10)
	b = tab(b, n.tractLabel)
	b = strconv.AppendInt(append(b, '\t'), int64(br.code), 10)
	b = tab(b, br.name)
	b = tab(b, n.tractCode)
	b = tab(b, n.class)
	b = tab(b, n.code)
	b = tab(b, n.name)
	return strconv.AppendInt(append(b, '\t'), int64(n.ward), 10)
}

func tab(b []byte, s string) []byte { return append(append(b, '\t'), s...) }

// appendHundredths formats an integer count of hundredths the way the
// source formats money and distance: "12.5", "0.3", "17.16", "8".
func appendHundredths(b []byte, v int) []byte {
	return strconv.AppendFloat(b, float64(v)/100, 'f', -1, 64)
}

func appendMicro(b []byte, v int) []byte {
	return strconv.AppendFloat(b, float64(v)/1e6, 'f', 6, 64)
}

func appendDate(b []byte, epoch int64) []byte {
	return time.Unix(epoch, 0).UTC().AppendFormat(b, "2006-01-02")
}

func appendDateTime(b []byte, epoch int64) []byte {
	return time.Unix(epoch, 0).UTC().AppendFormat(b, "2006-01-02 15:04:05")
}

// hashOnly is a sink that discards bytes; used by verify-only runs and tests.
type hashOnly struct{}

func (hashOnly) file(int) (io.WriteCloser, error) { return nopCloser{io.Discard}, nil }

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

var _ hash.Hash = sha256.New()
