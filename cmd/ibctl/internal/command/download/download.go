// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package download implements the "download" command.
package download

import (
	"context"

	"buf.build/go/app/appcmd"
	"buf.build/go/app/appext"
	"github.com/bufdev/ibctl/cmd/ibctl/internal/ibctlcmd"
)

// NewCommand returns a new download command that downloads and caches IBKR data.
func NewCommand(name string, builder appext.SubCommandBuilder) *appcmd.Command {
	return &appcmd.Command{
		Use:   name,
		Short: "Download and cache IBKR data via Flex Query API",
		Args:  appcmd.NoArgs,
		Run: builder.NewRunFunc(
			func(ctx context.Context, container appext.Container) error {
				return run(ctx, container)
			},
		),
	}
}

func run(ctx context.Context, container appext.Container) error {
	// Construct the downloader using shared command wiring.
	downloader, err := ibctlcmd.NewDownloader(container)
	if err != nil {
		return err
	}
	// Always re-download fresh data.
	return downloader.Download(ctx)
}
