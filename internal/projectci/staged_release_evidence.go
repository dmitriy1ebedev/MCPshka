package projectci

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/releaseprep"
)

var stagedReceiptName = regexp.MustCompile(`^mcpshka-0\.[0-9]{1,4}\.[0-9]{1,6}\.[a-f0-9]{12}\.candidate\.ci-receipt\.json$`)

func stagedReleaseCIEvidence(root, head string) string {
	if root != "/root/repos/mcpshka" || len(head) != 40 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return checkStagedRelease(ctx, filepath.Join(root, "bin"), head, brokerclient.Run)
}

func checkStagedRelease(ctx context.Context, bin, head string, broker func(context.Context, string, ...string) ([]byte, error)) string {
	dir, err := os.OpenRoot(bin)
	if err != nil {
		return ""
	}
	defer dir.Close()
	list, err := dir.Open(".")
	if err != nil {
		return ""
	}
	entries, _ := list.ReadDir(512)
	list.Close()
	attempts := 0
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !stagedReceiptName.MatchString(name) || ctx.Err() != nil {
			continue
		}
		f, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			continue
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 8192 {
			f.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, 8193))
		f.Close()
		hint, parseErr := releaseprep.ParseLocalReceipt(data)
		if err != nil || parseErr != nil || hint.SourceSHA != head {
			continue
		}
		// The service-owned file only supplies permit IDs. Read the two
		// fenced receipts from root and bind them to the exact current HEAD.
		proof, err := broker(ctx, "executor", "release-receipt", hint.BuildPermit, hint.TestEvidence.ID)
		got, parseErr := releaseprep.ParseLocalReceipt(proof)
		if err == nil && parseErr == nil && got.SourceSHA == head && got.BuildPermit == hint.BuildPermit &&
			got.TestEvidence.ID == hint.TestEvidence.ID && got.Tree == hint.Tree &&
			got.BinarySHA == hint.BinarySHA && got.BrokerSHA == hint.BrokerSHA {
			return got.TestEvidence.Kind + " CI in root-sealed candidate for " + head[:12]
		}
		attempts++
		if attempts == 4 {
			break
		}
	}
	return ""
}
