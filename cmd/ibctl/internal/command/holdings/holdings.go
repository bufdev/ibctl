// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package holdings implements the "holdings" command group.
package holdings

import (
	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/holdings/holdingsoverview"
)

// NewCommand returns a new holdings command group.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	return &appcmd.Command{
		Use:   name,
		Short: "Display holdings information",
		SubCommands: []*appcmd.Command{
			holdingsoverview.NewCommand("overview", builder),
		},
	}
}
