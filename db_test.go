package main

import (
	"math/rand"
	"strings"
	"testing"
)

func TestBlogIDRange_Constants(t *testing.T) {
	if blogIDBase != 1_500_000_000 {
		t.Errorf("blogIDBase = %d, want 1500000000", blogIDBase)
	}
	if blogIDRange != 500_000_000 {
		t.Errorf("blogIDRange = %d, want 500000000", blogIDRange)
	}
	if blogIDBase+blogIDRange-1 > jetmonV1MaxSignedInt {
		t.Errorf("synthetic blog_id range upper bound %d exceeds Jetmon v1 signed int max %d", blogIDBase+blogIDRange-1, jetmonV1MaxSignedInt)
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
		if !isJetmonV1SafeBlogID(id) {
			t.Fatalf("sample %d: blog_id %d is not Jetmon v1 safe", i, id)
		}
	}
}

// TestBlogIDRange_NoCollisionWithWordPressBlogIDs verifies the synthetic range
// is well above typical WordPress blog_ids. Real blog_ids are in the millions;
// blogIDBase is 1.5B while staying below Jetmon v1's signed int limit.
func TestBlogIDRange_NoCollisionWithWordPressBlogIDs(t *testing.T) {
	const maxRealBlogID = int64(1_000_000_000) // generous upper bound for real blog_ids
	if blogIDBase <= maxRealBlogID {
		t.Errorf("blogIDBase %d is not safely above real WordPress blog_id range", blogIDBase)
	}
}

func TestIsJetmonV1SafeBlogID(t *testing.T) {
	cases := map[int64]bool{
		-1:                                  false,
		0:                                   false,
		blogIDBase:                          true,
		blogIDBase + blogIDRange - 1:        true,
		jetmonV1MaxSignedInt:                true,
		jetmonV1MaxSignedInt + 1:            false,
		int64(1) << 62:                      false,
		(int64(1) << 62) + (int64(1) << 30): false,
	}
	for id, want := range cases {
		if got := isJetmonV1SafeBlogID(id); got != want {
			t.Errorf("isJetmonV1SafeBlogID(%d) = %v, want %v", id, got, want)
		}
	}
}

func TestWriteModeSQLResetsMonitorState(t *testing.T) {
	cases := map[string]string{
		"reactivate":              sqlReactivateMonitor,
		"reactivate-with-blog-id": sqlReactivateMonitorWithBlogID,
		"deactivate":              sqlDeactivateMonitor,
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
