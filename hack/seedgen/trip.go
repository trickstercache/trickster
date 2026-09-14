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

import "math"

// trip holds one output row. Money is in cents and distance in hundredths
// of a mile so that every value is exact and formats identically everywhere.
type trip struct {
	id                uint64
	vendor            string
	pickupEpoch       int64
	dropoffEpoch      int64
	storeAndFwd       int
	rateCode          int
	pickupLonMicro    int
	pickupLatMicro    int
	dropoffLonMicro   int
	dropoffLatMicro   int
	passengers        string
	distHundredths    int
	fareCents         int
	extraCents        int
	transitTaxCents   int
	tipCents          int
	tollsCents        int
	ehailCents        int
	improvementCents  int
	totalCents        int
	payment           string
	tripType          int
	pickupToken       string
	dropoffToken      string
	cabType           string
	pickup            *neighborhood
	dropoff           *neighborhood
	pickupSecondOfDay int
}

const (
	firstTripID   = 1200000000
	flatFareCents = 5200
	transitTax    = 50
	improvement   = 30
	tollMain      = 554
	tollLong      = 1175
)

func makeTrip(row uint64, day, k, n int, prevID uint64) trip {
	var t trip
	t.id = prevID + 1 + mix(row, saltIDGap)%20
	t.pickupSecondOfDay = secondOfDay(day, k, n, row)
	hour := t.pickupSecondOfDay / 3600
	t.pickupEpoch = windowStart + int64(day)*secondsPerDay + int64(t.pickupSecondOfDay)

	t.pickup = &neighborhoods[pickupTable.pick(unit(row, saltPickup))]
	t.dropoff = pickDropoff(row, t.pickup)

	if t.pickup.airport {
		t.cabType = cabTypes[cabAirportTable.pick(unit(row, saltCabType))].value
	} else {
		t.cabType = cabTypes[cabTable.pick(unit(row, saltCabType))].value
	}

	t.distHundredths = distanceHundredths(row, t.pickup, t.dropoff)
	t.rateCode = rateCode(row, t.pickup)
	t.dropoffEpoch = t.pickupEpoch + int64(durationSeconds(row, t.distHundredths, hour))
	t.passengers = passengers[passengerTable.pick(unit(row, saltPassenger))].value
	t.vendor = vendors[vendorTable.pick(unit(row, saltVendor))].value
	t.payment = payments[paymentTable.pick(unit(row, saltPayment))].value

	t.fareCents = fareCents(t.distHundredths, t.rateCode)
	t.extraCents = extraCents(hour, day%7)
	t.transitTaxCents = transitTax
	if unit(row, saltSurcharge) < 0.005 {
		t.transitTaxCents = 0
	}
	t.improvementCents = improvement
	if unit(row, saltSurcharge^0x55) < 0.001 {
		t.improvementCents = 0
	}
	t.tollsCents = tollsCents(row, t.pickup, t.dropoff)
	t.tipCents = tipCents(row, t.fareCents, t.payment)
	t.totalCents = t.fareCents + t.extraCents + t.transitTaxCents + t.tipCents +
		t.tollsCents + t.ehailCents + t.improvementCents

	t.pickupLonMicro, t.pickupLatMicro = coords(row, saltCoords, t.pickup)
	t.dropoffLonMicro, t.dropoffLatMicro = coords(row, saltCoords^0x99, t.dropoff)
	t.pickupToken = zoneToken(row, saltZoneToken)
	t.dropoffToken = zoneToken(row, saltZoneToken^0x33)
	if unit(row, saltStoreFwd) < 0.008 {
		t.storeAndFwd = 1
	}
	return t
}

