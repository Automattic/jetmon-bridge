package main

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestIsDuplicateKey(t *testing.T) {
	dup := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry 'x' for key 'idx_blog_id'"}

	if !isDuplicateKey(dup) {
		t.Fatal("expected true for MySQL error 1062")
	}
	if isDuplicateKey(&mysql.MySQLError{Number: 1045, Message: "Access denied"}) {
		t.Fatal("expected false for MySQL error 1045")
	}
	if isDuplicateKey(errors.New("some generic error")) {
		t.Fatal("expected false for non-MySQL error")
	}

	// errors.As must traverse wrapping chains.
	if !isDuplicateKey(fmt.Errorf("insert monitor: %w", dup)) {
		t.Fatal("expected true for wrapped duplicate key error")
	}
}

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
