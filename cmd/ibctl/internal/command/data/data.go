// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package data implements the "data" command group.
package data

import (
	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/data/datazip"
)

// NewCommand returns a new data command group with data management sub-commands.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	return &appcmd.Command{
		Use:   name,
		Short: "Manage ibctl data",
		SubCommands: []*appcmd.Command{
			datazip.NewCommand("zip", builder),
		},
	}
}
