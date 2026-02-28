// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package debug implements the "debug" command group.
package debug

import (
	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/debug/debugprobe"
)

// NewCommand returns a new debug command group.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	return &appcmd.Command{
		Use:   name,
		Short: "Debug and diagnostic commands",
		SubCommands: []*appcmd.Command{
			debugprobe.NewCommand("probe", builder),
		},
	}
}
