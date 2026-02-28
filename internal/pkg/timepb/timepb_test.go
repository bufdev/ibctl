// Copyright 2026 Peter Edge
//
// All rights reserved.

package timepb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBasic(t *testing.T) {
	t.Parallel()
	// Invalid date: month 13, day 60 should fail validation.
	_, err := NewProtoDate(2005, time.Month(13), 60)
	require.Error(t, err)
}
