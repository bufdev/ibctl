// Copyright 2026 Peter Edge
//
// All rights reserved.

package main

import (
	"context"

	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/analyze"
)

func main() {
	appcmd.Main(context.Background(), newRootCommand("ibctl"))
}

// newRootCommand creates the root ibctl command with all sub-commands.
func newRootCommand(name string) *appcmd.Command {
	builder := appext.NewBuilder(name)
	return &appcmd.Command{
		Use:                 name,
		BindPersistentFlags: builder.BindRoot,
		SubCommands: []*appcmd.Command{
			analyze.NewCommand("analyze", builder),
		},
	}
}
