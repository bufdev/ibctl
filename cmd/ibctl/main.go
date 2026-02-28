// Copyright 2026 Peter Edge
//
// All rights reserved.

package main

import (
	"context"

	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/config"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/download"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/command/holdings"
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

Configuration: ~/.config/ibctl/config.yaml (override with IBCTL_CONFIG_DIR)
Data:          ~/.local/share/ibctl        (override with IBCTL_DATA_DIR)

Run "ibctl config init" to create a configuration file.`,
		BindPersistentFlags: builder.BindRoot,
		SubCommands: []*appcmd.Command{
			config.NewCommand("config", builder),
			download.NewCommand("download", builder),
			holdings.NewCommand("holdings", builder),
			probe.NewCommand("probe", builder),
		},
	}
}
