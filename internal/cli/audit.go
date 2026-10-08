package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
)

func newAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Audit-log tooling",
	}
	cmd.AddCommand(newAuditVerifyCmd())
	return cmd
}

func newAuditVerifyCmd() *cobra.Command {
	var (
		keyFile string
		segment bool
	)
	cmd := &cobra.Command{
		Use:   "verify <file>",
		Short: "Verify the hash chain of an exported audit log",
		Long: "Reads OTLP/JSON log payloads exported from a SIEM or an OTel\n" +
			"collector's file exporter (one or more payloads, concatenated or\n" +
			"one per line), keeps the chained rousseau audit records, orders\n" +
			"them by sequence and verifies the chain. Records of chain\n" +
			"version 1 and 2 are both accepted. With --key-file the HMAC on\n" +
			"every record is checked too. --segment verifies a window that\n" +
			"does not start at sequence 0. Exits non-zero on any break.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return auditVerifyRun(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], keyFile, segment)
		},
	}
	cmd.Flags().StringVar(&keyFile, "key-file", "", "audit chain HMAC key file (mode 0600); checks every record's MAC")
	cmd.Flags().BoolVar(&segment, "segment", false, "verify a window that starts mid-chain")
	return cmd
}

func auditVerifyRun(stdout, stderr io.Writer, path, keyFile string, segment bool) error {
	f, err := os.Open(path) //nolint:gosec // operator-supplied export path
	if err != nil {
		return fmt.Errorf("audit verify: %w", err)
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // read-only
	recs, err := audit_egress.ParseOTLPLogs(f)
	if err != nil {
		return fmt.Errorf("audit verify: %w", err)
	}
	if len(recs) == 0 {
		return errors.New("audit verify: no chained audit records in " + path)
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Chain.Sequence < recs[j].Chain.Sequence })
	opts := audit_egress.VerifyOptions{Segment: segment}
	if keyFile != "" {
		if opts.Key, err = readChainKey(keyFile); err != nil {
			return err
		}
	}
	if err := audit_egress.Verify(recs, opts); err != nil {
		fmt.Fprintln(stderr, "audit verify FAILED:", err) //nolint:errcheck // CLI diagnostic
		return err
	}
	first, last := recs[0].Chain.Sequence, recs[len(recs)-1].Chain.Sequence
	fmt.Fprintf(stdout, "audit verify OK: %d records, sequence %d..%d, MAC checked: %t\n", //nolint:errcheck // CLI status
		len(recs), first, last, len(opts.Key) > 0)
	return nil
}
