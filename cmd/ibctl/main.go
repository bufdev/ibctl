// Copyright 2026 Peter Edge
//
// All rights reserved.

package main

import (
	"context"

	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/config"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/data"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/download"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/holding"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/probe"
)

func main() {
	appcmd.Main(context.Background(), newRootCommand("ibctl"))
}

// newRootCommand creates the root ibctl command with all sub-commands.
func newRootCommand(name string) *appcmd.Command {
	builder := appext.NewBuilder(name)
	return &appcmd.Command{
		Use:   name,
		Short: "Analyze Interactive Brokers holdings and trades",
		Long: `Analyze Interactive Brokers holdings and trades.

All commands operate on an ibctl directory (--dir flag, defaults to current directory)
containing ibctl.yaml and well-known subdirectories for data, cache, and statements.

Run "ibctl config init" to create a new ibctl directory.`,
		BindPersistentFlags: builder.BindRoot,
		SubCommands: []*appcmd.Command{
			config.NewCommand("config", builder),
			data.NewCommand("data", builder),
			download.NewCommand("download", builder),
			holding.NewCommand("holding", builder),
			probe.NewCommand("probe", builder),
		},
	}
}