// pickDropoff keeps about 15 % of trips inside the pickup neighborhood and sends
// airport pickups mostly to the three busiest neighborhoods.
func pickDropoff(row uint64, pickup *neighborhood) *neighborhood {
	u := unit(row, saltDropoff)
	if pickup.airport {
		switch {
		case u < 0.04:
			return pickup
		case u < 0.64:
			return &neighborhoods[top3[int(u*100)%3]]
		}
		return &neighborhoods[pickupTable.pick(unit(row, saltDropoff^0x11))]
	}
	if u < 0.10 { // plus the weighted picks that land at home, about 15 % in total
		return pickup
	}
	return &neighborhoods[pickupTable.pick(unit(row, saltDropoff^0x11))]
}

// distanceHundredths is log-normal (median 1.7 mi, mean about 3 mi, capped
// at 40 mi) except for airport trips, which run 12 to 18 mi.
func distanceHundredths(row uint64, pickup, dropoff *neighborhood) int {
	if pickup.airport || dropoff.airport {
		return 1200 + int(600*unit(row, saltDistance))
	}
	v := math.Exp(0.53 + 0.85*gauss(row, saltDistance))
	if v > 40 {
		v = 40
	}
	if pickup == dropoff && v > 3 {
		v = 3
	}
	return int(v*100 + 0.5)
}

func rateCode(row uint64, pickup *neighborhood) int {
	u := unit(row, saltRateCode)
	if pickup.airport {
		if u < 0.5 {
			return 2
		}
		return 1
	}
	switch {
	case u < 0.0030:
		return 5
	case u < 0.0047:
		return 3
	case u < 0.0051:
		return 4
	case u < 0.0052:
		return 99
	case u < 0.00521:
		return 6
	}
	return 1
}

// fareCents is 4.40 + 2.80 per mile rounded to the nearest half dollar, or a
// flat fare for rate code 2.
func fareCents(distHundredths, rateCode int) int {
	if rateCode == 2 {
		return flatFareCents
	}
	c := 440 + 280*distHundredths/100
	return (c + 25) / 50 * 50
}

// durationSeconds uses minutes per mile that widen at the evening peak and
// tighten overnight, with a spread of roughly 3 to 11 minutes per mile.
func durationSeconds(row uint64, distHundredths, hour int) int {
	mpm := 5.6
	switch {
	case hour >= 16 && hour < 20:
		mpm = 8.0
	case hour < 6:
		mpm = 3.8
	}
	mpm *= 1 + 0.6*(unit(row, saltDuration)-0.5)
	s := max(int(float64(distHundredths)/100*mpm*60+0.5), 60)
	return s
}

func extraCents(hour, dow int) int {
	switch {
	case hour >= 20 || hour < 6:
		return 50
	case hour >= 16 && dow < 5:
		return 100
	}
	return 0
}

func tollsCents(row uint64, pickup, dropoff *neighborhood) int {
	u := unit(row, saltTolls)
	if pickup.airport || dropoff.airport {
		if u < 0.6 {
			return tollMain
		}
		return 0
	}
	switch {
	case u < 0.02:
		return tollMain
	case u < 0.021:
		return tollLong
	}
	return 0
}

// tipCents follows the source convention: only cash-coded rows carry a tip,
// 15 to 25 % of the fare, and 4 % of them tip nothing.
func tipCents(row uint64, fareCents int, payment string) int {
	if payment != "CSH" || unit(row, saltTip) < 0.04 {
		return 0
	}
	pct := 15 + int(11*unit(row, saltTip^1))
	return fareCents * pct / 100
}

func coords(row, salt uint64, n *neighborhood) (int, int) {
	if n.boroughIdx == boroughUnknown {
		return 0, 0
	}
	lon := n.lonMicro + int((unit(row, salt)-0.5)*16000)
	lat := n.latMicro + int((unit(row, salt^0x42)-0.5)*16000)
	return lon, lat
}

// zoneToken is a short ASCII stand-in for the source's opaque bytes.
func zoneToken(row, salt uint64) string {
	h := mix(row, salt)
	if h%1000 < 15 {
		return ""
	}
	b := []byte{base36[(h>>8)%36], base36[(h>>16)%36]}
	if h%2 == 0 {
		b = append(b, base36[(h>>24)%36])
	}
	return string(b)
}
