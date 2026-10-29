/*
Copyright 2026 Kubotal

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"kubocd/internal/archiveio"

	"github.com/spf13/cobra"
)

const archiveHelp = `Archive budgets apply per traversal, not per command.
Each archive KuboCD opens has its own budgets.
Defaults: 47087616 decompressed bytes, 41943040 logical content bytes, 1000 entries,
1048576 bytes per metadata file, and 1048576 bytes per listing.
Skipped entries, headers and padding count. Downloads and Helm subprocess resource use are outside these budgets.
KuboCD limits apply to its own archive reads; the bundled Helm loader has a separate 100 MiB content limit.
The PATH Helm executable can have different limits. Embedded charts loaded only by Helm use Helm's limits.
Raise individual limits or use --disableArchiveLimits for unbounded resource handling.
Failed extraction can leave partial output.
Path checks and complete tar/gzip validation remain enabled. All numeric limits must be positive, even when disabled.

Examples:
  kubocd dump package package.yaml --charts --maxArchiveContentSize=83886080 --maxArchiveStreamSize=94371840
  kubocd render release.yaml --maxArchiveMetadataSize=2097152
  kubocd dump package package.yaml --charts --disableArchiveLimits`

func registerArchiveFlags(cmd *cobra.Command) *archiveio.Limits {
	limits := archiveio.Defaults()
	flags := cmd.Flags()
	flags.Int64Var(&limits.StreamSize, "maxArchiveStreamSize", limits.StreamSize,
		"Maximum decompressed bytes per archive, including headers, skipped entries and padding.")
	flags.Int64Var(&limits.ContentSize, "maxArchiveContentSize", limits.ContentSize,
		"Maximum logical content bytes per archive, including skipped and duplicate entries.")
	flags.Int64Var(&limits.Entries, "maxArchiveEntries", limits.Entries,
		"Maximum entries per archive, including implicit parent directories.")
	flags.Int64Var(&limits.MetadataSize, "maxArchiveMetadataSize", limits.MetadataSize,
		"Maximum bytes per archive metadata file read or decoded.")
	flags.Int64Var(&limits.ListingSize, "maxArchiveListingSize", limits.ListingSize,
		"Maximum bytes per archive listing, including newlines.")
	flags.BoolVar(&limits.Disabled, "disableArchiveLimits", false,
		"Disable CLI archive resource limits; path checks and archive validation remain enabled.")
	cmd.PreRunE = func(_ *cobra.Command, _ []string) error { return limits.Validate() }
	if cmd.Long == "" {
		cmd.Long = cmd.Short
	}
	cmd.Long += "\n\n" + archiveHelp
	return &limits
}
