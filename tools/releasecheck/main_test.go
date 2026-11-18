package main

import "testing"

func TestCanonicalDistributionVersions(t *testing.T) {
	for _, version := range []string{"v0.1.0-rc.1", "v1.0.0-rc.2", "v1.0.0", "v1.2.3"} {
		if !validVersion(version) {
			t.Fatalf("valid distribution rejected: %s", version)
		}
	}
	for _, version := range []string{"", "1.0.0", "v1.0", "v1.00.0", "v1.0.0-rc.0", "v1.0.0-rc.01", "v1.0.0-beta.1", "v2.0.0", "v1.0.0+metadata", "v2.0.0+incompatible"} {
		if validVersion(version) {
			t.Fatalf("unsafe/unsupported distribution accepted: %s", version)
		}
	}
}
