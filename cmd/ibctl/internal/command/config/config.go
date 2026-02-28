// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package config implements the "config" command group.
package config

import (
	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/config/configedit"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/config/configinit"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/config/configvalidate"
)

// NewCommand returns a new config command group with init, edit, and validate sub-commands.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	return &appcmd.Command{
		Use:   name,
		Short: "Manage ibctl configuration",
		SubCommands: []*appcmd.Command{
			configinit.NewCommand("init", builder),
			configedit.NewCommand("edit", builder),
			configvalidate.NewCommand("validate", builder),
		},
	}
}
