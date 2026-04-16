package engines

import (
	"bytes"
	"testing"
)

func TestCachingPolicyRoundTrip(t *testing.T) {
	v := CachingPolicy{
		IsFresh:           true,
		NoCache:           true,
		CanRevalidate:     true,
		FreshnessLifetime: 300,
		ETag:              "abc123",
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 CachingPolicy
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if !v2.IsFresh {
		t.Fatal("IsFresh mismatch")
	}
	if !v2.NoCache {
		t.Fatal("NoCache mismatch")
	}
	if !v2.CanRevalidate {
		t.Fatal("CanRevalidate mismatch")
	}
	if v2.FreshnessLifetime != 300 {
		t.Fatal("FreshnessLifetime mismatch")
	}
	if v2.ETag != "abc123" {
		t.Fatal("ETag mismatch")
	}
}

func TestHTTPDocumentRoundTrip(t *testing.T) {
	v := HTTPDocument{
		StatusCode:  200,
		Status:      "200 OK",
		Body:        []byte("response body"),
		ContentType: "application/json",
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 HTTPDocument
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if v2.StatusCode != 200 {
		t.Fatal("StatusCode mismatch")
	}
	if v2.Status != "200 OK" {
		t.Fatal("Status mismatch")
	}
	if !bytes.Equal(v2.Body, []byte("response body")) {
		t.Fatal("Body mismatch")
	}
	if v2.ContentType != "application/json" {
		t.Fatal("ContentType mismatch")
	}
}
