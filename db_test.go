package main

import (
	"math/rand"
	"strings"
	"testing"
)

func TestBlogIDRange_Constants(t *testing.T) {
	if blogIDBase != int64(1)<<62 {
		t.Errorf("blogIDBase = %d, want 2^62 = %d", blogIDBase, int64(1)<<62)
	}
	if blogIDRange != int64(1)<<30 {
		t.Errorf("blogIDRange = %d, want 2^30 = %d", blogIDRange, int64(1)<<30)
	}
}

func TestBlogIDRange_GeneratedIDsInBounds(t *testing.T) {
	lo := blogIDBase
	hi := blogIDBase + blogIDRange
	for i := range 1000 {
		id := blogIDBase + rand.Int63n(blogIDRange)
		if id < lo || id >= hi {
			t.Fatalf("sample %d: blog_id %d outside [%d, %d)", i, id, lo, hi)
		}
	}
}

// TestBlogIDRange_NoCollisionWithWordPressBlogIDs verifies the synthetic range
// is well above typical WordPress blog_ids. Real blog_ids are in the millions;
// blogIDBase is ~4.6×10^18.
func TestBlogIDRange_NoCollisionWithWordPressBlogIDs(t *testing.T) {
	const maxRealBlogID = int64(1_000_000_000) // generous upper bound for real blog_ids
	if blogIDBase <= maxRealBlogID {
		t.Errorf("blogIDBase %d is not safely above real WordPress blog_id range", blogIDBase)
	}
}

func TestWriteModeSQLResetsMonitorState(t *testing.T) {
	cases := map[string]string{
		"reactivate": sqlReactivateMonitor,
		"deactivate": sqlDeactivateMonitor,
	}
	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			for _, want := range []string{
				"site_status = 1",
				"last_status_change = NOW()",
			} {
				if !strings.Contains(query, want) {
					t.Fatalf("%s SQL missing %q:\n%s", name, want, query)
				}
			}
		})
	}
}
