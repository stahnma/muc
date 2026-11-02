package benchmark

import (
	"bytes"
	"compress/flate"
	"io"
	"testing"
	"time"
)

// Every host must compress exactly the same bytes, or their scores are not
// comparable. The corpus is generated, so pin it.
func TestCorpusIsDeterministic(t *testing.T) {
	a, b := makeCorpus(corpusSize), makeCorpus(corpusSize)
	if !bytes.Equal(a, b) {
		t.Fatal("two corpora built from the same seed differ")
	}
	if len(a) != corpusSize {
		t.Fatalf("corpus is %d bytes, want %d", len(a), corpusSize)
	}
	if got := string(a[:32]); got != string(makeCorpus(32)) {
		t.Fatalf("corpus prefix depends on size: %q", got)
	}
}

// The corpus should be text-like: compressible, but not trivially. Either
// extreme would time something other than general-purpose compression.
func TestCorpusCompressesLikeText(t *testing.T) {
	corpus := makeCorpus(corpusSize)
	var buf bytes.Buffer
	fw, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	fw.Write(corpus)
	fw.Close()

	ratio := float64(buf.Len()) / float64(len(corpus))
	if ratio < 0.15 || ratio > 0.6 {
		t.Fatalf("compressed to %.2f of original; want text-like 0.15–0.6", ratio)
	}

	// And it must round-trip, or the workload is not doing what it claims.
	out, err := io.ReadAll(flate.NewReader(&buf))
	if err != nil || !bytes.Equal(out, corpus) {
		t.Fatalf("corpus did not round-trip: %v", err)
	}
}

func TestMeasureReportsThroughput(t *testing.T) {
	corpus := makeCorpus(corpusSize)
	if got := measure(corpus, 1, 50*time.Millisecond); got <= 0 {
		t.Fatalf("single-worker throughput = %v, want > 0", got)
	}
	if got := measure(corpus, 2, 50*time.Millisecond); got <= 0 {
		t.Fatalf("two-worker throughput = %v, want > 0", got)
	}
}
