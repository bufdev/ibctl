// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package timepb provides conversion functions between xtime.Date and proto Date.
package timepb

import (
	"time"

	"buf.build/go/protovalidate"
	timev1 "github.com/bufdev/ibctl/internal/gen/proto/go/standard/time/v1"
	"github.com/bufdev/ibctl/internal/standard/xtime"
)

// NewProtoDate creates a new validated proto Date from year, month, and day.
func NewProtoDate(year int, month time.Month, day int) (*timev1.Date, error) {
	protoDate := &timev1.Date{
		Year:  uint32(year),
		Month: uint32(month),
		Day:   uint32(day),
	}
	if err := protovalidate.Validate(protoDate); err != nil {
		return nil, err
	}
	return protoDate, nil
}

// DateToProto converts an xtime.Date to a validated proto Date.
func DateToProto(date xtime.Date) (*timev1.Date, error) {
	return NewProtoDate(date.Year, date.Month, date.Day)
}

// ProtoToDate converts a validated proto Date to an xtime.Date.
func ProtoToDate(protoDate *timev1.Date) (xtime.Date, error) {
	if err := protovalidate.Validate(protoDate); err != nil {
		return xtime.Date{}, err
	}
	return xtime.Date{
		Year:  int(protoDate.GetYear()),
		Month: time.Month(protoDate.GetMonth()),
		Day:   int(protoDate.GetDay()),
	}, nil
}
