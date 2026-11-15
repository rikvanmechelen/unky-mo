package web

import (
	"bytes"
	"testing"
	"time"
)

func TestCertSourceFollowsHosts(t *testing.T) {
	dir := t.TempDir()
	hosts := []string{"localhost", "192.168.1.20"}
	now := time.Now()
	src, res, err := NewCertSource(dir, func() []string { return hosts }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	first, _ := src.GetCertificate(nil)
	if src.Leaf().VerifyHostname("192.168.1.20") != nil {
		t.Fatal("first leaf doesn't cover its IP")
	}

	// Same hosts: nothing changes.
	changed, err := src.Refresh()
	if err != nil || changed {
		t.Fatalf("Refresh with the same hosts = %v, %v", changed, err)
	}
	if again, _ := src.GetCertificate(nil); again != first {
		t.Error("an unchanged host list swapped the cert")
	}

	// The laptop moved to another network.
	hosts = []string{"localhost", "10.1.4.7"}
	changed, err = src.Refresh()
	if err != nil || !changed {
		t.Fatalf("Refresh after a network change = %v, %v", changed, err)
	}
	if err := src.Leaf().VerifyHostname("10.1.4.7"); err != nil {
		t.Errorf("new leaf doesn't cover the new IP: %v", err)
	}
	cur, _ := src.GetCertificate(nil)
	if cur == first || !bytes.Equal(cur.Certificate[0], src.Leaf().Raw) {
		t.Error("GetCertificate didn't return the new cert")
	}
	if err := src.Leaf().CheckSignatureFrom(res.CA); err != nil {
		t.Errorf("new leaf isn't signed by the same CA: %v", err)
	}
}

func TestCertSourceRenewsNearExpiry(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	src, _, err := NewCertSource(dir, func() []string { return []string{"localhost"} }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	old := src.Leaf()
	now = old.NotAfter.Add(-leafRenewBefore + time.Hour)
	changed, err := src.Refresh()
	if err != nil || !changed {
		t.Fatalf("Refresh near expiry = %v, %v", changed, err)
	}
	if !src.Leaf().NotAfter.After(old.NotAfter) {
		t.Error("leaf wasn't renewed")
	}
}
