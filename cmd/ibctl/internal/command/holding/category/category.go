// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package category implements the "holding category" command group.
package category

import (
	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/holding/category/categorylist"
)

// NewCommand returns a new category command group.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	return &appcmd.Command{
		Use:   name,
		Short: "Display holdings by category",
		SubCommands: []*appcmd.Command{
			categorylist.NewCommand("list", builder),
		},
	}
}
