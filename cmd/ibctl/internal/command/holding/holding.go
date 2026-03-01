// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package holding implements the "holding" command group.
package holding

import (
	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/holding/category"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/holding/holdinglist"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/holding/holdingvalue"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/holding/lot"
)

// NewCommand returns a new holding command group.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	return &appcmd.Command{
		Use:   name,
		Short: "Display holding information",
		SubCommands: []*appcmd.Command{
			category.NewCommand("category", builder),
			holdinglist.NewCommand("list", builder),
			lot.NewCommand("lot", builder),
			holdingvalue.NewCommand("value", builder),
		},
	}
}
