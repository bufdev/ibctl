// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package lot implements the "holding lot" command group.
package lot

import (
	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/holding/lot/lotlist"
)

// NewCommand returns a new lot command group.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	return &appcmd.Command{
		Use:   name,
		Short: "Display individual tax lots",
		SubCommands: []*appcmd.Command{
			lotlist.NewCommand("list", builder),
		},
	}
}
